package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/storage"
	"github.com/cbl/cbl-quote/internal/usecase"
)

// fakeQuoteStorage реализует usecase.QuoteStorage и запоминает сохраненные котировки.
type fakeQuoteStorage struct {
	mu    sync.Mutex
	saved []storage.Quote
	err   error
}

// Save сохраняет котировку в память.
func (s *fakeQuoteStorage) Save(_ context.Context, quote storage.Quote) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.saved = append(s.saved, quote)
	return nil
}

// last возвращает последнюю сохраненную котировку.
func (s *fakeQuoteStorage) last() (storage.Quote, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.saved) == 0 {
		return storage.Quote{}, false
	}
	return s.saved[len(s.saved)-1], true
}

// fixedClock возвращает постоянное время.
func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

// newLockService собирает сервис с хранилищем, часами и источником курса.
func newLockService(t *testing.T, storage *fakeQuoteStorage, clock func() time.Time, opts ...usecase.Option) *usecase.Service {
	t.Helper()
	aggregator, err := domain.NewAggregator("2")
	require.NoError(t, err)
	guard, err := domain.NewAnomalyGuard("10")
	require.NoError(t, err)
	source := &countingSource{
		name: "a",
		rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.50", AsOf: clock()},
	}
	allOpts := []usecase.Option{
		usecase.WithQuoteStorage(storage),
		usecase.WithClock(clock),
		usecase.WithLockTTL(5*time.Minute, 15*time.Minute),
	}
	allOpts = append(allOpts, opts...)
	return usecase.NewService([]domain.RateSource{source}, aggregator, guard, nil, allOpts...)
}

func TestLockQuoteSuccess(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	store := &fakeQuoteStorage{}
	service := newLockService(t, store, fixedClock(now))

	locked, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
		FromAsset:   "usdt",
		ToAsset:     "rub",
		AmountAsset: "RUB",
		AmountMinor: 100_000,
	})
	require.NoError(t, err)

	_, parseErr := uuid.Parse(locked.ID)
	require.NoError(t, parseErr, "quote_id обязан быть UUID")
	parsedID, _ := uuid.Parse(locked.ID)
	assert.Equal(t, uuid.Version(7), parsedID.Version(), "quote_id обязан быть UUIDv7")
	assert.Equal(t, "95.50", locked.Rate)
	assert.Equal(t, now.Add(5*time.Minute), locked.LockedUntil)
	assert.Equal(t, "USDT", locked.Base)
	assert.Equal(t, "RUB", locked.Quote)

	saved, ok := store.last()
	require.True(t, ok)
	assert.Equal(t, locked.ID, saved.ID)
	assert.Equal(t, "USDT", saved.BaseAsset)
	assert.Equal(t, "RUB", saved.QuoteAsset)
	assert.Equal(t, "95.50", saved.Rate)
	assert.Equal(t, locked.LockedUntil, saved.LockedUntil)
	assert.Equal(t, now, saved.CreatedAt)
	assert.Nil(t, saved.PaymentID)

	// Аудит-поля (6.3): метаданные фиксации заполнены.
	require.NotNil(t, saved.AsOf)
	assert.Equal(t, now, *saved.AsOf)
	require.NotNil(t, saved.TTLSeconds)
	assert.Equal(t, 300, *saved.TTLSeconds)
	require.NotNil(t, saved.AmountAsset)
	assert.Equal(t, "RUB", *saved.AmountAsset)
	require.NotNil(t, saved.AmountMinor)
	assert.Equal(t, int64(100_000), *saved.AmountMinor)
	require.NotNil(t, saved.Spread)
	assert.Equal(t, "0.000000", *saved.Spread, "один источник - нулевой разброс")
	assert.False(t, saved.GuardAdjusted)
	var sources []storage.QuoteSource
	require.NoError(t, json.Unmarshal([]byte(saved.Sources), &sources))
	require.Len(t, sources, 1)
	assert.Equal(t, "a", sources[0].Name)
	assert.Equal(t, "95.50", sources[0].Value)
}

// setRate заменяет курс источника для проверки защиты от аномалий.
func (s *countingSource) setRate(rate domain.Rate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rate = rate
}

