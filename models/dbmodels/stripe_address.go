package dbmodels

import "time"

type StripeAddress struct {
	ID         string     `json:"id" gorm:"primaryKey;type:uuid;default:gen_random_uuid()"`
	Line1      string     `json:"line1" gorm:"not null"`
	City       string     `json:"city" gorm:"not null"`
	Country    string     `json:"country" gorm:"not null;size:2"`
	PostalCode string     `json:"postal_code" gorm:"not null"`
	AssignedTo *string    `json:"assigned_to" gorm:"uniqueIndex;default:null"`
	AssignedAt *time.Time `json:"assigned_at" gorm:"default:null"`
	CreatedAt  time.Time  `json:"created_at"`
}
