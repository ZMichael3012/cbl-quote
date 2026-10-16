// Package source contains rate source adapters of the quote service.
package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cbl/cbl-quote/internal/domain"
)

// ConfigSource defines a static rate source from configuration.
type ConfigSource struct {
	name  string
	rates map[string]domain.Rate
	clock func() time.Time
}

// NewConfigSource parses JSON in the form {"USDT/RUB":"95.50","BTC/USDT":"60000.10"}.
func NewConfigSource(name, raw string, clock func() time.Time) (*ConfigSource, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: empty config", domain.ErrInvalidRate)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrInvalidRate, err)
	}
	if clock == nil {
		clock = time.Now
	}
	rates := make(map[string]domain.Rate, len(parsed))
	for pair, value := range parsed {
		base, quote, ok := strings.Cut(pair, "/")
		if !ok {
			return nil, fmt.Errorf("%w: pair %q", domain.ErrInvalidRate, pair)
		}
		if _, err := domain.ParseDecimal(value); err != nil {
			return nil, fmt.Errorf("%w: pair %q value %q", domain.ErrInvalidRate, pair, value)
		}
		rates[domain.Key(base, quote)] = domain.Rate{
			Base:  strings.ToUpper(strings.TrimSpace(base)),
			Quote: strings.ToUpper(strings.TrimSpace(quote)),
			Value: strings.TrimSpace(value),
			AsOf:  clock().UTC(),
		}
	}
	if len(rates) == 0 {
		return nil, fmt.Errorf("%w: no pairs", domain.ErrInvalidRate)
	}
	return &ConfigSource{name: name, rates: rates, clock: clock}, nil
}

// Name returns the source name.
func (s *ConfigSource) Name() string { return s.name }

// Fetch returns the pair rate from configuration.
func (s *ConfigSource) Fetch(_ context.Context, base, quote string) (domain.Rate, error) {
	rate, ok := s.rates[domain.Key(base, quote)]
	if !ok {
		return domain.Rate{}, fmt.Errorf("%w: pair %s", domain.ErrNoSource, domain.Key(base, quote))
	}
	rate.AsOf = s.clock().UTC()
	return rate, nil
}
