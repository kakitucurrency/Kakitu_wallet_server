package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

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

// validateCallbackAuth checks that the M-Pesa callback request carries a valid
// secret token. When the CALLBACK_SECRET env var is set, the request must include
// either a matching "Authorization" header or a "secret" query parameter.
// Returns true if the request is authorized.
func validateCallbackAuth(r *http.Request) bool {
	secret := utils.GetEnv("CALLBACK_SECRET", "")
	if secret == "" {
		// No secret configured; allow all callbacks (but log a warning once via caller).
		return true
	}
	// Check Authorization header first.
	if r.Header.Get("Authorization") == secret {
		return true
	}
	// Fall back to query parameter (useful for Safaricom callback URLs: ?secret=xyz).
	if r.URL.Query().Get("secret") == secret {
		return true
	}
	return false
}

// ── Cash-In ──────────────────────────────────────────────────────────────────

type cashInRequest struct {
	Phone       string `json:"phone"`
	AmountKES   string `json:"amount_kes"`
	KshsAddress string `json:"kshs_address"`
}

// normalizePhone converts Kenyan phone numbers to the 2547XXXXXXXX format
// required by Safaricom Daraja. Accepts 07XXXXXXXX, +2547XXXXXXXX, 2547XXXXXXXX.
func normalizePhone(phone string) (string, error) {
	digits := regexp.MustCompile(`\D`).ReplaceAllString(phone, "")
	switch {
	case strings.HasPrefix(digits, "254") && len(digits) == 12:
		return digits, nil
	case strings.HasPrefix(digits, "0") && len(digits) == 10:
		return "254" + digits[1:], nil
	case len(digits) == 9:
		return "254" + digits, nil
	default:
		return "", fmt.Errorf("unrecognised phone format: %s", phone)
	}
}

