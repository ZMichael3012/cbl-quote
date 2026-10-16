// Package storage: the content is listed in doc.go.
package storage

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// QuoteSource describes a source that participated in the rate lock.
type QuoteSource struct {
	// Name sets the source name.
	Name string `json:"name"`
	// Value sets the source rate as a decimal string.
	Value string `json:"value"`
}

// Quote describes a row of the quotes table (an immutable audit journal).
type Quote struct {
	// ID sets the quote UUIDv7.
	ID string `gorm:"column:id;type:uuid;primaryKey"`
	// PaymentID sets the payment for which the rate is locked; NULL for now (absent from CF-1).
	PaymentID *string `gorm:"column:payment_id"`
	// BaseAsset sets the base asset.
	BaseAsset string `gorm:"column:base_asset"`
	// QuoteAsset sets the quote asset.
	QuoteAsset string `gorm:"column:quote_asset"`
	// Rate sets the rate value as a decimal string.
	Rate string `gorm:"column:rate"`
	// LockedUntil sets the rate lock expiration time.
	LockedUntil time.Time `gorm:"column:locked_until"`
	// CreatedAt sets the record creation time.
	CreatedAt time.Time `gorm:"column:created_at"`
	// AsOf sets the validity time of the locked source rate.
	AsOf *time.Time `gorm:"column:as_of"`
	// TTLSeconds sets the requested lock duration in seconds.
	TTLSeconds *int `gorm:"column:ttl_seconds"`
	// AmountAsset sets the asset of the lock request amount.
	AmountAsset *string `gorm:"column:amount_asset"`
	// AmountMinor sets the request amount in minor units.
	AmountMinor *int64 `gorm:"column:amount_minor"`
	// Sources sets the JSON array of sources that provided the rate (audit).
	Sources string `gorm:"column:sources;type:jsonb"`
	// Spread sets the relative spread of the sources as a decimal string.
	Spread *string `gorm:"column:spread"`
	// GuardAdjusted indicates that the rate was replaced by the last known one due to an anomaly.
	GuardAdjusted bool `gorm:"column:guard_adjusted"`
}

// TableName sets the quotes table name.
func (Quote) TableName() string { return "quotes" }

// Repository reads and saves quotes.
type Repository struct {
	db *gorm.DB
}

// NewRepository creates a Repository.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Save saves the quote for audit: a repeated Save of a row with the same id
// fails with a uniqueness error, and editing existing rows is forbidden
// by a DB trigger (append-only journal). An empty source list is saved
// as an empty jsonb array.
func (r *Repository) Save(ctx context.Context, quote Quote) error {
	if quote.Sources == "" {
		quote.Sources = "[]"
	}
	if err := r.db.WithContext(ctx).Create(&quote).Error; err != nil {
		return fmt.Errorf("storage: save quote: %w", err)
	}
	return nil
}

// Get returns the quote by identifier.
func (r *Repository) Get(ctx context.Context, id string) (Quote, error) {
	var quote Quote
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&quote).Error; err != nil {
		return Quote{}, fmt.Errorf("storage: get quote %s: %w", id, err)
	}
	return quote, nil
}
