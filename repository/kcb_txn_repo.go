// repository/kcb_txn_repo.go
package repository

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgconn"
	"github.com/kakitucurrency/kakitu-wallet-server/models/dbmodels"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// isDuplicateKeyError returns true if err is a PostgreSQL unique constraint violation (SQLSTATE 23505).
func isDuplicateKeyError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ErrDuplicateKCBTxHash is returned when a cashout tx_hash was already used.
var ErrDuplicateKCBTxHash = errors.New("duplicate tx_hash: cash-out already exists")

type KCBTxnRepo struct {
	DB *gorm.DB
}

// CreatePendingCashIn saves a new pending STK Push transaction.
func (r *KCBTxnRepo) CreatePendingCashIn(amountKES decimal.Decimal, phone, kshsAddress, checkoutRequestID string) (*dbmodels.KCBTransaction, error) {
	txn := &dbmodels.KCBTransaction{
		Type:              "cashin",
		AmountKES:         amountKES,
		Phone:             phone,
		KshsAddress:       kshsAddress,
		CheckoutRequestID: checkoutRequestID,
		Status:            "pending",
	}
	if err := r.DB.Create(txn).Error; err != nil {
		return nil, err
	}
	return txn, nil
}

// ClaimPendingCashIn atomically transitions a cashin from "pending" → "processing".
// Returns nil, nil if the row was already processed (idempotent).
func (r *KCBTxnRepo) ClaimPendingCashIn(checkoutRequestID string) (*dbmodels.KCBTransaction, error) {
	result := r.DB.
		Model(&dbmodels.KCBTransaction{}).
		Where("checkout_request_id = ? AND status = 'pending'", checkoutRequestID).
		Update("status", "processing")
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	var txn dbmodels.KCBTransaction
	if err := r.DB.Where("checkout_request_id = ?", checkoutRequestID).First(&txn).Error; err != nil {
		return nil, err
	}
	return &txn, nil
}

// CreatePendingCashOutAtomic inserts a new pending cashout transaction.
// Duplicate tx_hash is detected via the unique constraint (SQLSTATE 23505) and
// returned as ErrDuplicateKCBTxHash, avoiding the SELECT FOR UPDATE phantom-row problem.
func (r *KCBTxnRepo) CreatePendingCashOutAtomic(amountKES decimal.Decimal, phone, kshsAddress, txHash, txRef string) (*dbmodels.KCBTransaction, error) {
	txn := &dbmodels.KCBTransaction{
		Type:                 "cashout",
		AmountKES:            amountKES,
		Phone:                phone,
		KshsAddress:          kshsAddress,
		TxHash:               txHash,
		TransactionReference: txRef,
		Status:               "pending",
	}
	if err := r.DB.Create(txn).Error; err != nil {
		if isDuplicateKeyError(err) {
			return nil, ErrDuplicateKCBTxHash
		}
		return nil, err
	}
	return txn, nil
}

// UpdateCashOutAccepted transitions a cashout from "pending" → "processing" and stores KCB's ref.
func (r *KCBTxnRepo) UpdateCashOutAccepted(id uuid.UUID, retrievalRefNumber string) error {
	return r.DB.Model(&dbmodels.KCBTransaction{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":               "processing",
			"retrieval_ref_number": retrievalRefNumber,
		}).Error
}

// UpdateStatus sets status and optionally mpesa_receipt on a transaction.
// Returns an error if no row was found for the given id.
func (r *KCBTxnRepo) UpdateStatus(id uuid.UUID, status, receipt string) error {
	updates := map[string]interface{}{"status": status}
	if receipt != "" {
		updates["mpesa_receipt"] = receipt
	}
	result := r.DB.Model(&dbmodels.KCBTransaction{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("UpdateStatus: no KCBTransaction found for id %s", id)
	}
	return nil
}

// GetByTransactionReference finds a cashout by our 12-char txRef.
func (r *KCBTxnRepo) GetByTransactionReference(txRef string) (*dbmodels.KCBTransaction, error) {
	var txn dbmodels.KCBTransaction
	if err := r.DB.Where("transaction_reference = ?", txRef).First(&txn).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &txn, nil
}

// GetStaleCashOuts returns cashout transactions stuck in "processing" that need polling.
// Conditions: status=processing, last_polled_at is null or >60s ago, poll_attempts<10.
func (r *KCBTxnRepo) GetStaleCashOuts() ([]*dbmodels.KCBTransaction, error) {
	var txns []*dbmodels.KCBTransaction
	err := r.DB.Where(
		"type = 'cashout' AND status = 'processing' AND poll_attempts < 10 AND (last_polled_at IS NULL OR last_polled_at < ?)",
		time.Now().Add(-60*time.Second),
	).Find(&txns).Error
	return txns, err
}

// IncrementPollAttempt bumps poll_attempts and sets last_polled_at to now.
func (r *KCBTxnRepo) IncrementPollAttempt(id uuid.UUID) error {
	now := time.Now()
	return r.DB.Model(&dbmodels.KCBTransaction{}).Where("id = ?", id).Updates(map[string]interface{}{
		"poll_attempts":  gorm.Expr("poll_attempts + 1"),
		"last_polled_at": now,
	}).Error
}
