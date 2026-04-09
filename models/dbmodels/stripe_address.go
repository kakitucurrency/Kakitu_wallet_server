package dbmodels

import "time"

// StripeAddress holds a real EU postal address from the pool.
// Each unassigned address (AssignedTo == nil) is available for a new Kakitu user's card.
type StripeAddress struct {
	Base
	Line1      string     `json:"line1" gorm:"not null"`
	City       string     `json:"city" gorm:"not null"`
	Country    string     `json:"country" gorm:"not null;size:2"` // ISO 3166-1 alpha-2
	PostalCode string     `json:"postal_code" gorm:"not null"`
	AssignedTo *string    `json:"assigned_to" gorm:"uniqueIndex;default:null"`
	AssignedAt *time.Time `json:"assigned_at" gorm:"default:null"`
}
