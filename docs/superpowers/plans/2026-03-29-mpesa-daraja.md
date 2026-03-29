# M-Pesa Daraja Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add M-Pesa STK Push (cash-in) and B2C (cash-out) to kakitu_wallet_server so users can convert between KES and KSHS.

**Architecture:** New `mpesa/` package handles all Daraja API calls; `repository/mpesa_txn_repo.go` handles DB ops; `controller/mpesa_c.go` handles HTTP; `utils/treasury.go` handles treasury wallet signing. Routes mounted in `main.go`.

**Tech Stack:** Go, chi router, GORM/Postgres, go-redis, Safaricom Daraja API v2, existing `utils/ed25519` for block signing.

---

## File Map

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `models/dbmodels/mpesa_txns.go` | GORM model for mpesa_transactions table |
| Create | `mpesa/models.go` | Daraja request/response structs |
| Create | `mpesa/auth.go` | OAuth token fetch + Redis cache |
| Create | `mpesa/stk.go` | STK Push initiation |
| Create | `mpesa/b2c.go` | B2C transfer initiation |
| Create | `repository/mpesa_txn_repo.go` | DB operations for mpesa_transactions |
| Create | `utils/treasury.go` | Nano seed → keypair, block hash, sign, send KSHS |
| Create | `controller/mpesa_c.go` | HTTP handlers for all M-Pesa routes |
| Modify | `database/postgres.go` | Add MpesaTransaction to Migrate() |
| Modify | `main.go` | Mount /mpesa routes, pass MpesaTxnRepo to controller |

---

## Task 1: DB Model — mpesa_transactions

**Files:**
- Create: `models/dbmodels/mpesa_txns.go`

- [ ] **Step 1: Create the model file**

```go
// models/dbmodels/mpesa_txns.go
package dbmodels

import "github.com/shopspring/decimal"

// MpesaTransaction records every M-Pesa cash-in and cash-out event
type MpesaTransaction struct {
	Base
	Type            string          `json:"type" gorm:"not null"`             // "cashin" | "cashout"
	AmountKes       decimal.Decimal `json:"amount_kes" gorm:"type:decimal(10,2);not null"`
	KshsAddress     string          `json:"kshs_address" gorm:"not null"`
	MerchantReqID   string          `json:"merchant_req_id" gorm:"uniqueIndex"` // CheckoutRequestID or ConversationID
	MpesaReceipt    string          `json:"mpesa_receipt"`
	TxHash          string          `json:"tx_hash" gorm:"uniqueIndex"`         // on-chain hash (cashout only)
	Status          string          `json:"status" gorm:"not null;default:'pending'"` // pending | confirmed | failed
}
```

- [ ] **Step 2: Check shopspring/decimal is available**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server
grep "shopspring" go.mod
```

If not present, run:
```bash
go get github.com/shopspring/decimal
```

- [ ] **Step 3: Add to Migrate() in database/postgres.go**

In `database/postgres.go`, change:
```go
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&dbmodels.FcmToken{})
}
```
To:
```go
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&dbmodels.FcmToken{}, &dbmodels.MpesaTransaction{})
}
```

- [ ] **Step 4: Verify it compiles**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server
go build ./...
```
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add models/dbmodels/mpesa_txns.go database/postgres.go go.mod go.sum
git commit -m "feat: add mpesa_transactions DB model and migration"
```

---

## Task 2: Daraja Models

**Files:**
- Create: `mpesa/models.go`

- [ ] **Step 1: Create the models file**

```go
// mpesa/models.go
package mpesa

// ── Auth ──────────────────────────────────────────────────────────────────────

type authResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   string `json:"expires_in"`
}

// ── STK Push ──────────────────────────────────────────────────────────────────

type STKPushRequest struct {
	BusinessShortCode string `json:"BusinessShortCode"`
	Password          string `json:"Password"`
	Timestamp         string `json:"Timestamp"`
	TransactionType   string `json:"TransactionType"`
	Amount            string `json:"Amount"`
	PartyA            string `json:"PartyA"`  // phone number
	PartyB            string `json:"PartyB"`  // shortcode
	PhoneNumber       string `json:"PhoneNumber"`
	CallBackURL       string `json:"CallBackURL"`
	AccountReference  string `json:"AccountReference"`
	TransactionDesc   string `json:"TransactionDesc"`
}

type STKPushResponse struct {
	MerchantRequestID   string `json:"MerchantRequestID"`
	CheckoutRequestID   string `json:"CheckoutRequestID"`
	ResponseCode        string `json:"ResponseCode"`
	ResponseDescription string `json:"ResponseDescription"`
	CustomerMessage     string `json:"CustomerMessage"`
}

// STKCallbackBody is what Safaricom POSTs to /mpesa/cashin/callback
type STKCallbackBody struct {
	Body struct {
		StkCallback struct {
			MerchantRequestID string `json:"MerchantRequestID"`
			CheckoutRequestID string `json:"CheckoutRequestID"`
			ResultCode        int    `json:"ResultCode"`
			ResultDesc        string `json:"ResultDesc"`
			CallbackMetadata  *struct {
				Item []struct {
					Name  string      `json:"Name"`
					Value interface{} `json:"Value"`
				} `json:"Item"`
			} `json:"CallbackMetadata"`
		} `json:"stkCallback"`
	} `json:"Body"`
}

