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
	quote := storage.Quote{
		ID:          uuid.Must(uuid.NewV7()).String(),
		PaymentID:   &paymentID,
		BaseAsset:   "USDT",
		QuoteAsset:  "RUB",
		Rate:        "95.50",
		LockedUntil: time.Now().UTC().Add(15 * time.Minute),
		CreatedAt:   time.Now().UTC(),
	}
	require.NoError(t, repo.Save(context.Background(), quote))

	loaded, err := repo.Get(context.Background(), quote.ID)
	require.NoError(t, err)
	assert.Equal(t, quote.Rate, loaded.Rate)
	assert.Equal(t, quote.BaseAsset, loaded.BaseAsset)
	require.NotNil(t, loaded.PaymentID)
	assert.Equal(t, paymentID, *loaded.PaymentID)

	_, err = repo.Get(context.Background(), uuid.Must(uuid.NewV7()).String())
	require.Error(t, err)
}
