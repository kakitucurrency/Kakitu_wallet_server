// models/dbmodels/kcb_transaction.go
package dbmodels

import (
	"time"

	"github.com/shopspring/decimal"
)

// KCBTransaction records every KCB Buni cash-in (STK Push) and cash-out (Funds Transfer MO).
type KCBTransaction struct {
	Base
	Type                 string          `json:"type" gorm:"not null"`                    // "cashin" | "cashout"
	AmountKES            decimal.Decimal `json:"amount_kes" gorm:"type:decimal(15,2);not null"`
	KshsAddress          string          `json:"kshs_address" gorm:"not null"`             // 0x... ETH address
	Phone                string          `json:"phone" gorm:"not null"`                    // 2547XXXXXXXX
	CheckoutRequestID    string          `json:"checkout_request_id" gorm:"uniqueIndex:idx_kcb_checkout_id,where:checkout_request_id <> ''"` // STK Push ID — cashin only
	TransactionReference string          `json:"transaction_reference" gorm:"uniqueIndex:idx_kcb_tx_ref,where:transaction_reference <> ''"` // Our ≤12-char ref — cashout only
	RetrievalRefNumber   string          `json:"retrieval_ref_number" gorm:"index"`         // KCB's ref on FT accept
	MpesaReceipt         string          `json:"mpesa_receipt"`                             // STK receipt on cashin success
	TxHash               string          `json:"tx_hash" gorm:"uniqueIndex:idx_kcb_tx_hash,where:tx_hash <> ''"` // cashout dedup
	Status               string          `json:"status" gorm:"not null;default:'pending'"` // pending|processing|completed|failed|mint_failed
	PollAttempts         int             `json:"poll_attempts" gorm:"default:0"`
	LastPolledAt         *time.Time      `json:"last_polled_at"`
}
