package dbmodels

// StripeCard records a card issued for a Kakitu user (currently via Reap).
type StripeCard struct {
	Base
	KakituAddress      string  `json:"kakitu_address" gorm:"uniqueIndex;not null"`
	StripeCardholderID string  `json:"stripe_cardholder_id" gorm:"not null;index"`
	StripeCardID       string  `json:"stripe_card_id" gorm:"uniqueIndex;not null"`
	AddressID          *string `json:"address_id" gorm:"default:null"`
}
