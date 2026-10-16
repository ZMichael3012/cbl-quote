//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/storage"
	"github.com/cbl/cbl-quote/internal/testsupport"
)

// setupRedis starts a Redis container and returns a client.
func setupRedis(t *testing.T) *redis.Client {
	t.Helper()
	return testsupport.RedisClient(t)
}

func TestIntegrationRedisCacheRoundTrip(t *testing.T) {
	client := setupRedis(t)
	cache := storage.NewRedisCache(client)
	ctx := context.Background()

	rate := domain.Rate{
		Base: "USDT", Quote: "RUB", Value: "95.50",
		AsOf: time.Date(2026, 10, 14, 12, 0, 0, 123456789, time.UTC),
	}
	require.NoError(t, cache.Set(ctx, rate, time.Minute))
	require.NoError(t, cache.SetLast(ctx, rate, time.Hour))

	loaded, ok, err := cache.Get(ctx, "USDT/RUB")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, rate.Value, loaded.Value)
	assert.True(t, rate.AsOf.Equal(loaded.AsOf), "AsOf is preserved with nanosecond precision")

	last, ok, err := cache.GetLast(ctx, "USDT/RUB")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, rate.Value, last.Value)

	_, ok, err = cache.Get(ctx, "BTC/RUB")
	require.NoError(t, err)
	assert.False(t, ok, "an unknown pair is a miss without an error")
}

func TestIntegrationRedisCacheTTLExpiry(t *testing.T) {
	client := setupRedis(t)
	cache := storage.NewRedisCache(client)
	ctx := context.Background()

	rate := domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.50", AsOf: time.Now().UTC()}
	require.NoError(t, cache.Set(ctx, rate, 200*time.Millisecond))
	require.NoError(t, cache.SetLast(ctx, rate, time.Hour))

	_, ok, err := cache.Get(ctx, "USDT/RUB")
	require.NoError(t, err)
	require.True(t, ok, "the value is available before the TTL expires")

	time.Sleep(500 * time.Millisecond)

	_, ok, err = cache.Get(ctx, "USDT/RUB")
	require.NoError(t, err)
	assert.False(t, ok, "after the TTL expires the fresh key is empty")

	_, ok, err = cache.GetLast(ctx, "USDT/RUB")
	require.NoError(t, err)
	assert.True(t, ok, "last-known lives longer than the fresh key")
}

func TestIntegrationRedisCacheCorruptedValueMiss(t *testing.T) {
	client := setupRedis(t)
	cache := storage.NewRedisCache(client)
	ctx := context.Background()

	require.NoError(t, client.Set(ctx, "rate:USDT:RUB", "not-a-rate", time.Minute).Err())

	_, ok, err := cache.Get(ctx, "USDT/RUB")
	require.NoError(t, err)
	assert.False(t, ok, "a corrupted value is read as a miss")

	require.NoError(t, client.Set(ctx, "rate:last:USDT:RUB", "95.50|bad-time", time.Minute).Err())
	_, ok, err = cache.GetLast(ctx, "USDT/RUB")
	require.NoError(t, err)
	assert.False(t, ok)
}
