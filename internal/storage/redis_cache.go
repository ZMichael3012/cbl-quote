package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/cbl/cbl-quote/internal/domain"
)

// Redis key prefixes per the CBL-003 convention: fresh rate and last known
// for degradation. The asset pair is represented in the key with a colon.
const (
	rateKeyPrefix = "rate:"
	lastKeyPrefix = "rate:last:"
)

// RedisCache caches rates in Redis: a fresh key with a short TTL
// and a last-known key for degradation when the sources are unavailable.
type RedisCache struct {
	client *redis.Client
}

// NewRedisCache creates a RedisCache over a go-redis client.
func NewRedisCache(client *redis.Client) *RedisCache {
	return &RedisCache{client: client}
}

// cacheKey builds the Redis key from the "BASE/QUOTE" pair.
func cacheKey(prefix, pair string) string {
	return prefix + strings.Replace(pair, "/", ":", 1)
}

// Get returns the fresh rate of the pair; a miss is returned as ok=false without an error.
func (c *RedisCache) Get(ctx context.Context, pair string) (domain.Rate, bool, error) {
	return c.get(ctx, cacheKey(rateKeyPrefix, pair), pair)
}

// Set stores the fresh rate of the pair with the given TTL.
func (c *RedisCache) Set(ctx context.Context, rate domain.Rate, ttl time.Duration) error {
	return c.set(ctx, cacheKey(rateKeyPrefix, domain.Key(rate.Base, rate.Quote)), rate, ttl)
}

// GetLast returns the last known rate of the pair.
func (c *RedisCache) GetLast(ctx context.Context, pair string) (domain.Rate, bool, error) {
	return c.get(ctx, cacheKey(lastKeyPrefix, pair), pair)
}

// SetLast stores the last known rate of the pair with a long TTL.
func (c *RedisCache) SetLast(ctx context.Context, rate domain.Rate, ttl time.Duration) error {
	return c.set(ctx, cacheKey(lastKeyPrefix, domain.Key(rate.Base, rate.Quote)), rate, ttl)
}

// get reads and parses the key value; a corrupted value is a miss.
func (c *RedisCache) get(ctx context.Context, key, pair string) (domain.Rate, bool, error) {
	value, err := c.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return domain.Rate{}, false, nil
	}
	if err != nil {
		return domain.Rate{}, false, fmt.Errorf("storage: cache get %s: %w", key, err)
	}
	rate, ok := parseCachedRate(pair, value)
	return rate, ok, nil
}

// set writes the rate value; a zero TTL is rejected as a configuration error.
func (c *RedisCache) set(ctx context.Context, key string, rate domain.Rate, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("storage: cache set %s: non-positive ttl %s", key, ttl)
	}
	value := rate.Value + "|" + rate.AsOf.UTC().Format(time.RFC3339Nano)
	if err := c.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("storage: cache set %s: %w", key, err)
	}
	return nil
}

// parseCachedRate parses the "rate|time" value; an invalid value is read as a miss.
// The ParseDecimal check protects against corrupted or foreign Redis content.
func parseCachedRate(pair, value string) (domain.Rate, bool) {
	rawRate, rawTime, ok := strings.Cut(value, "|")
	if !ok {
		return domain.Rate{}, false
	}
	if _, err := domain.ParseDecimal(rawRate); err != nil {
		return domain.Rate{}, false
	}
	asOf, err := time.Parse(time.RFC3339Nano, rawTime)
	if err != nil {
		return domain.Rate{}, false
	}
	base, quote, ok := strings.Cut(pair, "/")
	if !ok {
		return domain.Rate{}, false
	}
	return domain.Rate{Base: base, Quote: quote, Value: rawRate, AsOf: asOf}, true
}
