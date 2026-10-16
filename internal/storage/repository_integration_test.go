//go:build integration

package storage_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/cbl/cbl-quote/internal/storage"
	"github.com/cbl/cbl-quote/internal/testsupport"
)

func setupQuoteDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testsupport.PostgresDB(t, "quote", filepath.Join("..", "..", "migrations"))
}

func TestIntegrationQuoteSaveAndGet(t *testing.T) {
	db := setupQuoteDB(t)
	repo := storage.NewRepository(db)

	paymentID := "pay-1"
	asOf := time.Now().UTC().Truncate(time.Millisecond)
	ttlSeconds := 300
	amountAsset := "RUB"
	amountMinor := int64(100_000)
	spread := "0.001234"
	quote := storage.Quote{
		ID:            uuid.Must(uuid.NewV7()).String(),
		PaymentID:     &paymentID,
		BaseAsset:     "USDT",
		QuoteAsset:    "RUB",
		Rate:          "95.50",
		LockedUntil:   time.Now().UTC().Add(15 * time.Minute),
		CreatedAt:     time.Now().UTC(),
		AsOf:          &asOf,
		TTLSeconds:    &ttlSeconds,
		AmountAsset:   &amountAsset,
		AmountMinor:   &amountMinor,
		Sources:       `[{"name":"a","value":"95.50"},{"name":"b","value":"95.60"}]`,
		Spread:        &spread,
		GuardAdjusted: true,
	}
	require.NoError(t, repo.Save(context.Background(), quote))

	loaded, err := repo.Get(context.Background(), quote.ID)
	require.NoError(t, err)
	assert.Equal(t, quote.Rate, loaded.Rate)
	assert.Equal(t, quote.BaseAsset, loaded.BaseAsset)
	require.NotNil(t, loaded.PaymentID)
	assert.Equal(t, paymentID, *loaded.PaymentID)
	require.NotNil(t, loaded.AsOf)
	assert.True(t, asOf.Equal(*loaded.AsOf))
	require.NotNil(t, loaded.TTLSeconds)
	assert.Equal(t, ttlSeconds, *loaded.TTLSeconds)
	require.NotNil(t, loaded.AmountAsset)
	assert.Equal(t, amountAsset, *loaded.AmountAsset)
	require.NotNil(t, loaded.AmountMinor)
	assert.Equal(t, amountMinor, *loaded.AmountMinor)
	assert.JSONEq(t, quote.Sources, loaded.Sources)
	require.NotNil(t, loaded.Spread)
	assert.Equal(t, spread, *loaded.Spread)
	assert.True(t, loaded.GuardAdjusted)

	_, err = repo.Get(context.Background(), uuid.Must(uuid.NewV7()).String())
	require.Error(t, err)
}

func TestIntegrationQuotesAppendOnly(t *testing.T) {
	db := setupQuoteDB(t)
	repo := storage.NewRepository(db)

	id := uuid.Must(uuid.NewV7()).String()
	quote := storage.Quote{
		ID:          id,
		BaseAsset:   "USDT",
		QuoteAsset:  "RUB",
		Rate:        "95.50",
		LockedUntil: time.Now().UTC().Add(15 * time.Minute),
		CreatedAt:   time.Now().UTC(),
	}
	require.NoError(t, repo.Save(context.Background(), quote))

	updateErr := db.Exec("UPDATE quotes SET rate = '1.00' WHERE id = ?", id).Error
	require.Error(t, updateErr)
	assert.Contains(t, updateErr.Error(), "append-only")

	deleteErr := db.Exec("DELETE FROM quotes WHERE id = ?", id).Error
	require.Error(t, deleteErr)
	assert.Contains(t, deleteErr.Error(), "append-only")

	truncateErr := db.Exec("TRUNCATE quotes").Error
	require.Error(t, truncateErr)
	assert.Contains(t, truncateErr.Error(), "append-only")

	duplicateErr := repo.Save(context.Background(), quote)
	require.Error(t, duplicateErr, "a repeated insert of the same id is rejected by the uniqueness constraint")

	loaded, err := repo.Get(context.Background(), id)
	require.NoError(t, err, "the journal is immutable: the row is in place")
	assert.Equal(t, "95.50", loaded.Rate)
}

func TestIntegrationQuotesAuditCheckConstraints(t *testing.T) {
	db := setupQuoteDB(t)
	repo := storage.NewRepository(db)

	zeroTTL := 0
	zeroAmount := int64(0)
	tests := map[string]storage.Quote{
		"zero ttl":    {ID: uuid.Must(uuid.NewV7()).String(), BaseAsset: "USDT", QuoteAsset: "RUB", Rate: "95.50", LockedUntil: time.Now().UTC(), CreatedAt: time.Now().UTC(), TTLSeconds: &zeroTTL},
		"zero amount": {ID: uuid.Must(uuid.NewV7()).String(), BaseAsset: "USDT", QuoteAsset: "RUB", Rate: "95.50", LockedUntil: time.Now().UTC(), CreatedAt: time.Now().UTC(), AmountMinor: &zeroAmount},
	}
	for name, quote := range tests {
		t.Run(name, func(t *testing.T) {
			err := repo.Save(context.Background(), quote)
			require.Error(t, err, "a DB CHECK constraint is the second audit barrier")
		})
	}
}

func TestIntegrationLegacyQuoteReadable(t *testing.T) {
	db := setupQuoteDB(t)
	repo := storage.NewRepository(db)

	// A dev stand row from before 6.3: the audit columns are empty and do not hinder reading.
	id := uuid.Must(uuid.NewV7()).String()
	require.NoError(t, db.Exec(
		`INSERT INTO quotes (id, base_asset, quote_asset, rate, locked_until, created_at) VALUES (?, 'USDT', 'RUB', '95.50', now() + interval '5 minutes', now())`,
		id,
	).Error)

	loaded, err := repo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Nil(t, loaded.AsOf)
	assert.Nil(t, loaded.TTLSeconds)
	assert.Nil(t, loaded.AmountMinor)
	assert.False(t, loaded.GuardAdjusted)
}
