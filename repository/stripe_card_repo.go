package repository

import (
	"errors"

	"github.com/kakitucurrency/kakitu-wallet-server/models/dbmodels"
	"gorm.io/gorm"
)

type StripeCardRepo struct {
	DB *gorm.DB
}

// GetCardByAccount returns the card for a kakituAddress, or nil if none exists.
func (r *StripeCardRepo) GetCardByAccount(kakituAddress string) (*dbmodels.StripeCard, error) {
	var card dbmodels.StripeCard
	err := r.DB.Where("kakitu_address = ?", kakituAddress).First(&card).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &card, nil
}

// CreateCard persists a newly issued card.
// cardholderID is the provider's cardholder/meta identifier.
// cardID is the provider's card identifier.
func (r *StripeCardRepo) CreateCard(kakituAddress, cardholderID, cardID string) (*dbmodels.StripeCard, error) {
	card := &dbmodels.StripeCard{
		KakituAddress:      kakituAddress,
		StripeCardholderID: cardholderID,
		StripeCardID:       cardID,
	}
	if err := r.DB.Create(card).Error; err != nil {
		return nil, err
	}
	return card, nil
}