// ── B2C ───────────────────────────────────────────────────────────────────────

type B2CRequest struct {
	InitiatorName      string `json:"InitiatorName"`
	SecurityCredential string `json:"SecurityCredential"`
	CommandID          string `json:"CommandID"`
	Amount             string `json:"Amount"`
	PartyA             string `json:"PartyA"` // shortcode
	PartyB             string `json:"PartyB"` // phone number
	Remarks            string `json:"Remarks"`
	QueueTimeOutURL    string `json:"QueueTimeOutURL"`
	ResultURL          string `json:"ResultURL"`
	Occasion           string `json:"Occasion"`
}

type B2CResponse struct {
	ConversationID          string `json:"ConversationID"`
	OriginatorConversationID string `json:"OriginatorConversationID"`
	ResponseCode            string `json:"ResponseCode"`
	ResponseDescription     string `json:"ResponseDescription"`
}

// B2CResultBody is what Safaricom POSTs to /mpesa/cashout/callback
type B2CResultBody struct {
	Result struct {
		ResultType               int    `json:"ResultType"`
		ResultCode               int    `json:"ResultCode"`
		ResultDesc               string `json:"ResultDesc"`
		OriginatorConversationID string `json:"OriginatorConversationID"`
		ConversationID           string `json:"ConversationID"`
		TransactionID            string `json:"TransactionID"`
	} `json:"Result"`
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server
go build ./...
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add mpesa/models.go
git commit -m "feat: add Daraja API request/response models"
```

---

## Task 3: Daraja Auth

**Files:**
- Create: `mpesa/auth.go`

- [ ] **Step 1: Create auth.go**

```go
// mpesa/auth.go
package mpesa

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/kakitucurrency/kakitu-wallet-server/database"
	"github.com/kakitucurrency/kakitu-wallet-server/utils"
)

const redisTokenKey = "mpesa:oauth_token"

// BaseURL returns the Daraja base URL based on MPESA_ENVIRONMENT env var.
func BaseURL() string {
	if utils.GetEnv("MPESA_ENVIRONMENT", "sandbox") == "production" {
		return "https://api.safaricom.co.ke"
	}
	return "https://sandbox.safaricom.co.ke"
}

// GetToken returns a valid Daraja OAuth token, using Redis cache when possible.
func GetToken() (string, error) {
	// Try cache first
	cached, err := database.GetRedisDB().Get(redisTokenKey)
	if err == nil && cached != "" {
		return cached, nil
	}

	// Fetch fresh token
	consumerKey := utils.GetEnv("MPESA_CONSUMER_KEY", "")
	consumerSecret := utils.GetEnv("MPESA_CONSUMER_SECRET", "")
	if consumerKey == "" || consumerSecret == "" {
		return "", fmt.Errorf("MPESA_CONSUMER_KEY and MPESA_CONSUMER_SECRET must be set")
	}

	url := fmt.Sprintf("%s/oauth/v1/generate?grant_type=client_credentials", BaseURL())
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("building auth request: %w", err)
	}
	creds := base64.StdEncoding.EncodeToString([]byte(consumerKey + ":" + consumerSecret))
	req.Header.Set("Authorization", "Basic "+creds)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling Daraja auth: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading auth response: %w", err)
	}

	var authResp authResponse
	if err := json.Unmarshal(body, &authResp); err != nil {
		return "", fmt.Errorf("parsing auth response: %w", err)
	}
	if authResp.AccessToken == "" {
		return "", fmt.Errorf("empty access token from Daraja: %s", string(body))
	}

	// Cache with TTL = expires_in - 60 seconds
	expiresIn, err := strconv.Atoi(authResp.ExpiresIn)
	if err != nil {
		expiresIn = 3600
	}
	ttl := time.Duration(expiresIn-60) * time.Second
	_ = database.GetRedisDB().Set(redisTokenKey, authResp.AccessToken, ttl)

	return authResp.AccessToken, nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server
go build ./...
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add mpesa/auth.go
git commit -m "feat: Daraja OAuth token fetch with Redis cache"
```

---

## Task 4: STK Push

**Files:**
- Create: `mpesa/stk.go`

- [ ] **Step 1: Create stk.go**

```go
// mpesa/stk.go
package mpesa

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kakitucurrency/kakitu-wallet-server/utils"
)

