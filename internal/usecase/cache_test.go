package usecase_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/usecase"
)

// fakeCache реализует usecase.RateCache как in-memory кеш для unit-тестов.
type fakeCache struct {
	mu       sync.Mutex
	fresh    map[string]domain.Rate
	last     map[string]domain.Rate
	freshTTL time.Duration
	lastTTL  time.Duration
	getErr   error
	setErr   error
}

// newFakeCache создает пустой кеш.
func newFakeCache() *fakeCache {
	return &fakeCache{fresh: map[string]domain.Rate{}, last: map[string]domain.Rate{}}
}

// Get возвращает свежий курс пары.
func (c *fakeCache) Get(_ context.Context, pair string) (domain.Rate, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getErr != nil {
		return domain.Rate{}, false, c.getErr
	}
	rate, ok := c.fresh[pair]
	return rate, ok, nil
}

// Set сохраняет свежий курс пары.
func (c *fakeCache) Set(_ context.Context, rate domain.Rate, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setErr != nil {
		return c.setErr
	}
	c.freshTTL = ttl
	c.fresh[domain.Key(rate.Base, rate.Quote)] = rate
	return nil
}

// GetLast возвращает последний известный курс пары.
func (c *fakeCache) GetLast(_ context.Context, pair string) (domain.Rate, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getErr != nil {
		return domain.Rate{}, false, c.getErr
	}
	rate, ok := c.last[pair]
	return rate, ok, nil
}

// SetLast сохраняет последний известный курс пары.
func (c *fakeCache) SetLast(_ context.Context, rate domain.Rate, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setErr != nil {
		return c.setErr
	}
	c.lastTTL = ttl
	c.last[domain.Key(rate.Base, rate.Quote)] = rate
	return nil
}

// countingSource считает обращения к источнику и отдает заданный курс.
type countingSource struct {
	mu    sync.Mutex
	name  string
	rate  domain.Rate
	err   error
	calls int
}

// Name задает имя источника.
func (s *countingSource) Name() string { return s.name }

// Fetch увеличивает счетчик обращений и возвращает курс.
func (s *countingSource) Fetch(_ context.Context, _, _ string) (domain.Rate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return domain.Rate{}, s.err
	}
	return s.rate, nil
}

// callCount возвращает число обращений к источнику.
func (s *countingSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// newCachedService собирает сервис с кешем и фиксированным источником.
func newCachedService(t *testing.T, source domain.RateSource, cache usecase.RateCache, opts ...usecase.Option) *usecase.Service {
	t.Helper()
	aggregator, err := domain.NewAggregator("2")
	require.NoError(t, err)
	guard, err := domain.NewAnomalyGuard("10")
	require.NoError(t, err)
	allOpts := append([]usecase.Option{usecase.WithRateCache(cache, 5*time.Second, time.Hour)}, opts...)
	return usecase.NewService([]domain.RateSource{source}, aggregator, guard, nil, allOpts...)
}

func TestGetRateCacheHitSkipsSources(t *testing.T) {
	t.Parallel()

	source := &countingSource{name: "a", rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "100.00", AsOf: time.Now()}}
	cache := newFakeCache()
	cached := domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.00", AsOf: time.Now().UTC()}
	require.NoError(t, cache.Set(context.Background(), cached, time.Minute))

	service := newCachedService(t, source, cache)

	rate, err := service.GetRate(context.Background(), "usdt", "rub")
	require.NoError(t, err)
	assert.Equal(t, "95.00", rate.Value)
	assert.Equal(t, 0, source.callCount(), "источник не вызывается при hit")
}

