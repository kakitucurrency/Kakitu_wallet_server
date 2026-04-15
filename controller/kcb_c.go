// controller/kcb_c.go
package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/render"
	"github.com/google/uuid"
	"github.com/kakitucurrency/kakitu-wallet-server/ethereum"
	"github.com/kakitucurrency/kakitu-wallet-server/kcb"
	"github.com/kakitucurrency/kakitu-wallet-server/repository"
	"github.com/kakitucurrency/kakitu-wallet-server/utils"
	"github.com/shopspring/decimal"
	"k8s.io/klog/v2"
)

// KCBController handles KCB Buni cash-in and cash-out HTTP endpoints.
type KCBController struct {
	EthClient *ethereum.Client
	TxnRepo   *repository.KCBTxnRepo
}

// normalizeKCBPhone converts Kenyan phone numbers to 2547XXXXXXXX required by KCB Buni.
// Accepts 07XXXXXXXX, +2547XXXXXXXX, 2547XXXXXXXX.
func normalizeKCBPhone(phone string) (string, error) {
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

// isValidKCBTxHash returns true if hash is a 0x-prefixed 32-byte hex string.
func isValidKCBTxHash(hash string) bool {
	if len(hash) != 66 || !strings.HasPrefix(hash, "0x") {
		return false
	}
	for _, c := range hash[2:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// validateKCBCallbackAuth checks that callbacks carry a valid CALLBACK_SECRET.
// When CALLBACK_SECRET is empty all callbacks are allowed (dev/sandbox).
func validateKCBCallbackAuth(r *http.Request) bool {
	secret := utils.GetEnv("CALLBACK_SECRET", "")
	if secret == "" {
		return true
	}
	if r.Header.Get("Authorization") == secret {
		return true
	}
	if r.URL.Query().Get("secret") == secret {
		return true
	}
	return false
}

// ── Cash-In ──────────────────────────────────────────────────────────────────

type kcbCashInRequest struct {
	Phone       string `json:"phone"`
	AmountKES   string `json:"amount_kes"`
	KshsAddress string `json:"kshs_address"`
}

// HandleCashIn initiates an STK Push to collect KES from the user's M-Pesa.
// POST /kcb/cashin
func (kc *KCBController) HandleCashIn(w http.ResponseWriter, r *http.Request) {
	var req kcbCashInRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrBadrequest(w, r, "invalid_request_body")
		return
	}
	if req.Phone == "" {
		ErrBadrequest(w, r, "phone_required")
		return
	}
	if req.AmountKES == "" {
		ErrBadrequest(w, r, "amount_kes_required")
		return
	}
	if !common.IsHexAddress(req.KshsAddress) {
		ErrBadrequest(w, r, "invalid_kshs_address")
		return
	}

	phone, err := normalizeKCBPhone(req.Phone)
	if err != nil {
		ErrBadrequest(w, r, fmt.Sprintf("invalid_phone: %s", err))
		return
	}

	amount, err := strconv.ParseInt(req.AmountKES, 10, 64)
	if err != nil || amount <= 0 {
		ErrBadrequest(w, r, "amount_kes_must_be_positive_integer")
		return
	}

	token, err := kcb.GetToken()
	if err != nil {
		klog.Errorf("HandleCashIn: GetToken error: %v", err)
		ErrInternalServerError(w, r, "token_error")
		return
	}

	// ref8 is used to build invoiceNumber = "{debitAccount}-{ref8}"
	ref8 := strings.ReplaceAll(uuid.New().String(), "-", "")[:8]

	result, err := kcb.STKPush(token, phone, req.AmountKES, ref8)
	if err != nil {
		klog.Errorf("HandleCashIn: STKPush error: %v", err)
		ErrInternalServerError(w, r, "stk_push_failed")
		return
	}

	amountDec := decimal.NewFromInt(amount)
	if _, err := kc.TxnRepo.CreatePendingCashIn(amountDec, phone, req.KshsAddress, result.CheckoutRequestID); err != nil {
		klog.Errorf("HandleCashIn: CreatePendingCashIn error: %v", err)
		ErrInternalServerError(w, r, "database_error")
		return
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]string{
		"status":              "pending",
		"checkout_request_id": result.CheckoutRequestID,
		"message":             "Enter your M-Pesa PIN to complete payment",
	})
}

// HandleCashInCallback processes the STK Push result posted by KCB.
// POST /kcb/cashin/callback
func (kc *KCBController) HandleCashInCallback(w http.ResponseWriter, r *http.Request) {
	if !validateKCBCallbackAuth(r) {
		klog.Warning("Rejected KCB cash-in callback: invalid or missing secret")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// KCB requires a 200 response immediately.
	w.WriteHeader(http.StatusOK)

	var body kcb.STKCallbackBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return
	}

	cb := body.Body.StkCallback
	checkoutRequestID := cb.CheckoutRequestID

	if cb.ResultCode != 0 {
		klog.Infof("STK failed for %s: ResultCode=%d %s", checkoutRequestID, cb.ResultCode, cb.ResultDesc)
		txn, _ := kc.TxnRepo.ClaimPendingCashIn(checkoutRequestID)
		if txn != nil {
			_ = kc.TxnRepo.UpdateStatus(txn.ID, "failed", "")
		}
		return
	}

	// Extract MpesaReceiptNumber from CallbackMetadata.Item[].
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
		klog.Errorf("HandleCashInCallback: missing MpesaReceiptNumber for %s", checkoutRequestID)
		txn, _ := kc.TxnRepo.ClaimPendingCashIn(checkoutRequestID)
		if txn != nil {
			_ = kc.TxnRepo.UpdateStatus(txn.ID, "failed", "")
		}
		return
	}

	// Atomically claim the pending transaction — prevents double-mint on duplicate callbacks.
	txn, err := kc.TxnRepo.ClaimPendingCashIn(checkoutRequestID)
	if err != nil {
		klog.Errorf("HandleCashInCallback: ClaimPendingCashIn error for %s: %v", checkoutRequestID, err)
		return
	}
	if txn == nil {
		klog.Warningf("HandleCashInCallback: duplicate or unknown callback for %s, skipping", checkoutRequestID)
		return
	}

	amountKES, _ := txn.AmountKES.Float64()
	if _, err := kc.EthClient.MintKSHS(txn.KshsAddress, receipt, int64(amountKES)); err != nil {
		klog.Errorf("MINT_FAILED — user %s owed %s KSHS, receipt: %s | error: %v",
			txn.KshsAddress, txn.AmountKES.String(), receipt, err)
		_ = kc.TxnRepo.UpdateStatus(txn.ID, "mint_failed", receipt)
		return
	}

	_ = kc.TxnRepo.UpdateStatus(txn.ID, "completed", receipt)
}

