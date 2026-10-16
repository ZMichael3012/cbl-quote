// Package usecase: the use cases are listed in doc.go.
package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/storage"
)

// Default values for the cache and quote locking,
// as well as trust boundaries for values from Redis.
const (
	defaultCacheTTL   = 5 * time.Second
	defaultLastTTL    = time.Hour
	defaultLockTTL    = 5 * time.Minute
	defaultLockMaxTTL = 15 * time.Minute
	// cacheClockSkew sets the allowed clock skew of a cached value.
	cacheClockSkew = time.Minute
	// maxFreshHitAge sets the upper bound of the allowed age of a fresh value.
	maxFreshHitAge = 2 * time.Minute
	// warnInterval sets the minimum interval between same-kind WARN messages.
	warnInterval = 30 * time.Second
)

// RateCache caches rates; Redis is only a fast path, its errors do not break GetRate.
type RateCache interface {
	// Get returns the fresh rate of the pair; a miss is ok=false without an error.
	Get(ctx context.Context, pair string) (domain.Rate, bool, error)
	// Set stores the fresh rate of the pair with a TTL.
	Set(ctx context.Context, rate domain.Rate, ttl time.Duration) error
	// GetLast returns the last known rate of the pair for degradation.
	GetLast(ctx context.Context, pair string) (domain.Rate, bool, error)
	// SetLast stores the last known rate of the pair with a long TTL.
	SetLast(ctx context.Context, rate domain.Rate, ttl time.Duration) error
}

// QuoteStorage saves locked quotes for audit.
type QuoteStorage interface {
	// Save saves the quote.
	Save(ctx context.Context, quote storage.Quote) error
}

// LockQuoteRequest describes a quote lock request.
type LockQuoteRequest struct {
	// FromAsset sets the base asset.
	FromAsset string
	// ToAsset sets the quote asset.
	ToAsset string
	// AmountAsset sets the asset of the amount for which the rate is locked.
	AmountAsset string
	// AmountMinor sets the amount in minor units.
	AmountMinor int64
	// TTLSeconds sets the lock duration; 0 means the default value.
	TTLSeconds uint32
}

// LockedQuote describes a locked quote.
type LockedQuote struct {
	// ID sets the quote identifier.
	ID string
	// Base sets the base asset.
	Base string
	// Quote sets the quote asset.
	Quote string
	// Rate sets the locked rate as a decimal string.
	Rate string
	// LockedUntil sets the lock expiration time.
	LockedUntil time.Time
	// CreatedAt sets the quote creation time.
	CreatedAt time.Time
}

// Service aggregates source rates and protects against anomalous values.
type Service struct {
	sources    []domain.RateSource
	aggregator *domain.Aggregator
	guard      *domain.AnomalyGuard
	identical  map[string]struct{}
	log        *zap.Logger
	cache      RateCache
	cacheTTL   time.Duration
	lastTTL    time.Duration
	quotes     QuoteStorage
	lockTTL    time.Duration
	lockMaxTTL time.Duration
	clock      func() time.Time
	throttle   *warnThrottle
}

// Option configures Service.
type Option func(*Service)

// WithIdenticalPairs allows pairs with identical assets, for example USDT/USD.
func WithIdenticalPairs(pairs ...string) Option {
	return func(s *Service) {
		for _, pair := range pairs {
			base, quote, ok := strings.Cut(pair, "/")
			if !ok {
				continue
			}
			s.identical[domain.Key(base, quote)] = struct{}{}
		}
	}
}

// WithRateCache attaches the rate cache; non-positive TTLs are replaced by default values.
func WithRateCache(cache RateCache, ttl, lastTTL time.Duration) Option {
	return func(s *Service) {
		s.cache = cache
		s.cacheTTL = ttl
		if s.cacheTTL <= 0 {
			s.cacheTTL = defaultCacheTTL
		}
		s.lastTTL = lastTTL
		if s.lastTTL <= 0 {
			s.lastTTL = defaultLastTTL
		}
	}
}

