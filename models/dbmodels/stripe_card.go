package dbmodels

import "time"

type StripeCard struct {
	ID                 string    `json:"id" gorm:"primaryKey;type:uuid;default:gen_random_uuid()"`
	KakituAddress      string    `json:"kakitu_address" gorm:"uniqueIndex;not null"`
	StripeCardholderID string    `json:"stripe_cardholder_id" gorm:"not null"`
	StripeCardID       string    `json:"stripe_card_id" gorm:"not null"`
	AddressID          string    `json:"address_id" gorm:"not null"`
	CreatedAt          time.Time `json:"created_at"`
}