// ── Cash-Out ─────────────────────────────────────────────────────────────────

type kcbCashOutRequest struct {
	Phone       string `json:"phone"`
	AmountKES   string `json:"amount_kes"`
	TxHash      string `json:"tx_hash"`
	KshsAddress string `json:"kshs_address"`
}

// HandleCashOut verifies the on-chain KSHS burn and initiates a KCB Funds Transfer to M-Pesa.
// POST /kcb/cashout
func (kc *KCBController) HandleCashOut(w http.ResponseWriter, r *http.Request) {
	var req kcbCashOutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrBadrequest(w, r, "invalid_request_body")
		return
	}
	if req.Phone == "" {
		ErrBadrequest(w, r, "phone_required")
		return
	}
	if req.AmountKES == "" {
		ErrBadrequest(w, r, "amount_kes_required")
		return
	}
	if !isValidKCBTxHash(req.TxHash) {
		ErrBadrequest(w, r, "invalid_tx_hash")
		return
	}
	if !common.IsHexAddress(req.KshsAddress) {
		ErrBadrequest(w, r, "invalid_kshs_address")
		return
	}

	phone, err := normalizeKCBPhone(req.Phone)
	if err != nil {
		ErrBadrequest(w, r, fmt.Sprintf("invalid_phone: %s", err))
		return
	}

	amount, err := strconv.ParseInt(req.AmountKES, 10, 64)
	if err != nil || amount <= 0 {
		ErrBadrequest(w, r, "amount_kes_must_be_positive_integer")
		return
	}

	// Verify the on-chain ERC20 burn before sending any money.
	if err := kc.EthClient.VerifyBurn(req.TxHash, req.KshsAddress, amount); err != nil {
		ErrBadrequest(w, r, fmt.Sprintf("on_chain_verification_failed: %s", err))
		return
	}

	// Fetch token before creating the DB record — a transient token failure should not
	// lock the tx_hash permanently (the user couldn't retry if the record existed).
	token, err := kcb.GetToken()
	if err != nil {
		klog.Errorf("HandleCashOut: GetToken error: %v", err)
		ErrInternalServerError(w, r, "token_error")
		return
	}

	// txRef ≤ 12 chars: "KK" + first 10 hex chars of a UUID
	txRef := "KK" + strings.ReplaceAll(uuid.New().String(), "-", "")[:10]

	amountDec := decimal.NewFromInt(amount)
	txn, err := kc.TxnRepo.CreatePendingCashOutAtomic(amountDec, phone, req.KshsAddress, req.TxHash, txRef)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateKCBTxHash) {
			render.Status(r, http.StatusConflict)
			render.JSON(w, r, map[string]string{"error": "tx_hash_already_used"})
			return
		}
		klog.Errorf("HandleCashOut: CreatePendingCashOutAtomic error: %v", err)
		ErrInternalServerError(w, r, "database_error")
		return
	}

	ftResult, err := kcb.FundsTransfer(token, txRef, phone, amountDec)
	if err != nil {
		_ = kc.TxnRepo.UpdateStatus(txn.ID, "failed", "")
		klog.Errorf("HandleCashOut: FundsTransfer error for %s: %v", txRef, err)
		ErrInternalServerError(w, r, "payout_failed")
		return
	}

	if err := kc.TxnRepo.UpdateCashOutAccepted(txn.ID, ftResult.RetrievalRefNumber); err != nil {
		klog.Errorf("CRITICAL: FT accepted but UpdateCashOutAccepted failed — MANUAL RECONCILIATION REQUIRED | txRef: %s | retrievalRef: %s | phone: %s | amount: %d KES | err: %v",
			txRef, ftResult.RetrievalRefNumber, phone, amount, err)
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]string{
		"status":                "processing",
		"transaction_reference": txRef,
	})
}