// WithQuoteStorage attaches the locked quote storage.
func WithQuoteStorage(quotes QuoteStorage) Option {
	return func(s *Service) { s.quotes = quotes }
}

// WithLockTTL sets the default lock duration and the upper limit;
// values below one second and invalid values are replaced by the default value:
// the audit TTL is stored in whole seconds, and a zero ttl_seconds is forbidden by a CHECK.
func WithLockTTL(defaultTTL, maxTTL time.Duration) Option {
	return func(s *Service) {
		s.lockTTL = defaultTTL
		if s.lockTTL < time.Second {
			s.lockTTL = defaultLockTTL
		}
		s.lockMaxTTL = maxTTL
		if s.lockMaxTTL < time.Second {
			s.lockMaxTTL = defaultLockMaxTTL
		}
		if s.lockMaxTTL < s.lockTTL {
			s.lockMaxTTL = s.lockTTL
		}
	}
}

// WithClock sets the time source; used by tests.
func WithClock(clock func() time.Time) Option {
	return func(s *Service) {
		if clock != nil {
			s.clock = clock
		}
	}
}

// NewService creates a Service.
func NewService(sources []domain.RateSource, aggregator *domain.Aggregator, guard *domain.AnomalyGuard, log *zap.Logger, opts ...Option) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	service := &Service{
		sources:    sources,
		aggregator: aggregator,
		guard:      guard,
		identical:  make(map[string]struct{}),
		log:        log,
		cacheTTL:   defaultCacheTTL,
		lastTTL:    defaultLastTTL,
		lockTTL:    defaultLockTTL,
		lockMaxTTL: defaultLockMaxTTL,
		clock:      time.Now,
	}
	for _, opt := range opts {
		opt(service)
	}
	service.throttle = newWarnThrottle(warnInterval, service.clock)
	return service
}

// GetRate returns the median rate of the pair with caching and anomaly protection.
// Values from Redis are treated as untrusted input: age and format are validated.
// When all sources are unavailable, the last known rate from the cache is returned
// within lastTTL, otherwise ErrNoSource; on spread violation - ErrSpread.
func (s *Service) GetRate(ctx context.Context, base, quote string) (domain.Rate, error) {
	base, quote, pair, err := s.validatePair(base, quote)
	if err != nil {
		return domain.Rate{}, err
	}

	if s.cache != nil {
		cached, ok, cacheErr := s.cache.Get(ctx, pair)
		switch {
		case cacheErr != nil:
			s.warn("cache", "rate cache read failed", zap.String("pair", pair), zap.Error(cacheErr))
		case ok && s.freshHitUsable(cached):
			return cached, nil
		case ok:
			s.warn("cache-stale:"+pair, "rate cache value rejected",
				zap.String("pair", pair), zap.Time("as_of", cached.AsOf))
		}
	}

	rate, _, err := s.fetchRate(ctx, base, quote, pair)
	if err == nil {
		s.storeRate(ctx, pair, rate)
		return rate, nil
	}
	if !errors.Is(err, domain.ErrNoSource) {
		return domain.Rate{}, err
	}

	if s.cache != nil {
		last, ok, cacheErr := s.cache.GetLast(ctx, pair)
		switch {
		case cacheErr != nil:
			s.warn("cache", "rate cache fallback read failed", zap.String("pair", pair), zap.Error(cacheErr))
		case ok && s.lastKnownUsable(last):
			s.warn("fallback:"+pair, "serving last known rate",
				zap.String("pair", pair), zap.Time("as_of", last.AsOf))
			return last, nil
		}
	}
	s.warn("sources:"+pair, "all rate sources failed",
		zap.String("pair", pair), zap.Int("sources", len(s.sources)))
	return domain.Rate{}, err
}

// validatePair normalizes the pair and validates the assets: code format, non-emptiness
// and the ban on identical assets outside the whitelist.
func (s *Service) validatePair(base, quote string) (string, string, string, error) {
	base = strings.TrimSpace(base)
	quote = strings.TrimSpace(quote)
	if !validAsset(base) || !validAsset(quote) {
		return "", "", "", fmt.Errorf("%w: pair %q/%q", domain.ErrInvalidRate, base, quote)
	}
	if strings.EqualFold(base, quote) {
		if _, allowed := s.identical[domain.Key(base, quote)]; !allowed {
			return "", "", "", fmt.Errorf("%w: identical pair %q/%q", domain.ErrInvalidRate, base, quote)
		}
	}
	return base, quote, domain.Key(base, quote), nil
}