func TestLockQuoteGuardAdjustedAudit(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	aggregator, err := domain.NewAggregator("2")
	require.NoError(t, err)
	guard, err := domain.NewAnomalyGuard("10")
	require.NoError(t, err)
	source := &countingSource{
		name: "a",
		rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.50", AsOf: now},
	}
	store := &fakeQuoteStorage{}
	service := usecase.NewService([]domain.RateSource{source}, aggregator, guard, nil,
		usecase.WithQuoteStorage(store),
		usecase.WithClock(fixedClock(now)),
		usecase.WithLockTTL(5*time.Minute, 15*time.Minute),
	)

	_, err = service.LockQuote(context.Background(), usecase.LockQuoteRequest{
		FromAsset: "USDT", ToAsset: "RUB", AmountAsset: "RUB", AmountMinor: 1,
	})
	require.NoError(t, err)

	source.setRate(domain.Rate{Base: "USDT", Quote: "RUB", Value: "120.00", AsOf: now.Add(time.Minute)})
	locked, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
		FromAsset: "USDT", ToAsset: "RUB", AmountAsset: "RUB", AmountMinor: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, "95.50", locked.Rate, "аномальный курс заменен последним известным")

	saved, ok := store.last()
	require.True(t, ok)
	assert.True(t, saved.GuardAdjusted, "факт подмены виден в аудите")
	require.NotNil(t, saved.AsOf)
	assert.Equal(t, now, *saved.AsOf, "as_of относится к выданному курсу, а не к отвергнутой выборке")
	var sources []storage.QuoteSource
	require.NoError(t, json.Unmarshal([]byte(saved.Sources), &sources))
	require.Len(t, sources, 1)
	assert.Equal(t, "120.00", sources[0].Value, "в аудите остаются сырые данные источников")
}

func TestLockQuoteCustomTTL(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	store := &fakeQuoteStorage{}
	service := newLockService(t, store, fixedClock(now))

	locked, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
		FromAsset:   "USDT",
		ToAsset:     "RUB",
		AmountAsset: "USDT",
		AmountMinor: 1,
		TTLSeconds:  120,
	})
	require.NoError(t, err)
	assert.Equal(t, now.Add(120*time.Second), locked.LockedUntil)
	saved, ok := store.last()
	require.True(t, ok)
	require.NotNil(t, saved.TTLSeconds)
	assert.Equal(t, 120, *saved.TTLSeconds, "аудит хранит запрошенный TTL")
}

func TestLockQuoteSubSecondTTLUsesDefault(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	store := &fakeQuoteStorage{}
	service := newLockService(t, store, fixedClock(now),
		usecase.WithLockTTL(500*time.Millisecond, 15*time.Minute))

	locked, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
		FromAsset: "USDT", ToAsset: "RUB", AmountAsset: "RUB", AmountMinor: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, now.Add(5*time.Minute), locked.LockedUntil, "субсекундный TTL заменяется дефолтом")

	saved, ok := store.last()
	require.True(t, ok)
	require.NotNil(t, saved.TTLSeconds)
	assert.Equal(t, 300, *saved.TTLSeconds, "ttl_seconds не может быть нулевым (CHECK)")
}

func TestLockQuoteRejectsTTLAboveMax(t *testing.T) {
	t.Parallel()

	store := &fakeQuoteStorage{}
	service := newLockService(t, store, fixedClock(time.Now()))

	_, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
		FromAsset:   "USDT",
		ToAsset:     "RUB",
		AmountAsset: "RUB",
		AmountMinor: 1,
		TTLSeconds:  901,
	})
	require.ErrorIs(t, err, domain.ErrInvalidLock)
	_, ok := store.last()
	assert.False(t, ok, "котировка не сохраняется при ошибке валидации")
}

func TestLockQuoteRejectsInvalidAmount(t *testing.T) {
	t.Parallel()

	store := &fakeQuoteStorage{}
	service := newLockService(t, store, fixedClock(time.Now()))
	ctx := context.Background()

	cases := []usecase.LockQuoteRequest{
		{FromAsset: "USDT", ToAsset: "RUB", AmountAsset: "RUB", AmountMinor: 0},
		{FromAsset: "USDT", ToAsset: "RUB", AmountAsset: "RUB", AmountMinor: -5},
		{FromAsset: "USDT", ToAsset: "RUB", AmountAsset: "EUR", AmountMinor: 10},
		{FromAsset: "USDT", ToAsset: "RUB", AmountAsset: "", AmountMinor: 10},
	}
	for i, req := range cases {
		_, err := service.LockQuote(ctx, req)
		require.ErrorIs(t, err, domain.ErrInvalidLock, "case %d", i)
	}
	_, ok := store.last()
	assert.False(t, ok)
}