// InitiateSTKPush sends an STK Push prompt to the user's phone.
// phone must be in format 2547XXXXXXXX.
// amountKES is an integer string e.g. "100".
// Returns the CheckoutRequestID on success.
func InitiateSTKPush(token, phone, amountKES, callbackURL string) (string, error) {
	shortCode := utils.GetEnv("MPESA_SHORTCODE", "174379")
	passKey := utils.GetEnv("MPESA_PASSKEY", "bfb279f9aa9bdbcf158e97dd71a467cd2e0c893059b10f78e6b72ada1ed2c919")

	timestamp := time.Now().Format("20060102150405")
	rawPassword := shortCode + passKey + timestamp
	password := base64.StdEncoding.EncodeToString([]byte(rawPassword))

	payload := STKPushRequest{
		BusinessShortCode: shortCode,
		Password:          password,
		Timestamp:         timestamp,
		TransactionType:   "CustomerPayBillOnline",
		Amount:            amountKES,
		PartyA:            phone,
		PartyB:            shortCode,
		PhoneNumber:       phone,
		CallBackURL:       callbackURL + "/mpesa/cashin/callback",
		AccountReference:  "Kakitu",
		TransactionDesc:   "Cash In",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshalling STK request: %w", err)
	}

	url := fmt.Sprintf("%s/mpesa/stkpush/v1/processrequest", BaseURL())
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(body))
	if err != nil {
		return "", fmt.Errorf("building STK request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling STK Push API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading STK response: %w", err)
	}

	var stkResp STKPushResponse
	if err := json.Unmarshal(respBody, &stkResp); err != nil {
		return "", fmt.Errorf("parsing STK response: %w", err)
	}
	if stkResp.ResponseCode != "0" {
		return "", fmt.Errorf("STK Push failed: %s", stkResp.ResponseDescription)
	}

	return stkResp.CheckoutRequestID, nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server
go build ./...
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add mpesa/stk.go
git commit -m "feat: Daraja STK Push initiation"
```

---

## Task 5: B2C Transfer

**Files:**
- Create: `mpesa/b2c.go`

- [ ] **Step 1: Create b2c.go**

```go
// mpesa/b2c.go
package mpesa

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/kakitucurrency/kakitu-wallet-server/utils"
)

// InitiateB2C sends KES from the Kakitu shortcode to a user's phone.
// phone must be in format 2547XXXXXXXX.
// amountKES is an integer string e.g. "100".
// Returns the ConversationID on success.
func InitiateB2C(token, phone, amountKES, callbackURL string) (string, error) {
	shortCode := utils.GetEnv("MPESA_SHORTCODE", "174379")
	initiatorName := utils.GetEnv("MPESA_B2C_INITIATOR", "testapi")
	securityCred := utils.GetEnv("MPESA_B2C_SECURITY_CRED", "")
	if securityCred == "" {
		return "", fmt.Errorf("MPESA_B2C_SECURITY_CRED must be set")
	}

	payload := B2CRequest{
		InitiatorName:      initiatorName,
		SecurityCredential: securityCred,
		CommandID:          "BusinessPayment",
		Amount:             amountKES,
		PartyA:             shortCode,
		PartyB:             phone,
		Remarks:            "Kakitu Cash Out",
		QueueTimeOutURL:    callbackURL + "/mpesa/cashout/callback",
		ResultURL:          callbackURL + "/mpesa/cashout/callback",
		Occasion:           "CashOut",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshalling B2C request: %w", err)
	}

	url := fmt.Sprintf("%s/mpesa/b2c/v3/paymentrequest", BaseURL())
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(body))
	if err != nil {
		return "", fmt.Errorf("building B2C request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling B2C API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading B2C response: %w", err)
	}

	var b2cResp B2CResponse
	if err := json.Unmarshal(respBody, &b2cResp); err != nil {
		return "", fmt.Errorf("parsing B2C response: %w", err)
	}
	if b2cResp.ResponseCode != "0" {
		return "", fmt.Errorf("B2C failed: %s", b2cResp.ResponseDescription)
	}

	return b2cResp.ConversationID, nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server
go build ./...
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add mpesa/b2c.go
git commit -m "feat: Daraja B2C transfer initiation"
```

---

## Task 6: Treasury Wallet — Sign and Send KSHS

**Files:**
- Create: `utils/treasury.go`

This task implements the full flow to send KSHS from the treasury wallet:
1. Derive keypair from `TREASURY_SEED` env var
2. Build a state block (send)
3. Hash and sign it
4. Generate work on the frontier
5. Broadcast via RPCClient

- [ ] **Step 1: Create utils/treasury.go**

```go
// utils/treasury.go
package utils

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"github.com/kakitucurrency/kakitu-wallet-server/models"
	"github.com/kakitucurrency/kakitu-wallet-server/net"
	"github.com/kakitucurrency/kakitu-wallet-server/utils/ed25519"
	"golang.org/x/crypto/blake2b"
)

// kshsRawPerUnit is 10^30 (1 KSHS = 10^30 raw)
var kshsRawPerUnit, _ = new(big.Int).SetString("1000000000000000000000000000000", 10)

