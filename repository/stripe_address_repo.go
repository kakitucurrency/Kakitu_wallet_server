package repository

import (
	"errors"
	"time"

	"github.com/kakitucurrency/kakitu-wallet-server/models/dbmodels"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrNoAddressesAvailable = errors.New("no addresses available in pool")

type StripeAddressRepo struct {
	DB *gorm.DB
}

// AssignAddress atomically claims one unassigned address for the given kakituAddress.
// Uses SELECT FOR UPDATE SKIP LOCKED so concurrent requests never race on the same row.
// Returns ErrNoAddressesAvailable if the pool is empty.
func (r *StripeAddressRepo) AssignAddress(kakituAddress string) (*dbmodels.StripeAddress, error) {
	var addr dbmodels.StripeAddress
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("assigned_to IS NULL").
			First(&addr)
		if res.Error != nil {
			if errors.Is(res.Error, gorm.ErrRecordNotFound) {
				return ErrNoAddressesAvailable
			}
			return res.Error
		}
		now := time.Now().UTC()
		return tx.Model(&addr).Updates(map[string]interface{}{
			"assigned_to": kakituAddress,
			"assigned_at": now,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return &addr, nil
}

// AvailableCount returns how many unassigned addresses remain in the pool.
func (r *StripeAddressRepo) AvailableCount() (int64, error) {
	var count int64
	err := r.DB.Model(&dbmodels.StripeAddress{}).Where("assigned_to IS NULL").Count(&count).Error
	return count, err
}
