// Пакет usecase: сценарии указаны в doc.go.
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

// Значения по умолчанию для кеша и фиксации курса,
// а также границы доверия к значениям из Redis.
const (
	defaultCacheTTL   = 5 * time.Second
	defaultLastTTL    = time.Hour
	defaultLockTTL    = 5 * time.Minute
	defaultLockMaxTTL = 15 * time.Minute
	// cacheClockSkew задает допустимое опережение часов у значения из кеша.
	cacheClockSkew = time.Minute
	// maxFreshHitAge задает верхнюю границу допустимого возраста свежего значения.
	maxFreshHitAge = 2 * time.Minute
	// warnInterval задает минимальный интервал между однотипными WARN.
	warnInterval = 30 * time.Second
)

// RateCache кеширует курсы; Redis - только быстрый путь, его ошибки не ломают GetRate.
type RateCache interface {
	// Get возвращает свежий курс пары; miss - ok=false без ошибки.
	Get(ctx context.Context, pair string) (domain.Rate, bool, error)
	// Set сохраняет свежий курс пары с TTL.
	Set(ctx context.Context, rate domain.Rate, ttl time.Duration) error
	// GetLast возвращает последний известный курс пары для деградации.
	GetLast(ctx context.Context, pair string) (domain.Rate, bool, error)
	// SetLast сохраняет последний известный курс пары с длинным TTL.
	SetLast(ctx context.Context, rate domain.Rate, ttl time.Duration) error
}

// QuoteStorage сохраняет зафиксированные котировки для аудита.
type QuoteStorage interface {
	// Save сохраняет котировку.
	Save(ctx context.Context, quote storage.Quote) error
}

// LockQuoteRequest описывает запрос фиксации курса.
type LockQuoteRequest struct {
	// FromAsset задает базовый актив.
	FromAsset string
	// ToAsset задает котируемый актив.
	ToAsset string
	// AmountAsset задает актив суммы, для которой фиксируется курс.
	AmountAsset string
	// AmountMinor задает сумму в минимальных единицах.
	AmountMinor int64
	// TTLSeconds задает срок фиксации; 0 - значение по умолчанию.
	TTLSeconds uint32
}

// LockedQuote описывает зафиксированную котировку.
type LockedQuote struct {
	// ID задает идентификатор котировки.
	ID string
	// Base задает базовый актив.
	Base string
	// Quote задает котируемый актив.
	Quote string
	// Rate задает зафиксированный курс десятичной строкой.
	Rate string
	// LockedUntil задает срок действия фиксации.
	LockedUntil time.Time
	// CreatedAt задает время создания котировки.
	CreatedAt time.Time
}

// Service агрегирует курсы источников и защищает от аномальных значений.
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

// Option настраивает Service.
type Option func(*Service)

// WithIdenticalPairs разрешает пары с совпадающими активами, например USDT/USD.
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

// WithRateCache подключает кеш курсов; неположительные TTL заменяются значениями по умолчанию.
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

// WithQuoteStorage подключает хранилище зафиксированных котировок.
func WithQuoteStorage(quotes QuoteStorage) Option {
	return func(s *Service) { s.quotes = quotes }
}

// WithLockTTL задает срок фиксации по умолчанию и верхний предел;
// значения меньше секунды и некорректные заменяются значением по умолчанию:
// TTL аудита хранится целыми секундами, и нулевой ttl_seconds запрещен CHECK-ом.
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

// WithClock задает источник времени; используется тестами.
func WithClock(clock func() time.Time) Option {
	return func(s *Service) {
		if clock != nil {
			s.clock = clock
		}
	}
}

// NewService создает Service.
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

// GetRate возвращает медианный курс пары с кешированием и защитой от аномалий.
// Значения из Redis считаются недоверенным входом: проверяются возраст и формат.
// При недоступности всех источников возвращается последний известный курс из кеша
// в пределах lastTTL, при его отсутствии - ErrNoSource; при разбросе - ErrSpread.
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

// validatePair нормализует пару и проверяет активы: формат кода, непустоту
// и запрет совпадающих активов вне whitelist.
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

// validAsset проверяет код актива: 2 - 16 символов латиницы и цифр.
// Ограничение защищает ключи Redis, guard и логи от значений произвольной длины.
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

// fetchMeta описывает доказательную базу зафиксированного курса для аудита (6.3).
type fetchMeta struct {
	// sources задает успешные источники в порядке конфигурации.
	sources []storage.QuoteSource
	// spread задает относительный разброс источников десятичной строкой.
	spread string
	// guardAdjusted указывает, что курс заменен последним известным из-за аномалии.
	guardAdjusted bool
}

// fetchRate собирает курс из источников без кеша: источники, агрегация, guard,
// и возвращает метаданные для аудита (источники, разброс, признак подмены).
// Детали отказов источников остаются в WARN-логе, наружу идет только класс ошибки.
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

// freshHitUsable проверяет значение из свежего ключа кеша: время не из будущего
// и возраст в пределах двойного TTL или двух минут.
func (s *Service) freshHitUsable(rate domain.Rate) bool {
	now := s.clock()
	if rate.AsOf.After(now.Add(cacheClockSkew)) {
		return false
	}
	maxAge := max(2*s.cacheTTL, maxFreshHitAge)
	return now.Sub(rate.AsOf) <= maxAge
}

// lastKnownUsable проверяет значение last-known: время не из будущего
// и возраст в пределах TTL last-known плюс минута.
func (s *Service) lastKnownUsable(rate domain.Rate) bool {
	now := s.clock()
	if rate.AsOf.After(now.Add(cacheClockSkew)) {
		return false
	}
	return now.Sub(rate.AsOf) <= s.lastTTL+time.Minute
}

// storeRate записывает курс в кеш (свежий и last-known); ошибки кеша только логируются.
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

// warn пишет WARN не чаще интервала троттлинга: отказ зависимости не должен
// создавать поток логов при высокой нагрузке.
func (s *Service) warn(key, message string, fields ...zap.Field) {
	if s.throttle.allow(key) {
		s.log.Warn(message, fields...)
	}
}

// LockQuote фиксирует курс пары на срок TTL и сохраняет котировку для аудита.
// Идемпотентность уровня платежа обеспечивает вызывающая сторона: повторный вызов
// создает новую котировку, эффекта на деньги сам по себе не имеет.
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

	// Курс фиксируется только по свежим данным источников: кеш Redis недоверенный
	// и не является источником истины для денежной фиксации (security review 6.2).
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
		// Детали ошибки БД остаются в логе: наружу уходит только класс.
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