func TestGetRatePopulatesCacheWithTTLs(t *testing.T) {
	t.Parallel()

	source := &countingSource{name: "a", rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.50", AsOf: time.Now()}}
	cache := newFakeCache()
	service := newCachedService(t, source, cache)

	rate, err := service.GetRate(context.Background(), "USDT", "RUB")
	require.NoError(t, err)
	assert.Equal(t, "95.50", rate.Value)
	assert.Equal(t, 1, source.callCount())
	assert.Equal(t, 5*time.Second, cache.freshTTL)
	assert.Equal(t, time.Hour, cache.lastTTL)
	_, ok := cache.last["USDT/RUB"]
	assert.True(t, ok, "last-known заполняется для деградации")
}

func TestGetRateServesLastKnownOnSourceFailure(t *testing.T) {
	t.Parallel()

	source := &countingSource{name: "a", err: errors.New("timeout")}
	cache := newFakeCache()
	last := domain.Rate{Base: "USDT", Quote: "RUB", Value: "94.00", AsOf: time.Now().Add(-time.Minute).UTC()}
	require.NoError(t, cache.SetLast(context.Background(), last, time.Hour))

	service := newCachedService(t, source, cache)

	rate, err := service.GetRate(context.Background(), "USDT", "RUB")
	require.NoError(t, err)
	assert.Equal(t, "94.00", rate.Value)
}

func TestGetRateWithoutFallbackFails(t *testing.T) {
	t.Parallel()

	source := &countingSource{name: "a", err: errors.New("timeout")}
	service := newCachedService(t, source, newFakeCache())

	_, err := service.GetRate(context.Background(), "USDT", "RUB")
	require.ErrorIs(t, err, domain.ErrNoSource)
}

func TestGetRateRejectsUntrustedCachedValues(t *testing.T) {
	t.Parallel()

	source := &countingSource{name: "a", rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.50", AsOf: time.Now()}}
	ctx := context.Background()

	future := newFakeCache()
	require.NoError(t, future.Set(ctx, domain.Rate{
		Base: "USDT", Quote: "RUB", Value: "9999.99", AsOf: time.Now().Add(10 * time.Minute),
	}, time.Minute))
	service := newCachedService(t, source, future)
	rate, err := service.GetRate(ctx, "USDT", "RUB")
	require.NoError(t, err)
	assert.Equal(t, "95.50", rate.Value, "значение из будущего отвергается")
	assert.Equal(t, 1, source.callCount())

	old := newFakeCache()
	require.NoError(t, old.Set(ctx, domain.Rate{
		Base: "USDT", Quote: "RUB", Value: "9999.99", AsOf: time.Now().Add(-10 * time.Minute),
	}, time.Minute))
	service = newCachedService(t, source, old)
	rate, err = service.GetRate(ctx, "USDT", "RUB")
	require.NoError(t, err)
	assert.Equal(t, "95.50", rate.Value, "устаревшее значение отвергается")
	assert.Equal(t, 2, source.callCount())
}

func TestGetRateFallbackReadErrorFails(t *testing.T) {
	t.Parallel()

	source := &countingSource{name: "a", err: errors.New("timeout")}
	cache := newFakeCache()
	cache.getErr = errors.New("redis down")
	service := newCachedService(t, source, cache)

	_, err := service.GetRate(context.Background(), "USDT", "RUB")
	require.ErrorIs(t, err, domain.ErrNoSource)
}

func TestGetRateCacheFailuresDegrade(t *testing.T) {
	t.Parallel()

	source := &countingSource{name: "a", rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.50", AsOf: time.Now()}}
	cache := newFakeCache()
	cache.setErr = errors.New("redis down")
	service := newCachedService(t, source, cache)

	rate, err := service.GetRate(context.Background(), "USDT", "RUB")
	require.NoError(t, err, "ошибка записи кеша не ломает GetRate")
	assert.Equal(t, "95.50", rate.Value)

	cache = newFakeCache()
	cache.getErr = errors.New("redis down")
	service = newCachedService(t, source, cache)
	_, err = service.GetRate(context.Background(), "USDT", "RUB")
	require.NoError(t, err, "ошибка чтения кеша деградирует к источникам")
	assert.Equal(t, 2, source.callCount())
}