func TestLockQuoteRejectsInvalidPair(t *testing.T) {
	t.Parallel()

	store := &fakeQuoteStorage{}
	service := newLockService(t, store, fixedClock(time.Now()))
	ctx := context.Background()

	_, err := service.LockQuote(ctx, usecase.LockQuoteRequest{
		FromAsset: "", ToAsset: "RUB", AmountAsset: "RUB", AmountMinor: 1,
	})
	require.ErrorIs(t, err, domain.ErrInvalidLock)

	_, err = service.LockQuote(ctx, usecase.LockQuoteRequest{
		FromAsset: "USDT", ToAsset: "usdt", AmountAsset: "USDT", AmountMinor: 1,
	})
	require.ErrorIs(t, err, domain.ErrInvalidLock)

	_, err = service.LockQuote(ctx, usecase.LockQuoteRequest{
		FromAsset: "USDT!", ToAsset: "RUB", AmountAsset: "RUB", AmountMinor: 1,
	})
	require.ErrorIs(t, err, domain.ErrInvalidLock)
}

func TestLockQuoteMaxTTLAllowed(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	store := &fakeQuoteStorage{}
	service := newLockService(t, store, fixedClock(now))

	locked, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
		FromAsset: "USDT", ToAsset: "RUB", AmountAsset: "RUB", AmountMinor: 1,
		TTLSeconds: 900,
	})
	require.NoError(t, err, "TTL ровно на пределе допустим")
	assert.Equal(t, now.Add(15*time.Minute), locked.LockedUntil)
}

func TestLockQuoteIdenticalPairWhitelist(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	aggregator, err := domain.NewAggregator("2")
	require.NoError(t, err)
	guard, err := domain.NewAnomalyGuard("10")
	require.NoError(t, err)
	source := &countingSource{
		name: "a",
		rate: domain.Rate{Base: "USDT", Quote: "USD", Value: "1.00", AsOf: now},
	}
	store := &fakeQuoteStorage{}
	service := usecase.NewService([]domain.RateSource{source}, aggregator, guard, nil,
		usecase.WithIdenticalPairs("USDT/USD"),
		usecase.WithQuoteStorage(store),
		usecase.WithClock(fixedClock(now)),
		usecase.WithLockTTL(5*time.Minute, 15*time.Minute),
	)

	locked, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
		FromAsset: "USDT", ToAsset: "USD", AmountAsset: "USDT", AmountMinor: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, "1.00", locked.Rate)
}

func TestLockQuoteWithoutStorage(t *testing.T) {
	t.Parallel()

	aggregator, err := domain.NewAggregator("2")
	require.NoError(t, err)
	guard, err := domain.NewAnomalyGuard("10")
	require.NoError(t, err)
	source := &countingSource{name: "a", rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.50", AsOf: time.Now()}}
	service := usecase.NewService([]domain.RateSource{source}, aggregator, guard, nil)

	_, err = service.LockQuote(context.Background(), usecase.LockQuoteRequest{
		FromAsset: "USDT", ToAsset: "RUB", AmountAsset: "RUB", AmountMinor: 1,
	})
	require.ErrorIs(t, err, domain.ErrLockStorage)
}

func TestLockQuoteStorageFailure(t *testing.T) {
	t.Parallel()

	store := &fakeQuoteStorage{err: errors.New("pg down")}
	service := newLockService(t, store, fixedClock(time.Now()))

	_, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
		FromAsset: "USDT", ToAsset: "RUB", AmountAsset: "RUB", AmountMinor: 1,
	})
	require.ErrorIs(t, err, domain.ErrLockStorage)
}

func TestLockQuoteIgnoresCachedRate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	store := &fakeQuoteStorage{}
	cache := newFakeCache()
	require.NoError(t, cache.Set(context.Background(), domain.Rate{
		Base: "USDT", Quote: "RUB", Value: "90.00", AsOf: now.Add(-time.Second),
	}, time.Minute))
	service := newLockService(t, store, fixedClock(now), usecase.WithRateCache(cache, 5*time.Second, time.Hour))

	locked, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
		FromAsset: "USDT", ToAsset: "RUB", AmountAsset: "RUB", AmountMinor: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, "95.50", locked.Rate, "фиксация курса не доверяет кешу и берет свежие данные")
}
