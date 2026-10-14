// Пакет storage: состав указан в doc.go.
package storage

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// QuoteSource описывает источник, участвовавший в фиксации курса.
type QuoteSource struct {
	// Name задает имя источника.
	Name string `json:"name"`
	// Value задает курс источника десятичной строкой.
	Value string `json:"value"`
}

// Quote описывает строку таблицы quotes (неизменяемый журнал аудита).
type Quote struct {
	// ID задает UUIDv7 котировки.
	ID string `gorm:"column:id;type:uuid;primaryKey"`
	// PaymentID задает платеж, для которого зафиксирован курс; пока NULL (нет в CF-1).
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
	// AsOf задает время актуальности зафиксированного курса источников.
	AsOf *time.Time `gorm:"column:as_of"`
	// TTLSeconds задает запрошенный срок фиксации в секундах.
	TTLSeconds *int `gorm:"column:ttl_seconds"`
	// AmountAsset задает актив суммы запроса фиксации.
	AmountAsset *string `gorm:"column:amount_asset"`
	// AmountMinor задает сумму запроса в минимальных единицах.
	AmountMinor *int64 `gorm:"column:amount_minor"`
	// Sources задает JSON-массив источников, давших курс (аудит).
	Sources string `gorm:"column:sources;type:jsonb"`
	// Spread задает относительный разброс источников десятичной строкой.
	Spread *string `gorm:"column:spread"`
	// GuardAdjusted указывает, что курс заменен последним известным из-за аномалии.
	GuardAdjusted bool `gorm:"column:guard_adjusted"`
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

// Save сохраняет котировку для аудита: повторный Save строки с тем же id
// завершается ошибкой уникальности, правка существующих строк запрещена
// триггером БД (журнал append-only). Пустой список источников сохраняется
// как пустой jsonb-массив.
func (r *Repository) Save(ctx context.Context, quote Quote) error {
	if quote.Sources == "" {
		quote.Sources = "[]"
	}
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
