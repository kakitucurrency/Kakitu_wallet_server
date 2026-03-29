package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/render"
	"github.com/kakitucurrency/kakitu-wallet-server/mpesa"
	"github.com/kakitucurrency/kakitu-wallet-server/net"
	"github.com/kakitucurrency/kakitu-wallet-server/repository"
	"github.com/kakitucurrency/kakitu-wallet-server/utils"
	"github.com/shopspring/decimal"
	"k8s.io/klog/v2"
)

// MpesaController handles M-Pesa cash-in and cash-out HTTP endpoints.
type MpesaController struct {
	RPCClient    *net.RPCClient
	MpesaTxnRepo *repository.MpesaTxnRepo
}

// ── Cash-In ──────────────────────────────────────────────────────────────────

type cashInRequest struct {
	Phone       string `json:"phone"`
	AmountKES   string `json:"amount_kes"`
	KshsAddress string `json:"kshs_address"`
}

// HandleCashIn initiates an STK Push to collect KES from the user's phone.
// POST /mpesa/cashin
func (mc *MpesaController) HandleCashIn(w http.ResponseWriter, r *http.Request) {
	var req cashInRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrBadrequest(w, r, "invalid request body")
		return
	}

	if req.Phone == "" {
		ErrBadrequest(w, r, "phone is required")
		return
	}
	if req.AmountKES == "" {
		ErrBadrequest(w, r, "amount_kes is required")
		return
	}
	if !utils.ValidateAddress(req.KshsAddress, false) {
		ErrBadrequest(w, r, "invalid kshs_address")
		return
	}

	amount, err := strconv.ParseInt(req.AmountKES, 10, 64)
	if err != nil || amount <= 0 {
		ErrBadrequest(w, r, "amount_kes must be a positive integer")
		return
	}

	token, err := mpesa.GetToken()
	if err != nil {
		ErrInternalServerError(w, r, fmt.Sprintf("failed to get M-Pesa token: %s", err))
		return
	}

	callbackURL := utils.GetEnv("MPESA_CALLBACK_URL", "")
	checkoutID, err := mpesa.InitiateSTKPush(token, req.Phone, req.AmountKES, callbackURL)
	if err != nil {
		ErrInternalServerError(w, r, fmt.Sprintf("STK Push failed: %s", err))
		return
	}

	amountDec := decimal.NewFromInt(amount)
	if _, err := mc.MpesaTxnRepo.CreatePendingCashIn(amountDec, req.KshsAddress, checkoutID); err != nil {
		ErrInternalServerError(w, r, fmt.Sprintf("failed to save transaction: %s", err))
		return
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]string{
		"status":               "pending",
		"checkout_request_id":  checkoutID,
	})
}

// HandleCashInCallback processes the STK Push result posted by Safaricom.
// POST /mpesa/cashin/callback
func (mc *MpesaController) HandleCashInCallback(w http.ResponseWriter, r *http.Request) {
	// Safaricom requires a 200 response immediately.
	w.WriteHeader(http.StatusOK)

	var body mpesa.STKCallbackBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return
	}

	cb := body.Body.StkCallback
	checkoutRequestID := cb.CheckoutRequestID

	if cb.ResultCode != 0 {
		_ = mc.MpesaTxnRepo.UpdateStatus(checkoutRequestID, "failed", "")
		return
	}

	// Extract MpesaReceiptNumber from callback metadata items.
	var receipt string
	if cb.CallbackMetadata != nil {
		for _, item := range cb.CallbackMetadata.Item {
			if item.Name == "MpesaReceiptNumber" {
				if v, ok := item.Value.(string); ok {
					receipt = v
				}
				break
			}
		}
	}

	if receipt == "" {
		klog.Errorf("Missing MpesaReceiptNumber in callback for %s", cb.CheckoutRequestID)
		_ = mc.MpesaTxnRepo.UpdateStatus(cb.CheckoutRequestID, "failed", "")
		return
	}

	txn, err := mc.MpesaTxnRepo.FindByMerchantReqID(checkoutRequestID)
	if err != nil {
		_ = mc.MpesaTxnRepo.UpdateStatus(checkoutRequestID, "failed", "")
		return
	}

	amountRaw, err := utils.KesToRaw(txn.AmountKes.String())
	if err != nil {
		_ = mc.MpesaTxnRepo.UpdateStatus(checkoutRequestID, "failed", "")
		return
	}

	if err := utils.SendKSHS(mc.RPCClient, txn.KshsAddress, amountRaw); err != nil {
		_ = mc.MpesaTxnRepo.UpdateStatus(checkoutRequestID, "failed", "")
		return
	}

	_ = mc.MpesaTxnRepo.UpdateStatus(checkoutRequestID, "confirmed", receipt)
}