// TreasuryKeyPair derives the ed25519 keypair from TREASURY_SEED env var at index 0.
func TreasuryKeyPair() (privateKey ed25519.PrivateKey, publicKey ed25519.PublicKey, err error) {
	seedHex := GetEnv("TREASURY_SEED", "")
	if seedHex == "" {
		return nil, nil, errors.New("TREASURY_SEED env var not set")
	}
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		return nil, nil, fmt.Errorf("decoding TREASURY_SEED: %w", err)
	}
	if len(seed) != 32 {
		return nil, nil, errors.New("TREASURY_SEED must be 64 hex chars (32 bytes)")
	}

	// Nano key derivation: blake2b(seed || uint32_big_endian(index=0))
	h, err := blake2b.New(32, nil)
	if err != nil {
		return nil, nil, err
	}
	indexBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(indexBytes, 0)
	h.Write(seed)
	h.Write(indexBytes)
	privKeyBytes := h.Sum(nil)

	// Build full 64-byte ed25519 private key (privKeyBytes[:32] + pubKey)
	// The ed25519 Sign function uses privateKey[32:] as the cached public key.
	// We must replicate what GenerateKey does: blake2b-512 the seed half, clamp, scalar mult.
	digest := blake2b.Sum512(privKeyBytes)
	digest[0] &= 248
	digest[31] &= 127
	digest[31] |= 64

	// We need edwards25519 scalar mult — use ed25519.GenerateKey trick:
	// Build a fake 64-byte private key with privKeyBytes as first half, then derive pub key
	// by using standard ed25519 Sign path. Instead, use PublicKey derivation directly.
	// Since our ed25519 package's PrivateKey.Public() reads priv[32:], we need to compute pub.
	// Use Sign to get public key: Sign internally uses digest[0:32] for scalar and priv[32:] for pub.
	// Simplest: use GenerateKey with a reader that returns our privKeyBytes.
	reader := &fixedReader{data: privKeyBytes}
	pubKey, privKey, err := ed25519.GenerateKey(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating treasury keypair: %w", err)
	}

	return privKey, pubKey, nil
}

// fixedReader is an io.Reader that always returns the same bytes.
type fixedReader struct {
	data []byte
	pos  int
}