// HandleConfig returns public M-Pesa configuration (treasury address).
// GET /mpesa/config
func (mc *MpesaController) HandleConfig(w http.ResponseWriter, r *http.Request) {
	render.JSON(w, r, map[string]string{
		"treasury_address": utils.GetEnv("TREASURY_ADDRESS", ""),
	})
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

	phone, err := normalizePhone(req.Phone)
	if err != nil {
		ErrBadrequest(w, r, fmt.Sprintf("invalid phone: %s", err))
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
	checkoutID, err := mpesa.InitiateSTKPush(token, phone, req.AmountKES, callbackURL)
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
	if !validateCallbackAuth(r) {
		klog.Warning("Rejected cash-in callback: invalid or missing callback secret")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// Safaricom requires a 200 response immediately.
	w.WriteHeader(http.StatusOK)

	var body mpesa.STKCallbackBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return
	}

	cb := body.Body.StkCallback
	checkoutRequestID := cb.CheckoutRequestID

	if cb.ResultCode != 0 {
		if err := mc.MpesaTxnRepo.UpdateStatus(checkoutRequestID, "failed", ""); err != nil {
			klog.Errorf("ERROR: failed to update status for %s: %v", checkoutRequestID, err)
		}
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
		if err := mc.MpesaTxnRepo.UpdateStatus(cb.CheckoutRequestID, "failed", ""); err != nil {
			klog.Errorf("ERROR: failed to update status for %s: %v", cb.CheckoutRequestID, err)
		}
		return
	}

	// Atomically claim the pending transaction to prevent double-mint if
	// Safaricom delivers the callback more than once.
	txn, err := mc.MpesaTxnRepo.ClaimPendingTransaction(checkoutRequestID)
	if err != nil {
		klog.Errorf("ClaimPendingTransaction failed for %s: %v", checkoutRequestID, err)
		return
	}
	if txn == nil {
		// No pending row — already processed or does not exist; nothing to do.
		klog.Warningf("Duplicate or unknown cash-in callback for %s, skipping", checkoutRequestID)
		return
	}

	amountRaw, err := utils.KesToRaw(txn.AmountKes.String())
	if err != nil {
		if err := mc.MpesaTxnRepo.UpdateStatus(checkoutRequestID, "failed", ""); err != nil {
			klog.Errorf("ERROR: failed to update status for %s: %v", checkoutRequestID, err)
		}
		return
	}

	if err := utils.SendKSHS(mc.RPCClient, txn.KshsAddress, amountRaw); err != nil {
		klog.Errorf("SendKSHS to %s failed (check treasury balance): %v", txn.KshsAddress, err)
		if err := mc.MpesaTxnRepo.UpdateStatus(cb.CheckoutRequestID, "failed", receipt); err != nil {
			klog.Errorf("ERROR: failed to update status for %s: %v", cb.CheckoutRequestID, err)
		}
		return
	}

	if err := mc.MpesaTxnRepo.UpdateStatus(checkoutRequestID, "confirmed", receipt); err != nil {
		klog.Errorf("ERROR: failed to update status for %s: %v", checkoutRequestID, err)
	}
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

	phone, err := normalizePhone(req.Phone)
	if err != nil {
		ErrBadrequest(w, r, fmt.Sprintf("invalid phone: %s", err))
		return
	}

	amount, err := strconv.ParseInt(req.AmountKES, 10, 64)
	if err != nil || amount <= 0 {
		ErrBadrequest(w, r, "amount_kes must be a positive integer")
		return
	}

	// Verify the on-chain block.
	block, err := mc.RPCClient.MakeBlockRequest(req.TxHash)
	if err != nil {
		ErrBadrequest(w, r, fmt.Sprintf("block lookup failed: %s", err))
		return
	}

	if block.Confirmed != "true" {
		ErrBadrequest(w, r, "block is not confirmed")
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

	// FIRST create the pending cash-out record before sending any money.
	// This way, if the DB insert fails (e.g. duplicate tx_hash), no B2C is sent.
	amountDec := decimal.NewFromInt(amount)
	txn, err := mc.MpesaTxnRepo.CreatePendingCashOutAtomic(amountDec, block.BlockAccount, req.TxHash, "")
	if err != nil {
		klog.Errorf("Saving cash-out txn: %v", err)
		if strings.Contains(err.Error(), "duplicat") || strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "unique") {
			render.Status(r, http.StatusConflict)
			render.JSON(w, r, map[string]string{"error": "tx_hash already used"})
			return
		}
		ErrInternalServerError(w, r, "Failed to save transaction")
		return
	}

	token, err := mpesa.GetToken()
	if err != nil {
		if err := mc.MpesaTxnRepo.UpdateStatus(txn.MerchantReqID, "failed", ""); err != nil {
			klog.Errorf("ERROR: failed to update status after token error: %v", err)
		}
		ErrInternalServerError(w, r, fmt.Sprintf("failed to get M-Pesa token: %s", err))
		return
	}

	callbackURL := utils.GetEnv("MPESA_CALLBACK_URL", "")
	conversationID, err := mpesa.InitiateB2C(token, phone, req.AmountKES, callbackURL)
	if err != nil {
		if err := mc.MpesaTxnRepo.UpdateStatus(txn.MerchantReqID, "failed", ""); err != nil {
			klog.Errorf("ERROR: failed to update status after B2C error: %v", err)
		}
		ErrInternalServerError(w, r, fmt.Sprintf("B2C initiation failed: %s", err))
		return
	}

	// B2C succeeded — update the record with the ConversationID for callback matching.
	if err := mc.MpesaTxnRepo.UpdateStatus(txn.MerchantReqID, "pending", conversationID); err != nil {
		klog.Errorf("ERROR: failed to update conversation ID: %v", err)
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]string{
		"status": "pending",
	})
}

// HandleCashOutCallback processes the B2C result posted by Safaricom.
// POST /mpesa/cashout/callback
func (mc *MpesaController) HandleCashOutCallback(w http.ResponseWriter, r *http.Request) {
	if !validateCallbackAuth(r) {
		klog.Warning("Rejected cash-out callback: invalid or missing callback secret")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

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

	if err := mc.MpesaTxnRepo.UpdateStatus(result.ConversationID, status, result.TransactionID); err != nil {
		klog.Errorf("ERROR: failed to update cash-out status for %s: %v", result.ConversationID, err)
	}
}
