package repository

import (
	"github.com/kakitucurrency/kakitu-wallet-server/models/dbmodels"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type MpesaTxnRepo struct {
	DB *gorm.DB
}

// CreatePendingCashIn saves a new pending cash-in transaction.
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

// CreatePendingCashOut saves a new pending cash-out transaction.
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

// FindByMerchantReqID returns a transaction by Daraja CheckoutRequestID or ConversationID.
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

// UpdateStatus sets the status and optionally mpesa_receipt on a transaction.
func (r *MpesaTxnRepo) UpdateStatus(id string, status, receipt string) error {
	updates := map[string]interface{}{"status": status}
	if receipt != "" {
		updates["mpesa_receipt"] = receipt
	}
	return r.DB.Model(&dbmodels.MpesaTransaction{}).
		Where("merchant_req_id = ?", id).
		Updates(updates).Error
}
