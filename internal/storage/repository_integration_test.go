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
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/cbl/cbl-kit/storagex"

	"github.com/cbl/cbl-quote/internal/storage"
)

func setupQuoteDB(t *testing.T) *gorm.DB {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("quote"),
		tcpostgres.WithUsername("cbl"),
		tcpostgres.WithPassword("cbl"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, storagex.Migrate(ctx, db, filepath.Join("..", "..", "migrations")))
	return db
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
