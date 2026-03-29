package dbmodels

import "github.com/shopspring/decimal"

// MpesaTransaction records every M-Pesa cash-in and cash-out event
type MpesaTransaction struct {
	Base
	Type          string          `json:"type" gorm:"not null"`
	AmountKes     decimal.Decimal `json:"amount_kes" gorm:"type:decimal(10,2);not null"`
	KshsAddress   string          `json:"kshs_address" gorm:"not null"`
	MerchantReqID string          `json:"merchant_req_id" gorm:"uniqueIndex"`
	MpesaReceipt  string          `json:"mpesa_receipt"`
	TxHash        string          `json:"tx_hash" gorm:"uniqueIndex"`
	Status        string          `json:"status" gorm:"not null;default:'pending'"`
}
