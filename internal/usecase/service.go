// Пакет usecase содержит сценарии использования сервиса quote:
// агрегацию курсов источников и защиту денежного контура от аномалий.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/cbl/cbl-quote/internal/domain"
)

// Service агрегирует курсы источников и защищает от аномальных значений.
type Service struct {
	sources    []domain.RateSource
	aggregator *domain.Aggregator
	guard      *domain.AnomalyGuard
	identical  map[string]struct{}
	log        *zap.Logger
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
	}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// GetRate возвращает медианный курс пары с защитой от аномалий.
// При недоступности всех источников возвращает ErrNoSource, при разбросе
// источников выше предела - ErrSpread, при скачке - последний известный курс.
func (s *Service) GetRate(ctx context.Context, base, quote string) (domain.Rate, error) {
	base = strings.TrimSpace(base)
	quote = strings.TrimSpace(quote)
	if base == "" || quote == "" {
		return domain.Rate{}, fmt.Errorf("%w: pair %q/%q", domain.ErrInvalidRate, base, quote)
	}
	if strings.EqualFold(base, quote) {
		if _, allowed := s.identical[domain.Key(base, quote)]; !allowed {
			return domain.Rate{}, fmt.Errorf("%w: identical pair %q/%q", domain.ErrInvalidRate, base, quote)
		}
	}
	rates := make([]domain.Rate, 0, len(s.sources))
	failures := make([]error, 0, len(s.sources))
	for _, src := range s.sources {
		rate, err := src.Fetch(ctx, base, quote)
		if err != nil {
			failures = append(failures, fmt.Errorf("source %s: %w", src.Name(), err))
			continue
		}
		rates = append(rates, rate)
	}
	if len(rates) == 0 {
		s.log.Error("all rate sources failed",
			zap.String("pair", domain.Key(base, quote)),
			zap.Int("sources", len(s.sources)),
		)
		return domain.Rate{}, errors.Join(append([]error{domain.ErrNoSource}, failures...)...)
	}
	aggregated, err := s.aggregator.Aggregate(rates)
	if err != nil {
		return domain.Rate{}, err
	}
	checked, ok := s.guard.Check(aggregated)
	if !ok {
		s.log.Warn("rate anomaly rejected",
			zap.String("pair", domain.Key(base, quote)),
			zap.String("value", aggregated.Value),
			zap.String("last", checked.Value),
		)
	}
	return checked, nil
}
