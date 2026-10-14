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

// Префиксы ключей Redis по конвенции CBL-003: свежий курс и последний известный
// для деградации. Пара активов представляется в ключе через двоеточие.
const (
	rateKeyPrefix = "rate:"
	lastKeyPrefix = "rate:last:"
)

// RedisCache кеширует курсы в Redis: свежий ключ с коротким TTL
// и last-known ключ для деградации при недоступности источников.
type RedisCache struct {
	client *redis.Client
}

// NewRedisCache создает RedisCache поверх клиента go-redis.
func NewRedisCache(client *redis.Client) *RedisCache {
	return &RedisCache{client: client}
}

// cacheKey строит ключ Redis по паре "BASE/QUOTE".
func cacheKey(prefix, pair string) string {
	return prefix + strings.Replace(pair, "/", ":", 1)
}

// Get возвращает свежий курс пары; miss возвращается как ok=false без ошибки.
func (c *RedisCache) Get(ctx context.Context, pair string) (domain.Rate, bool, error) {
	return c.get(ctx, cacheKey(rateKeyPrefix, pair), pair)
}

// Set сохраняет свежий курс пары с заданным TTL.
func (c *RedisCache) Set(ctx context.Context, rate domain.Rate, ttl time.Duration) error {
	return c.set(ctx, cacheKey(rateKeyPrefix, domain.Key(rate.Base, rate.Quote)), rate, ttl)
}

// GetLast возвращает последний известный курс пары.
func (c *RedisCache) GetLast(ctx context.Context, pair string) (domain.Rate, bool, error) {
	return c.get(ctx, cacheKey(lastKeyPrefix, pair), pair)
}

// SetLast сохраняет последний известный курс пары с длинным TTL.
func (c *RedisCache) SetLast(ctx context.Context, rate domain.Rate, ttl time.Duration) error {
	return c.set(ctx, cacheKey(lastKeyPrefix, domain.Key(rate.Base, rate.Quote)), rate, ttl)
}

// get читает значение ключа и разбирает его; поврежденное значение - miss.
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

// set записывает значение курса; нулевой TTL отклоняется как ошибка конфигурации.
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

// parseCachedRate разбирает значение "курс|время"; невалидное значение читается как miss.
// Проверка через ParseDecimal защищает от поврежденного или чужого содержимого Redis.
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
