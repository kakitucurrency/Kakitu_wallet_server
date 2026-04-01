package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kakitucurrency/kakitu-wallet-server/database"
	"github.com/kakitucurrency/kakitu-wallet-server/models"
	"github.com/kakitucurrency/kakitu-wallet-server/net"
	"github.com/kakitucurrency/kakitu-wallet-server/repository"
	"github.com/kakitucurrency/kakitu-wallet-server/utils"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/mitchellh/mapstructure"
	"golang.org/x/exp/slices"
	"k8s.io/klog/v2"
)

const (
	// Time allowed to write a message to the peer.
	WriteWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	PongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	PingPeriod = (PongWait * 9) / 10

	// Maximum message size allowed from peer.
	MaxMessageSize = 512
)

// Client is a middleman between the websocket connection and the hub.
type Client struct {
	Hub *Hub

	// The websocket connection.
	Conn *websocket.Conn

	// Buffered channel of outbound messages.
	Send chan []byte

	// IP Address
	IPAddress string
	ID        uuid.UUID
	Accounts  []string // Subscribed accounts
	Currency  string

	Mutex sync.Mutex
}

var Upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// WebSocket connections are already gated by the connection
		// tracker. The CORS policy on HTTP routes does not apply to
		// WS upgrades, so we perform a lightweight origin check here.
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser clients (mobile wallet)
		}
		if utils.GetEnv("ENVIRONMENT", "development") == "development" {
			return true
		}
		for _, suffix := range []string{".kakitu.org", ".kakitu.africa", "://kakitu.org", "://kakitu.africa"} {
			if strings.HasSuffix(origin, suffix) {
				return true
			}
		}
		return false
	},
}

// allowedWSActions is the set of action types accepted over the WebSocket.
var allowedWSActions = map[string]bool{
	"account_subscribe": true,
	"fcm_update":        true,
}

// Connection limit constants.
const (
	MaxConnectionsPerIP = 5
	MaxGlobalConnections = 10000
)

// connTracker tracks WebSocket connections per IP and globally.
type connTracker struct {
	mu       sync.Mutex
	perIP    map[string]int
	total    int
}

var tracker = &connTracker{
	perIP: make(map[string]int),
}

func (ct *connTracker) tryAdd(ip string) bool {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if ct.total >= MaxGlobalConnections {
		return false
	}
	if ct.perIP[ip] >= MaxConnectionsPerIP {
		return false
	}
	ct.perIP[ip]++
	ct.total++
	return true
}

func (ct *connTracker) remove(ip string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if ct.perIP[ip] > 0 {
		ct.perIP[ip]--
		if ct.perIP[ip] == 0 {
			delete(ct.perIP, ip)
		}
	}
	if ct.total > 0 {
		ct.total--
	}
}

// Hub maintains the set of active clients and broadcasts messages to the
// clients.
type Hub struct {
	// Registered clients.
	Clients map[*Client]bool

	// Mutex to protect Clients map from concurrent access.
	ClientsMu sync.RWMutex

	// Outbound messages to the client
	Broadcast chan []byte

	// Register requests from the clients.
	Register chan *Client

	// Unregister requests from clients.
	Unregister chan *Client

	PricePrefix string

	RPCClient    *net.RPCClient
	FcmTokenRepo *repository.FcmTokenRepo
}

func NewHub(rpcClient *net.RPCClient, fcmTokenRepo *repository.FcmTokenRepo) *Hub {
	return &Hub{
		Broadcast:    make(chan []byte),
		Register:     make(chan *Client),
		Unregister:   make(chan *Client),
		Clients:      make(map[*Client]bool),
		PricePrefix:  "kshs",
		RPCClient:    rpcClient,
		FcmTokenRepo: fcmTokenRepo,
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.Register:
			h.ClientsMu.Lock()
			h.Clients[client] = true
			h.ClientsMu.Unlock()
		case client := <-h.Unregister:
			h.ClientsMu.Lock()
			if _, ok := h.Clients[client]; ok {
				delete(h.Clients, client)
				close(client.Send)
			}
			h.ClientsMu.Unlock()
		case message := <-h.Broadcast:
			h.ClientsMu.Lock()
			for client := range h.Clients {
				select {
				case client.Send <- message:
				default:
					close(client.Send)
					delete(h.Clients, client)
				}
			}
			h.ClientsMu.Unlock()
		}
	}
}

// GetClients returns a snapshot of the current clients map.
// Callers outside the Hub.Run goroutine MUST use this instead of accessing Clients directly.
func (h *Hub) GetClients() []*Client {
	h.ClientsMu.RLock()
	defer h.ClientsMu.RUnlock()
	clients := make([]*Client, 0, len(h.Clients))
	for c := range h.Clients {
		clients = append(clients, c)
	}
	return clients
}

