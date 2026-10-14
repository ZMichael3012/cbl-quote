//go:build integration

package usecase_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/storage"
	"github.com/cbl/cbl-quote/internal/testsupport"
	"github.com/cbl/cbl-quote/internal/usecase"
)

// newIntegrationService собирает сервис на реальных PostgreSQL и Redis.
func newIntegrationService(t *testing.T, source domain.RateSource, cacheTTL, lastTTL time.Duration) (*usecase.Service, *storage.Repository) {
	t.Helper()
	db := testsupport.PostgresDB(t, "quote", filepath.Join("..", "..", "migrations"))
	repo := storage.NewRepository(db)
	redisClient := testsupport.RedisClient(t)
	cache := storage.NewRedisCache(redisClient)

	aggregator, err := domain.NewAggregator("2")
	require.NoError(t, err)
	guard, err := domain.NewAnomalyGuard("10")
	require.NoError(t, err)
	service := usecase.NewService([]domain.RateSource{source}, aggregator, guard, nil,
		usecase.WithRateCache(cache, cacheTTL, lastTTL),
		usecase.WithQuoteStorage(repo),
		usecase.WithLockTTL(5*time.Minute, 15*time.Minute),
	)
	return service, repo
}

// setError задает ошибку источника для сценариев деградации в интеграционных тестах.
func (s *countingSource) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func TestIntegrationGetRateCacheHitRedis(t *testing.T) {
	source := &countingSource{
		name: "a",
		rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.50", AsOf: time.Now().UTC()},
	}
	service, _ := newIntegrationService(t, source, 10*time.Second, time.Hour)
	ctx := context.Background()

	first, err := service.GetRate(ctx, "USDT", "RUB")
	require.NoError(t, err)
	require.Equal(t, "95.50", first.Value)
	require.Equal(t, 1, source.callCount())

	second, err := service.GetRate(ctx, "USDT", "RUB")
	require.NoError(t, err)
	assert.Equal(t, "95.50", second.Value)
	assert.Equal(t, 1, source.callCount(), "второй вызов обслужен из Redis без обращения к источнику")
}

func TestIntegrationGetRateFallbackLastKnownRedis(t *testing.T) {
	source := &countingSource{
		name: "a",
		rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.50", AsOf: time.Now().UTC()},
	}
	service, _ := newIntegrationService(t, source, 50*time.Millisecond, time.Hour)
	ctx := context.Background()

	first, err := service.GetRate(ctx, "USDT", "RUB")
	require.NoError(t, err)
	require.Equal(t, "95.50", first.Value)

	source.setError(errors.New("source down"))
	time.Sleep(120 * time.Millisecond)

	fallback, err := service.GetRate(ctx, "USDT", "RUB")
	require.NoError(t, err, "последний известный курс обслуживает запрос при недоступности источников")
	assert.Equal(t, "95.50", fallback.Value)
	assert.Equal(t, 2, source.callCount())
}

func TestIntegrationLockQuotePersistsQuote(t *testing.T) {
	source := &countingSource{
		name: "a",
		rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.50", AsOf: time.Now().UTC()},
	}
	service, repo := newIntegrationService(t, source, 10*time.Second, time.Hour)

	locked, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
		FromAsset:   "USDT",
		ToAsset:     "RUB",
		AmountAsset: "RUB",
		AmountMinor: 250_000,
		TTLSeconds:  90,
	})
	require.NoError(t, err)
	parsedID, parseErr := uuid.Parse(locked.ID)
	require.NoError(t, parseErr)
	assert.Equal(t, uuid.Version(7), parsedID.Version())

	saved, err := repo.Get(context.Background(), locked.ID)
	require.NoError(t, err)
	assert.Equal(t, "USDT", saved.BaseAsset)
	assert.Equal(t, "RUB", saved.QuoteAsset)
	assert.Equal(t, "95.50", saved.Rate)
	assert.Nil(t, saved.PaymentID)
	assert.WithinDuration(t, locked.LockedUntil, saved.LockedUntil, time.Second)
	assert.WithinDuration(t, time.Now().UTC().Add(90*time.Second), saved.LockedUntil, 5*time.Second)
}