// ── Cash-Out ─────────────────────────────────────────────────────────────────

type cashOutRequest struct {
	Phone     string `json:"phone"`
	AmountKES string `json:"amount_kes"`
	TxHash    string `json:"tx_hash"`
}

// HandleCashOut verifies a KSHS send block and initiates a B2C payment to the user.
// POST /mpesa/cashout
func (mc *MpesaController) HandleCashOut(w http.ResponseWriter, r *http.Request) {
	var req cashOutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrBadrequest(w, r, "invalid request body")
		return
	}

	if req.Phone == "" {
		ErrBadrequest(w, r, "phone is required")
		return
	}
	if req.AmountKES == "" {
		ErrBadrequest(w, r, "amount_kes is required")
		return
	}
	if req.TxHash == "" {
		ErrBadrequest(w, r, "tx_hash is required")
		return
	}

	amount, err := strconv.ParseInt(req.AmountKES, 10, 64)
	if err != nil || amount <= 0 {
		ErrBadrequest(w, r, "amount_kes must be a positive integer")
		return
	}

	// Guard against double-spend.
	exists, err := mc.MpesaTxnRepo.TxHashExists(req.TxHash)
	if err != nil {
		ErrInternalServerError(w, r, fmt.Sprintf("db error: %s", err))
		return
	}
	if exists {
		render.Status(r, http.StatusConflict)
		render.JSON(w, r, &ErrorResponse{Error: "tx_hash already used"})
		return
	}

	// Verify the on-chain block.
	block, err := mc.RPCClient.MakeBlockRequest(req.TxHash)
	if err != nil {
		ErrBadrequest(w, r, fmt.Sprintf("block lookup failed: %s", err))
		return
	}

	if block.Subtype != "send" {
		ErrBadrequest(w, r, "block subtype must be 'send'")
		return
	}

	treasuryAddress := utils.GetEnv("TREASURY_ADDRESS", "")
	if block.Contents.LinkAsAccount != treasuryAddress {
		ErrBadrequest(w, r, "block destination does not match treasury address")
		return
	}

	expectedRaw, err := utils.KesToRaw(req.AmountKES)
	if err != nil {
		ErrBadrequest(w, r, fmt.Sprintf("invalid amount_kes: %s", err))
		return
	}
	if block.Amount != expectedRaw.String() {
		ErrBadrequest(w, r, "block amount does not match requested amount")
		return
	}

	token, err := mpesa.GetToken()
	if err != nil {
		ErrInternalServerError(w, r, fmt.Sprintf("failed to get M-Pesa token: %s", err))
		return
	}

	callbackURL := utils.GetEnv("MPESA_CALLBACK_URL", "")
	conversationID, err := mpesa.InitiateB2C(token, req.Phone, req.AmountKES, callbackURL)
	if err != nil {
		ErrInternalServerError(w, r, fmt.Sprintf("B2C initiation failed: %s", err))
		return
	}

	amountDec := decimal.NewFromInt(amount)
	if _, err := mc.MpesaTxnRepo.CreatePendingCashOut(amountDec, block.BlockAccount, req.TxHash, conversationID); err != nil {
		ErrInternalServerError(w, r, fmt.Sprintf("failed to save transaction: %s", err))
		return
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]string{
		"status": "pending",
	})
}

// HandleCashOutCallback processes the B2C result posted by Safaricom.
// POST /mpesa/cashout/callback
func (mc *MpesaController) HandleCashOutCallback(w http.ResponseWriter, r *http.Request) {
	// Safaricom requires a 200 response immediately.
	w.WriteHeader(http.StatusOK)

	var body mpesa.B2CResultBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return
	}

	result := body.Result
	status := "failed"
	if result.ResultCode == 0 {
		status = "confirmed"
	}

	_ = mc.MpesaTxnRepo.UpdateStatus(result.ConversationID, status, result.TransactionID)
}