// GetSubscribedAccounts returns the set of all accounts that any connected
// client is currently subscribed to. Used for early-exit filtering of node WS
// confirmations so we skip blocks that no client cares about.
func (h *Hub) GetSubscribedAccounts() map[string]struct{} {
	h.ClientsMu.RLock()
	defer h.ClientsMu.RUnlock()
	accounts := make(map[string]struct{})
	for c := range h.Clients {
		c.Mutex.Lock()
		for _, a := range c.Accounts {
			accounts[a] = struct{}{}
		}
		c.Mutex.Unlock()
	}
	return accounts
}

func (h *Hub) BroadcastToClient(client *Client, message []byte) {
	client.Mutex.Lock()
	defer client.Mutex.Unlock()
	select {
	case client.Send <- message:
	default:
		klog.Warningf("Client send buffer full, dropping message for %s", client.ID)
	}
}

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

// readPump pumps messages from the websocket connection to the hub.
//
// The application runs readPump in a per-connection goroutine. The application
// ensures that there is at most one reader on a connection by executing all
// reads from this goroutine.
func (c *Client) readPump() {
	defer func() {
		tracker.remove(c.IPAddress)
		c.Hub.Unregister <- c
		c.Conn.Close()
	}()
	c.Conn.SetReadLimit(MaxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(PongWait))
	c.Conn.SetPongHandler(func(string) error { c.Conn.SetReadDeadline(time.Now().Add(PongWait)); return nil })
	for {
		_, msg, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				klog.Errorf("error: %v", err)
			}
			break
		}
		msg = bytes.TrimSpace(bytes.Replace(msg, newline, space, -1))

		// Process message
		// Determine type of message and unMarshal
		var baseRequest map[string]interface{}
		if err = json.Unmarshal(msg, &baseRequest); err != nil {
			klog.Errorf("Error unmarshalling websocket base request %s", err)
			errJson, _ := json.Marshal(InvalidRequestError)
			c.Hub.BroadcastToClient(c, errJson)
			continue
		}

		if _, ok := baseRequest["action"]; !ok {
			errJson, _ := json.Marshal(InvalidRequestError)
			c.Hub.BroadcastToClient(c, errJson)
			continue
		}

		// Validate action against allowed list (Fix 5)
		actionStr, ok := baseRequest["action"].(string)
		if !ok || !allowedWSActions[actionStr] {
			errJson, _ := json.Marshal(ErrorResponse{Error: fmt.Sprintf("Unsupported action: %v", baseRequest["action"])})
			c.Hub.BroadcastToClient(c, errJson)
			continue
		}

		if baseRequest["action"] == "account_subscribe" {
			var subscribeRequest models.AccountSubscribe
			if err = mapstructure.Decode(baseRequest, &subscribeRequest); err != nil {
				klog.Errorf("Error unmarshalling websocket subscribe request %s", err)
				errJson, _ := json.Marshal(InvalidRequestError)
				c.Hub.BroadcastToClient(c, errJson)
				continue
			}
			// Check if account is valid
			if !utils.ValidateAddress(subscribeRequest.Account) {
				klog.Errorf("Invalid account %s", subscribeRequest.Account)
				c.Hub.BroadcastToClient(c, []byte("{\"error\":\"Invalid account\"}"))
				continue
			}

			// Handle subscribe
			// If UUID is present and valid, use that, otherwise generate a new one
			if subscribeRequest.Uuid != nil {
				id, err := uuid.Parse(*subscribeRequest.Uuid)
				if err != nil {
					c.ID = uuid.New()
				} else {
					c.ID = id
				}
			} else {
				// Create a UUID for this subscription
				c.ID = uuid.New()
			}
			// Get curency (protected by mutex — read from price cron).
			c.Mutex.Lock()
			if subscribeRequest.Currency != nil && slices.Contains(net.CurrencyList, strings.ToUpper(*subscribeRequest.Currency)) {
				c.Currency = strings.ToUpper(*subscribeRequest.Currency)
			} else {
				c.Currency = "USD"
			}
			c.Mutex.Unlock()
			// Normalize address prefix to kshs_
			if strings.HasPrefix(subscribeRequest.Account, "xrb_") {
				subscribeRequest.Account = fmt.Sprintf("kshs_%s", strings.TrimPrefix(subscribeRequest.Account, "xrb_"))
			} else if strings.HasPrefix(subscribeRequest.Account, "nano_") {
				subscribeRequest.Account = fmt.Sprintf("kshs_%s", strings.TrimPrefix(subscribeRequest.Account, "nano_"))
			}

			klog.Infof("Received account_subscribe: %s, %s", subscribeRequest.Account, c.IPAddress)

			// Get account info
			accountInfo, err := c.Hub.RPCClient.MakeAccountInfoRequest(subscribeRequest.Account)
			if err != nil || accountInfo == nil {
				klog.Errorf("Error getting account info %v", err)
				c.Hub.BroadcastToClient(c, []byte("{\"error\":\"subscribe error\"}"))
				continue
			}

			// Add account to tracker (protected by mutex — Accounts is read
			// from the callback goroutine and price cron concurrently).
			c.Mutex.Lock()
			if !slices.Contains(c.Accounts, subscribeRequest.Account) {
				c.Accounts = append(c.Accounts, subscribeRequest.Account)
			}
			c.Mutex.Unlock()

			// Get price info to include in response
			priceCur, err := database.GetRedisDB().Hget("prices", fmt.Sprintf("coingecko:%s-%s", c.Hub.PricePrefix, strings.ToLower(c.Currency)))
			if err != nil {
				klog.Errorf("Error getting price %s %v", fmt.Sprintf("coingecko:%s-%s", c.Hub.PricePrefix, strings.ToLower(c.Currency)), err)
			}
			priceBtc, err := database.GetRedisDB().Hget("prices", fmt.Sprintf("coingecko:%s-btc", c.Hub.PricePrefix))
			if err != nil {
				klog.Errorf("Error getting BTC price %v", err)
			}
			accountInfo["uuid"] = c.ID
			accountInfo["currency"] = c.Currency
			accountInfo["price"] = priceCur
			accountInfo["btc"] = priceBtc

			// Tag pending count
			pendingCount, err := c.Hub.RPCClient.GetReceivableCount(subscribeRequest.Account)
			if err != nil {
				klog.Errorf("Error getting pending count %v", err)
			}
			accountInfo["pending_count"] = pendingCount

			// Send our finished response
			response, err := json.Marshal(accountInfo)
			if err != nil {
				klog.Errorf("Error marshalling account info %v", err)
				c.Hub.BroadcastToClient(c, []byte("{\"error\":\"subscribe error\"}"))
				continue
			}
			c.Hub.BroadcastToClient(c, response)

			// The user may have a different UUID every time, 1 token, and multiple accounts
			// We store account/token in postgres since that's what we care about
			// Or remove the token, if notifications disabled
			if !subscribeRequest.NotificationEnabled {
				// Set token in db
				c.Hub.FcmTokenRepo.DeleteFcmToken(subscribeRequest.FcmToken, subscribeRequest.Account)
			} else {
				// Add/update token if not exists
				c.Hub.FcmTokenRepo.AddOrUpdateToken(subscribeRequest.FcmToken, subscribeRequest.Account)
			}
		} else if baseRequest["action"] == "fcm_update" {
			// Update FCM/notification preferences
			var fcmUpdateRequest models.FcmUpdate
			if err = mapstructure.Decode(baseRequest, &fcmUpdateRequest); err != nil {
				klog.Errorf("Error unmarshalling websocket fcm_update request %s", err)
				errJson, _ := json.Marshal(InvalidRequestError)
				c.Hub.BroadcastToClient(c, errJson)
				continue
			}
			// Check if account is valid
			if !utils.ValidateAddress(fcmUpdateRequest.Account) {
				c.Hub.BroadcastToClient(c, []byte("{\"error\":\"Invalid account\"}"))
				continue
			}
			// Do the updoot
			if !fcmUpdateRequest.Enabled {
				// Set token in db
				c.Hub.FcmTokenRepo.DeleteFcmToken(fcmUpdateRequest.FcmToken, fcmUpdateRequest.Account)
			} else {
				// Add token to db if not exists
				c.Hub.FcmTokenRepo.AddOrUpdateToken(fcmUpdateRequest.FcmToken, fcmUpdateRequest.Account)
			}
		}
	}
}

// writePump pumps messages from the hub to the websocket connection.
//
// A goroutine running writePump is started for each connection. The
// application ensures that there is at most one writer to a connection by
// executing all writes from this goroutine.
func (c *Client) writePump() {
	ticker := time.NewTicker(PingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(WriteWait))
			if !ok {
				// The hub closed the channel.
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.Conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)
			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(WriteWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Handles a ws connection request from user
func WebsocketChl(hub *Hub, w http.ResponseWriter, r *http.Request) {
	clientIP := utils.IPAddress(r)

	if !tracker.tryAdd(clientIP) {
		klog.Warningf("Connection limit exceeded for IP %s (per-IP: %d, global: %d)", clientIP, MaxConnectionsPerIP, MaxGlobalConnections)
		http.Error(w, "Too many connections", http.StatusServiceUnavailable)
		return
	}

	conn, err := Upgrader.Upgrade(w, r, nil)
	if err != nil {
		tracker.remove(clientIP)
		klog.Error(err)
		return
	}
	client := &Client{Hub: hub, Conn: conn, Send: make(chan []byte, 256), IPAddress: clientIP, Accounts: []string{}}
	client.Hub.Register <- client

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go client.writePump()
	go client.readPump()
}