func (r *fixedReader) Read(b []byte) (n int, err error) {
	n = copy(b, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// PubKeyToAddress converts a 32-byte ed25519 public key to a kshs_ address.
func PubKeyToAddress(pubKey []byte) (string, error) {
	if len(pubKey) != 32 {
		return "", errors.New("public key must be 32 bytes")
	}
	// Prepend 3 zero bytes → 35 bytes → base32 encode → 56 chars
	padded := make([]byte, 35)
	copy(padded[3:], pubKey)
	encoded := NanoEncoding.EncodeToString(padded)
	// Take chars [4:56] as the key portion (skip padding "1111")
	keyStr := encoded[4:]
	// Compute checksum: blake2b-5(pubKey), reversed → base32
	checksum := GetAddressChecksum(pubKey)
	checksumStr := NanoEncoding.EncodeToString(checksum)
	return "kshs_" + keyStr + checksumStr, nil
}

// KesToRaw converts a KES decimal amount to raw KSHS (1 KES = 1 KSHS = 10^30 raw).
func KesToRaw(amountKES string) (*big.Int, error) {
	// amountKES is a string like "100" or "50.5"
	f, ok := new(big.Float).SetString(amountKES)
	if !ok {
		return nil, fmt.Errorf("invalid amount: %s", amountKES)
	}
	rawFloat := new(big.Float).Mul(f, new(big.Float).SetInt(kshsRawPerUnit))
	rawInt, _ := rawFloat.Int(nil)
	return rawInt, nil
}

// hashStateBlock computes the blake2b-256 hash of a Nano state block.
func hashStateBlock(accountPub, previous, representativePub, balanceBytes, linkPub []byte) ([]byte, error) {
	// Preamble: uint256(6) = 32 bytes with 0x06 at position 31
	preamble := make([]byte, 32)
	preamble[31] = 6

	h, err := blake2b.New(32, nil)
	if err != nil {
		return nil, err
	}
	h.Write(preamble)
	h.Write(accountPub)
	h.Write(previous)
	h.Write(representativePub)
	h.Write(balanceBytes)
	h.Write(linkPub)
	return h.Sum(nil), nil
}

// SendKSHS sends amountRaw KSHS from the treasury wallet to destAddress.
// It fetches account info, builds and signs the block, generates work, then broadcasts.
func SendKSHS(rpcClient *net.RPCClient, destAddress string, amountRaw *big.Int) error {
	privKey, pubKey, err := TreasuryKeyPair()
	if err != nil {
		return fmt.Errorf("loading treasury keypair: %w", err)
	}

	treasuryAddress, err := PubKeyToAddress(pubKey)
	if err != nil {
		return fmt.Errorf("deriving treasury address: %w", err)
	}

	// Get account info: frontier, balance, representative
	accountInfo, err := rpcClient.MakeAccountInfoRequest(treasuryAddress)
	if err != nil {
		return fmt.Errorf("fetching treasury account info: %w", err)
	}
	if _, hasErr := accountInfo["error"]; hasErr {
		return fmt.Errorf("treasury account not opened: %v", accountInfo["error"])
	}

	frontier := fmt.Sprintf("%v", accountInfo["frontier"])
	representative := fmt.Sprintf("%v", accountInfo["representative"])
	balanceRaw := fmt.Sprintf("%v", accountInfo["balance"])

	currentBalance, ok := new(big.Int).SetString(balanceRaw, 10)
	if !ok {
		return fmt.Errorf("parsing treasury balance: %s", balanceRaw)
	}
	if currentBalance.Cmp(amountRaw) < 0 {
		return fmt.Errorf("insufficient treasury balance: have %s, need %s", currentBalance.String(), amountRaw.String())
	}
	newBalance := new(big.Int).Sub(currentBalance, amountRaw)

	// Balance as 16-byte big-endian uint128
	balanceBytes := make([]byte, 16)
	newBalance.FillBytes(balanceBytes)

	// Destination public key (link field)
	destPubKey, err := AddressToPub(destAddress)
	if err != nil {
		return fmt.Errorf("invalid destination address: %w", err)
	}

	// Treasury public key
	treasuryPubKey, err := AddressToPub(treasuryAddress)
	if err != nil {
		return fmt.Errorf("deriving treasury pub key: %w", err)
	}

	// Representative public key
	repPubKey, err := AddressToPub(representative)
	if err != nil {
		return fmt.Errorf("parsing representative: %w", err)
	}

	// Previous as bytes
	prevBytes, err := hex.DecodeString(frontier)
	if err != nil {
		return fmt.Errorf("decoding frontier: %w", err)
	}

	// Hash the block
	blockHash, err := hashStateBlock(treasuryPubKey, prevBytes, repPubKey, balanceBytes, destPubKey)
	if err != nil {
		return fmt.Errorf("hashing block: %w", err)
	}

	// Sign
	sig := ed25519.Sign(privKey, blockHash)
	sigHex := hex.EncodeToString(sig)

	// Generate work on frontier
	work, err := rpcClient.WorkGenerate(frontier, 64)
	if err != nil {
		return fmt.Errorf("generating work: %w", err)
	}

	// Build the process request
	newBalanceStr := newBalance.String()
	linkHex := hex.EncodeToString(destPubKey)
	subtype := "send"
	doWork := false
	processReq := models.ProcessRequestJsonBlock{
		Action:  "process",
		SubType: &subtype,
		Block: &models.ProcessJsonBlock{
			Type:           "state",
			Account:        treasuryAddress,
			Previous:       frontier,
			Representative: representative,
			Balance:        newBalanceStr,
			Link:           linkHex,
			Signature:      sigHex,
			Work:           &work,
		},
	}
	// Add json_block: true wrapper
	finalReq := map[string]interface{}{
		"action":     "process",
		"json_block": true,
		"subtype":    subtype,
		"block":      processReq.Block,
	}

	rawResp, err := rpcClient.MakeRequest(finalReq)
	if err != nil {
		return fmt.Errorf("broadcasting treasury block: %w", err)
	}

	// Check for node error
	var respMap map[string]interface{}
	if err := json.Unmarshal(rawResp, &respMap); err != nil {
		return fmt.Errorf("parsing process response: %w", err)
	}
	if errMsg, hasErr := respMap["error"]; hasErr {
		return fmt.Errorf("node rejected treasury block: %v", errMsg)
	}

	return nil
}
```

Note: add `"encoding/json"` to the imports above — it was omitted for brevity but is required for `json.Unmarshal`.

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server
go build ./...
```
Expected: no errors. Fix any import issues (ensure `encoding/json` is in the imports list in treasury.go).

- [ ] **Step 3: Commit**

```bash
git add utils/treasury.go
git commit -m "feat: treasury wallet keypair derivation and KSHS send block signing"
```

---

## Task 7: M-Pesa Transaction Repository

**Files:**
- Create: `repository/mpesa_txn_repo.go`

- [ ] **Step 1: Create the repository**

```go
// repository/mpesa_txn_repo.go
package repository

import (
	"github.com/kakitucurrency/kakitu-wallet-server/models/dbmodels"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type MpesaTxnRepo struct {
	DB *gorm.DB
}

// CreatePendingCashIn saves a new pending cash-in transaction and returns it.
func (r *MpesaTxnRepo) CreatePendingCashIn(amountKES decimal.Decimal, kshsAddress, checkoutRequestID string) (*dbmodels.MpesaTransaction, error) {
	txn := &dbmodels.MpesaTransaction{
		Type:          "cashin",
		AmountKes:     amountKES,
		KshsAddress:   kshsAddress,
		MerchantReqID: checkoutRequestID,
		Status:        "pending",
	}
	if err := r.DB.Create(txn).Error; err != nil {
		return nil, err
	}
	return txn, nil
}

// CreatePendingCashOut saves a new pending cash-out transaction and returns it.
func (r *MpesaTxnRepo) CreatePendingCashOut(amountKES decimal.Decimal, kshsAddress, txHash, conversationID string) (*dbmodels.MpesaTransaction, error) {
	txn := &dbmodels.MpesaTransaction{
		Type:          "cashout",
		AmountKes:     amountKES,
		KshsAddress:   kshsAddress,
		TxHash:        txHash,
		MerchantReqID: conversationID,
		Status:        "pending",
	}
	if err := r.DB.Create(txn).Error; err != nil {
		return nil, err
	}
	return txn, nil
}

// FindByMerchantReqID returns a transaction by its Daraja CheckoutRequestID or ConversationID.
func (r *MpesaTxnRepo) FindByMerchantReqID(id string) (*dbmodels.MpesaTransaction, error) {
	var txn dbmodels.MpesaTransaction
	if err := r.DB.Where("merchant_req_id = ?", id).First(&txn).Error; err != nil {
		return nil, err
	}
	return &txn, nil
}

// TxHashExists returns true if the tx_hash has already been used for a cashout.
func (r *MpesaTxnRepo) TxHashExists(txHash string) (bool, error) {
	var count int64
	err := r.DB.Model(&dbmodels.MpesaTransaction{}).
		Where("tx_hash = ? AND type = 'cashout'", txHash).
		Count(&count).Error
	return count > 0, err
}

// UpdateStatus sets the status (and optionally mpesa_receipt) on a transaction.
func (r *MpesaTxnRepo) UpdateStatus(id string, status, receipt string) error {
	updates := map[string]interface{}{"status": status}
	if receipt != "" {
		updates["mpesa_receipt"] = receipt
	}
	return r.DB.Model(&dbmodels.MpesaTransaction{}).
		Where("merchant_req_id = ?", id).
		Updates(updates).Error
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server
go build ./...
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add repository/mpesa_txn_repo.go
git commit -m "feat: M-Pesa transaction repository"
```

---

## Task 8: HTTP Handlers

**Files:**
- Create: `controller/mpesa_c.go`

- [ ] **Step 1: Create mpesa_c.go**

```go
// controller/mpesa_c.go
package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/kakitucurrency/kakitu-wallet-server/mpesa"
	"github.com/kakitucurrency/kakitu-wallet-server/net"
	"github.com/kakitucurrency/kakitu-wallet-server/repository"
	"github.com/kakitucurrency/kakitu-wallet-server/utils"
	"github.com/go-chi/render"
	"github.com/shopspring/decimal"
	"k8s.io/klog/v2"
)

type MpesaController struct {
	RPCClient    *net.RPCClient
	MpesaTxnRepo *repository.MpesaTxnRepo
}

// ── Cash In ───────────────────────────────────────────────────────────────────

type cashInRequest struct {
	Phone       string `json:"phone"`        // 2547XXXXXXXX
	AmountKES   string `json:"amount_kes"`   // e.g. "100"
	KshsAddress string `json:"kshs_address"`
}

// HandleCashIn initiates an STK Push for the user.
func (mc *MpesaController) HandleCashIn(w http.ResponseWriter, r *http.Request) {
	var req cashInRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrBadrequest(w, r, "invalid request body")
		return
	}

	if !utils.ValidateAddress(req.KshsAddress, false) {
		ErrBadrequest(w, r, "invalid kshs_address")
		return
	}
	if req.Phone == "" || req.AmountKES == "" {
		ErrBadrequest(w, r, "phone and amount_kes are required")
		return
	}

	// Validate amount is a positive integer
	amountInt, err := strconv.ParseInt(req.AmountKES, 10, 64)
	if err != nil || amountInt <= 0 {
		ErrBadrequest(w, r, "amount_kes must be a positive integer")
		return
	}

	token, err := mpesa.GetToken()
	if err != nil {
		klog.Errorf("M-Pesa auth error: %v", err)
		ErrInternalServerError(w, r, "M-Pesa authentication failed")
		return
	}

	callbackURL := utils.GetEnv("MPESA_CALLBACK_URL", "")
	checkoutID, err := mpesa.InitiateSTKPush(token, req.Phone, req.AmountKES, callbackURL)
	if err != nil {
		klog.Errorf("STK Push error: %v", err)
		ErrInternalServerError(w, r, fmt.Sprintf("STK Push failed: %v", err))
		return
	}

	amountDec, _ := decimal.NewFromString(req.AmountKES)
	if _, err := mc.MpesaTxnRepo.CreatePendingCashIn(amountDec, req.KshsAddress, checkoutID); err != nil {
		klog.Errorf("Saving cash-in txn error: %v", err)
		ErrInternalServerError(w, r, "Failed to save transaction")
		return
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]string{
		"status":              "pending",
		"checkout_request_id": checkoutID,
	})
}

// HandleCashInCallback receives Safaricom's STK Push result and sends KSHS on success.
func (mc *MpesaController) HandleCashInCallback(w http.ResponseWriter, r *http.Request) {
	// Always return 200 to Safaricom regardless of internal errors
	w.WriteHeader(http.StatusOK)

	var body mpesa.STKCallbackBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		klog.Errorf("Parsing STK callback: %v", err)
		return
	}

	cb := body.Body.StkCallback
	if cb.ResultCode != 0 {
		klog.Infof("STK Push failed for %s: %s", cb.CheckoutRequestID, cb.ResultDesc)
		_ = mc.MpesaTxnRepo.UpdateStatus(cb.CheckoutRequestID, "failed", "")
		return
	}

	// Extract M-Pesa receipt from CallbackMetadata
	receipt := ""
	if cb.CallbackMetadata != nil {
		for _, item := range cb.CallbackMetadata.Item {
			if item.Name == "MpesaReceiptNumber" {
				receipt = fmt.Sprintf("%v", item.Value)
			}
		}
	}

	// Look up the pending transaction to get kshs_address and amount
	txn, err := mc.MpesaTxnRepo.FindByMerchantReqID(cb.CheckoutRequestID)
	if err != nil {
		klog.Errorf("Finding cash-in txn %s: %v", cb.CheckoutRequestID, err)
		return
	}

	// Convert KES amount to raw KSHS
	amountRaw, err := utils.KesToRaw(txn.AmountKes.String())
	if err != nil {
		klog.Errorf("Converting amount to raw: %v", err)
		_ = mc.MpesaTxnRepo.UpdateStatus(cb.CheckoutRequestID, "failed", "")
		return
	}

	// Send KSHS from treasury to user
	if err := utils.SendKSHS(mc.RPCClient, txn.KshsAddress, amountRaw); err != nil {
		klog.Errorf("Sending KSHS to %s: %v", txn.KshsAddress, err)
		_ = mc.MpesaTxnRepo.UpdateStatus(cb.CheckoutRequestID, "failed", receipt)
		return
	}

	_ = mc.MpesaTxnRepo.UpdateStatus(cb.CheckoutRequestID, "confirmed", receipt)
	klog.Infof("Cash-in confirmed: %s KSHS → %s (receipt: %s)", txn.AmountKes.String(), txn.KshsAddress, receipt)
}

// ── Cash Out ──────────────────────────────────────────────────────────────────

type cashOutRequest struct {
	Phone     string `json:"phone"`      // 2547XXXXXXXX
	AmountKES string `json:"amount_kes"` // e.g. "100"
	TxHash    string `json:"tx_hash"`    // on-chain block hash
}

// HandleCashOut verifies the on-chain KSHS send and initiates a B2C transfer.
func (mc *MpesaController) HandleCashOut(w http.ResponseWriter, r *http.Request) {
	var req cashOutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrBadrequest(w, r, "invalid request body")
		return
	}
	if req.Phone == "" || req.AmountKES == "" || req.TxHash == "" {
		ErrBadrequest(w, r, "phone, amount_kes, and tx_hash are required")
		return
	}
	amountInt, err := strconv.ParseInt(req.AmountKES, 10, 64)
	if err != nil || amountInt <= 0 {
		ErrBadrequest(w, r, "amount_kes must be a positive integer")
		return
	}

	// Double-spend guard
	exists, err := mc.MpesaTxnRepo.TxHashExists(req.TxHash)
	if err != nil {
		ErrInternalServerError(w, r, "database error")
		return
	}
	if exists {
		render.Status(r, http.StatusConflict)
		render.JSON(w, r, map[string]string{"error": "tx_hash already used"})
		return
	}

	// Verify on-chain block
	block, err := mc.RPCClient.MakeBlockRequest(req.TxHash)
	if err != nil {
		ErrBadrequest(w, r, "block not found on chain")
		return
	}
	if block.Subtype != "send" {
		ErrBadrequest(w, r, "block is not a send")
		return
	}

	// Verify destination is treasury
	treasuryAddress := utils.GetEnv("TREASURY_ADDRESS", "")
	if block.Contents.LinkAsAccount != treasuryAddress {
		ErrBadrequest(w, r, "block destination is not the treasury")
		return
	}

	// Verify amount matches (block.Amount is in raw)
	expectedRaw, err := utils.KesToRaw(req.AmountKES)
	if err != nil {
		ErrBadrequest(w, r, "invalid amount")
		return
	}
	if block.Amount != expectedRaw.String() {
		ErrBadrequest(w, r, "block amount does not match requested amount")
		return
	}

	// Initiate B2C
	token, err := mpesa.GetToken()
	if err != nil {
		klog.Errorf("M-Pesa auth error: %v", err)
		ErrInternalServerError(w, r, "M-Pesa authentication failed")
		return
	}
	callbackURL := utils.GetEnv("MPESA_CALLBACK_URL", "")
	conversationID, err := mpesa.InitiateB2C(token, req.Phone, req.AmountKES, callbackURL)
	if err != nil {
		klog.Errorf("B2C error: %v", err)
		ErrInternalServerError(w, r, fmt.Sprintf("B2C failed: %v", err))
		return
	}

	amountDec, _ := decimal.NewFromString(req.AmountKES)
	if _, err := mc.MpesaTxnRepo.CreatePendingCashOut(amountDec, block.BlockAccount, req.TxHash, conversationID); err != nil {
		klog.Errorf("Saving cash-out txn: %v", err)
		ErrInternalServerError(w, r, "Failed to save transaction")
		return
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]string{"status": "pending"})
}

// HandleCashOutCallback receives Safaricom's B2C result.
func (mc *MpesaController) HandleCashOutCallback(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)

	var body mpesa.B2CResultBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		klog.Errorf("Parsing B2C callback: %v", err)
		return
	}

	result := body.Result
	status := "confirmed"
	if result.ResultCode != 0 {
		status = "failed"
	}

	receipt := result.TransactionID
	if err := mc.MpesaTxnRepo.UpdateStatus(result.ConversationID, status, receipt); err != nil {
		klog.Errorf("Updating cash-out txn %s: %v", result.ConversationID, err)
	}
	klog.Infof("Cash-out %s: conversationID=%s receipt=%s", status, result.ConversationID, receipt)
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server
go build ./...
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add controller/mpesa_c.go
git commit -m "feat: M-Pesa HTTP handlers for cash-in and cash-out"
```

---

## Task 9: Wire Up Routes in main.go

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Add MpesaTxnRepo creation after fcmRepo**

In `main.go`, after:
```go
fcmRepo := &repository.FcmTokenRepo{
    DB: db,
}
```

Add:
```go
mpesaTxnRepo := &repository.MpesaTxnRepo{
    DB: db,
}
```

- [ ] **Step 2: Create MpesaController**

After:
```go
hc := controller.HttpController{RPCClient: &rpcClient, BananoMode: false, FcmTokenRepo: fcmRepo, FcmClient: fcmClient}
```

Add:
```go
mc := controller.MpesaController{RPCClient: &rpcClient, MpesaTxnRepo: mpesaTxnRepo}
```

- [ ] **Step 3: Mount M-Pesa routes**

After:
```go
app.Post("/callback", hc.HandleHTTPCallback)
```

Add:
```go
// M-Pesa Daraja routes
app.Route("/mpesa", func(r chi.Router) {
    r.Post("/cashin", mc.HandleCashIn)
    r.Post("/cashin/callback", mc.HandleCashInCallback)
    r.Post("/cashout", mc.HandleCashOut)
    r.Post("/cashout/callback", mc.HandleCashOutCallback)
})
```

- [ ] **Step 4: Verify it compiles**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server
go build ./...
```
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat: mount M-Pesa Daraja routes on /mpesa"
```

---

## Task 10: Smoke Test with Daraja Sandbox

- [ ] **Step 1: Set up .env for sandbox**

Create a `.env` file (do NOT commit this):
```bash
MPESA_CONSUMER_KEY=your_sandbox_consumer_key
MPESA_CONSUMER_SECRET=your_sandbox_consumer_secret
MPESA_SHORTCODE=174379
MPESA_PASSKEY=bfb279f9aa9bdbcf158e97dd71a467cd2e0c893059b10f78e6b72ada1ed2c919
MPESA_B2C_INITIATOR=testapi
MPESA_B2C_SECURITY_CRED=your_sandbox_security_credential
MPESA_CALLBACK_URL=https://your-ngrok-or-server-url
MPESA_ENVIRONMENT=sandbox
TREASURY_SEED=your_64_hex_char_seed
TREASURY_ADDRESS=kshs_your_treasury_address
```

Get sandbox credentials from: https://developer.safaricom.co.ke

- [ ] **Step 2: Test auth endpoint manually**

```bash
curl -u "$MPESA_CONSUMER_KEY:$MPESA_CONSUMER_SECRET" \
  "https://sandbox.safaricom.co.ke/oauth/v1/generate?grant_type=client_credentials"
```
Expected: `{"access_token":"...","expires_in":"3599"}`

- [ ] **Step 3: Start the server and test cash-in**

```bash
go run main.go
```

In a second terminal:
```bash
curl -X POST http://localhost:3000/mpesa/cashin \
  -H "Content-Type: application/json" \
  -d '{"phone":"254708374149","amount_kes":"1","kshs_address":"kshs_your_test_address"}'
```
Expected: `{"status":"pending","checkout_request_id":"ws_CO_..."}`

- [ ] **Step 4: Test cash-out validation (should reject unknown tx_hash)**

```bash
curl -X POST http://localhost:3000/mpesa/cashout \
  -H "Content-Type: application/json" \
  -d '{"phone":"254708374149","amount_kes":"1","tx_hash":"0000000000000000000000000000000000000000000000000000000000000000"}'
```
Expected: `{"error":"block not found on chain"}` (400)

- [ ] **Step 5: Commit env template (not the .env itself)**

```bash
cat > .env.example << 'EOF'
MPESA_CONSUMER_KEY=
MPESA_CONSUMER_SECRET=
MPESA_SHORTCODE=174379
MPESA_PASSKEY=
MPESA_B2C_INITIATOR=testapi
MPESA_B2C_SECURITY_CRED=
MPESA_CALLBACK_URL=https://walletapi.kakitu.africa
MPESA_ENVIRONMENT=sandbox
TREASURY_SEED=
TREASURY_ADDRESS=
EOF
git add .env.example
git commit -m "chore: add .env.example with M-Pesa Daraja config template"
```
