// Пакет storage: состав указан в doc.go.
package storage

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Quote описывает строку таблицы quotes.
type Quote struct {
	// ID задает UUIDv7 котировки.
	ID string `gorm:"column:id;type:uuid;primaryKey"`
	// PaymentID задает платеж, для которого зафиксирован курс.
	PaymentID *string `gorm:"column:payment_id"`
	// BaseAsset задает базовый актив.
	BaseAsset string `gorm:"column:base_asset"`
	// QuoteAsset задает котируемый актив.
	QuoteAsset string `gorm:"column:quote_asset"`
	// Rate задает значение курса десятичной строкой.
	Rate string `gorm:"column:rate"`
	// LockedUntil задает срок фиксации курса.
	LockedUntil time.Time `gorm:"column:locked_until"`
	// CreatedAt задает время создания записи.
	CreatedAt time.Time `gorm:"column:created_at"`
}

// TableName задает имя таблицы quotes.
func (Quote) TableName() string { return "quotes" }

// Repository читает и сохраняет котировки.
type Repository struct {
	db *gorm.DB
}

// NewRepository создает Repository.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Save сохраняет котировку для аудита.
func (r *Repository) Save(ctx context.Context, quote Quote) error {
	if err := r.db.WithContext(ctx).Create(&quote).Error; err != nil {
		return fmt.Errorf("storage: save quote: %w", err)
	}
	return nil
}

// Get возвращает котировку по идентификатору.
func (r *Repository) Get(ctx context.Context, id string) (Quote, error) {
	var quote Quote
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&quote).Error; err != nil {
		return Quote{}, fmt.Errorf("storage: get quote %s: %w", id, err)
	}
	return quote, nil
}