// HandleCashOutCallback processes the Funds Transfer result posted by KCB.
// POST /kcb/cashout/callback
func (kc *KCBController) HandleCashOutCallback(w http.ResponseWriter, r *http.Request) {
	if !validateKCBCallbackAuth(r) {
		klog.Warning("Rejected KCB cash-out callback: invalid or missing secret")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	w.WriteHeader(http.StatusOK)

	var body kcb.FTCallbackBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return
	}

	txn, err := kc.TxnRepo.GetByTransactionReference(body.TransactionReference)
	if err != nil {
		klog.Errorf("HandleCashOutCallback: GetByTransactionReference error for %s: %v", body.TransactionReference, err)
		return
	}
	if txn == nil {
		klog.Warningf("HandleCashOutCallback: unknown transactionReference %s", body.TransactionReference)
		return
	}

	switch body.TransactionStatus {
	case "SUCCESS":
		_ = kc.TxnRepo.UpdateStatus(txn.ID, "completed", body.FtReference)
	case "FAILED":
		klog.Errorf("KCB cashout FAILED for txRef=%s | message: %s", body.TransactionReference, body.TransactionMessage)
		_ = kc.TxnRepo.UpdateStatus(txn.ID, "failed", "")
	default:
		klog.Warningf("HandleCashOutCallback: unknown transactionStatus '%s' for %s", body.TransactionStatus, body.TransactionReference)
	}
}