// validAsset validates the asset code: 2 to 16 characters of Latin letters and digits.
// The restriction protects Redis keys, the guard and logs from values of arbitrary length.
func validAsset(asset string) bool {
	if len(asset) < 2 || len(asset) > 16 {
		return false
	}
	for _, r := range strings.ToUpper(asset) {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// fetchMeta describes the evidence base of the locked rate for audit (6.3).
type fetchMeta struct {
	// sources sets the successful sources in configuration order.
	sources []storage.QuoteSource
	// spread sets the relative spread of the sources as a decimal string.
	spread string
	// guardAdjusted indicates that the rate was replaced by the last known one due to an anomaly.
	guardAdjusted bool
}

// fetchRate collects the rate from the sources without the cache: sources, aggregation, guard,
// and returns metadata for audit (sources, spread, substitution flag).
// Source failure details stay in the WARN log, only the error class is returned.
func (s *Service) fetchRate(ctx context.Context, base, quote, pair string) (domain.Rate, fetchMeta, error) {
	type sourcedRate struct {
		name string
		rate domain.Rate
	}
	collected := make([]sourcedRate, 0, len(s.sources))
	rates := make([]domain.Rate, 0, len(s.sources))
	for _, src := range s.sources {
		rate, err := src.Fetch(ctx, base, quote)
		if err != nil {
			s.warn("source:"+pair, "rate source failed",
				zap.String("pair", pair), zap.String("source", src.Name()), zap.Error(err))
			continue
		}
		collected = append(collected, sourcedRate{name: src.Name(), rate: rate})
		rates = append(rates, rate)
	}
	if len(rates) == 0 {
		return domain.Rate{}, fetchMeta{}, fmt.Errorf("%w: all %d sources failed", domain.ErrNoSource, len(s.sources))
	}
	aggregated, spread, err := s.aggregator.AggregateDetailed(rates)
	if err != nil {
		return domain.Rate{}, fetchMeta{}, err
	}
	checked, ok := s.guard.Check(aggregated)
	if !ok {
		s.warn("anomaly:"+pair, "rate anomaly rejected",
			zap.String("pair", pair),
			zap.String("value", aggregated.Value),
			zap.String("last", checked.Value),
		)
	}
	meta := fetchMeta{
		sources:       make([]storage.QuoteSource, 0, len(collected)),
		spread:        spread,
		guardAdjusted: !ok,
	}
	for _, item := range collected {
		meta.sources = append(meta.sources, storage.QuoteSource{Name: item.name, Value: item.rate.Value})
	}
	return checked, meta, nil
}

// freshHitUsable validates a value from the fresh cache key: the time is not from the future
// and the age is within double the TTL or two minutes.
func (s *Service) freshHitUsable(rate domain.Rate) bool {
	now := s.clock()
	if rate.AsOf.After(now.Add(cacheClockSkew)) {
		return false
	}
	maxAge := max(2*s.cacheTTL, maxFreshHitAge)
	return now.Sub(rate.AsOf) <= maxAge
}

// lastKnownUsable validates a last-known value: the time is not from the future
// and the age is within the last-known TTL plus one minute.
func (s *Service) lastKnownUsable(rate domain.Rate) bool {
	now := s.clock()
	if rate.AsOf.After(now.Add(cacheClockSkew)) {
		return false
	}
	return now.Sub(rate.AsOf) <= s.lastTTL+time.Minute
}

// storeRate writes the rate to the cache (fresh and last-known); cache errors are only logged.
func (s *Service) storeRate(ctx context.Context, pair string, rate domain.Rate) {
	if s.cache == nil {
		return
	}
	if err := s.cache.Set(ctx, rate, s.cacheTTL); err != nil {
		s.warn("cache", "rate cache write failed", zap.String("pair", pair), zap.Error(err))
	}
	if err := s.cache.SetLast(ctx, rate, s.lastTTL); err != nil {
		s.warn("cache", "rate last-known write failed", zap.String("pair", pair), zap.Error(err))
	}
}

// warn writes a WARN no more often than the throttling interval: a dependency failure
// must not create a flood of logs under high load.
func (s *Service) warn(key, message string, fields ...zap.Field) {
	if s.throttle.allow(key) {
		s.log.Warn(message, fields...)
	}
}

// LockQuote locks the rate of the pair for a TTL and saves the quote for audit.
// Payment-level idempotency is provided by the caller: a repeated call
// creates a new quote and by itself has no effect on money.
func (s *Service) LockQuote(ctx context.Context, req LockQuoteRequest) (LockedQuote, error) {
	base, quoteAsset, pair, err := s.validatePair(req.FromAsset, req.ToAsset)
	if err != nil {
		return LockedQuote{}, fmt.Errorf("%w: %w", domain.ErrInvalidLock, err)
	}
	amountAsset := strings.TrimSpace(req.AmountAsset)
	if req.AmountMinor <= 0 || amountAsset == "" ||
		(!strings.EqualFold(amountAsset, base) && !strings.EqualFold(amountAsset, quoteAsset)) {
		return LockedQuote{}, fmt.Errorf("%w: amount asset %q does not match pair", domain.ErrInvalidLock, amountAsset)
	}
	ttl := s.lockTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	if ttl > s.lockMaxTTL {
		return LockedQuote{}, fmt.Errorf("%w: ttl %s exceeds max %s", domain.ErrInvalidLock, ttl, s.lockMaxTTL)
	}
	if s.quotes == nil {
		return LockedQuote{}, fmt.Errorf("%w: quotes storage is not configured", domain.ErrLockStorage)
	}

	// The rate is locked only from fresh source data: the Redis cache is untrusted
	// and is not a source of truth for money locking (security review 6.2).
	rate, meta, err := s.fetchRate(ctx, base, quoteAsset, pair)
	if err != nil {
		return LockedQuote{}, err
	}
	s.storeRate(ctx, pair, rate)

	now := s.clock().UTC()
	quoteID, err := uuid.NewV7()
	if err != nil {
		return LockedQuote{}, fmt.Errorf("quote: generate quote id: %w", err)
	}
	sourcesJSON, err := json.Marshal(meta.sources)
	if err != nil {
		return LockedQuote{}, fmt.Errorf("quote: marshal sources: %w", err)
	}
	asOf := rate.AsOf.UTC()
	ttlSeconds := int(ttl / time.Second)
	amountMinor := req.AmountMinor
	amountAssetName := strings.ToUpper(amountAsset)
	spread := meta.spread
	record := storage.Quote{
		ID:            quoteID.String(),
		BaseAsset:     strings.ToUpper(base),
		QuoteAsset:    strings.ToUpper(quoteAsset),
		Rate:          rate.Value,
		LockedUntil:   now.Add(ttl),
		CreatedAt:     now,
		AsOf:          &asOf,
		TTLSeconds:    &ttlSeconds,
		AmountAsset:   &amountAssetName,
		AmountMinor:   &amountMinor,
		Sources:       string(sourcesJSON),
		Spread:        &spread,
		GuardAdjusted: meta.guardAdjusted,
	}
	if err := s.quotes.Save(ctx, record); err != nil {
		// DB error details stay in the log: only the class is returned.
		s.warn("lock-save:"+pair, "lock quote save failed",
			zap.String("pair", pair), zap.String("quote_id", record.ID), zap.Error(err))
		return LockedQuote{}, fmt.Errorf("%w: quotes storage failure", domain.ErrLockStorage)
	}
	return LockedQuote{
		ID:          record.ID,
		Base:        record.BaseAsset,
		Quote:       record.QuoteAsset,
		Rate:        record.Rate,
		LockedUntil: record.LockedUntil,
		CreatedAt:   record.CreatedAt,
	}, nil
}
