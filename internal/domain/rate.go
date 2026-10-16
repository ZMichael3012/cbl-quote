// Package domain contains the rate model, source aggregation and protection
// against anomalous values of the quote service.
package domain

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"
)

// Domain errors of the quote service.
var (
	// ErrInvalidRate reports an invalid rate value or pair.
	ErrInvalidRate = errors.New("quote: invalid rate")
	// ErrNoSource reports that no rate sources are available.
	ErrNoSource = errors.New("quote: no rate source available")
	// ErrSpread reports an unacceptable divergence between sources.
	ErrSpread = errors.New("quote: source spread too high")
	// ErrInvalidLock reports an invalid rate lock request.
	ErrInvalidLock = errors.New("quote: invalid lock request")
	// ErrLockStorage reports that the locked quote storage is unavailable.
	ErrLockStorage = errors.New("quote: lock storage unavailable")
)

// maxDecimals sets the maximum number of fractional digits in a rate.
const maxDecimals = 18

// maxIntDigits sets the maximum number of integer digits of a rate: values
// from the cache and sources are treated as untrusted input and bounded in magnitude.
const maxIntDigits = 20

// Rate describes an asset pair rate as a decimal string without float.
type Rate struct {
	// Base sets the base asset.
	Base string
	// Quote sets the quote asset.
	Quote string
	// Value sets the rate value as a decimal string.
	Value string
	// AsOf sets the rate as-of time.
	AsOf time.Time
}

// Key returns the normalized key of an asset pair.
func Key(base, quote string) string {
	return strings.ToUpper(strings.TrimSpace(base)) + "/" + strings.ToUpper(strings.TrimSpace(quote))
}

// ParseDecimal parses a non-negative decimal string into a rational number.
// Exponential notation, fractions and negative values are forbidden.
func ParseDecimal(value string) (*big.Rat, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty value", ErrInvalidRate)
	}
	if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "+") {
		return nil, fmt.Errorf("%w: signed value %q", ErrInvalidRate, value)
	}
	intPart, fracPart, hasFrac := strings.Cut(trimmed, ".")
	if !isDigits(intPart) || len(intPart) > maxIntDigits {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRate, value)
	}
	if hasFrac && (!isDigits(fracPart) || len(fracPart) > maxDecimals) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRate, value)
	}
	rat, ok := new(big.Rat).SetString(trimmed)
	if !ok || rat.Sign() <= 0 {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRate, value)
	}
	return rat, nil
}

// isDigits reports whether the string is non-empty and consists only of digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// RateSource describes a rate source.
type RateSource interface {
	// Name sets the source name.
	Name() string
	// Fetch returns the rate of an asset pair.
	Fetch(ctx context.Context, base, quote string) (Rate, error)
}

// Aggregator aggregates source rates by median and rejects excessive spread.
type Aggregator struct {
	maxSpread *big.Rat
}

// NewAggregator creates an Aggregator with the maximum source spread in percent.
func NewAggregator(maxSpreadPct string) (*Aggregator, error) {
	pct, err := ParseDecimal(maxSpreadPct)
	if err != nil {
		return nil, fmt.Errorf("%w: max spread %q", ErrInvalidRate, maxSpreadPct)
	}
	return &Aggregator{maxSpread: new(big.Rat).Quo(pct, big.NewRat(100, 1))}, nil
}

// Aggregate returns the median rate; it returns ErrSpread when the source spread
// exceeds the limit. For an even number of sources the lower median is used.
func (a *Aggregator) Aggregate(rates []Rate) (Rate, error) {
	rate, _, err := a.AggregateDetailed(rates)
	return rate, err
}

// AggregateDetailed returns the median rate and the relative source spread
// (max - min) / median as a decimal string with six digits: the spread is needed
// by the locked quote audit (6.3).
func (a *Aggregator) AggregateDetailed(rates []Rate) (Rate, string, error) {
	if len(rates) == 0 {
		return Rate{}, "", ErrNoSource
	}
	type parsed struct {
		rate  Rate
		value *big.Rat
	}
	items := make([]parsed, 0, len(rates))
	for _, rate := range rates {
		value, err := ParseDecimal(rate.Value)
		if err != nil {
			return Rate{}, "", fmt.Errorf("%w: source value %q", ErrInvalidRate, rate.Value)
		}
		items = append(items, parsed{rate: rate, value: value})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].value.Cmp(items[j].value) < 0 })

	min := items[0].value
	max := items[len(items)-1].value
	median := items[(len(items)-1)/2]

	spread := new(big.Rat).Sub(max, min)
	spread.Quo(spread, median.value)
	if spread.Cmp(a.maxSpread) > 0 {
		return Rate{}, "", fmt.Errorf("%w: %s", ErrSpread, spread.FloatString(4))
	}
	return median.rate, spread.FloatString(6), nil
}

// guardEntry describes the last accepted value of a pair.
type guardEntry struct {
	rate Rate
	at   time.Time
}

// AnomalyGuard protects against rate jumps relative to the last known value.
// The state is protected by a mutex: GetRate is called by concurrent gRPC handlers.
// If more than resetWindow has passed since the last accepted value, a new value
// is accepted again: otherwise a real market shift would be rejected indefinitely.
type AnomalyGuard struct {
	mu           sync.Mutex
	maxDeviation *big.Rat
	resetWindow  time.Duration
	last         map[string]guardEntry
	clock        func() time.Time
}

// NewAnomalyGuard creates an AnomalyGuard with the allowed deviation in percent
// and a 15-minute resynchronization window.
func NewAnomalyGuard(maxDeviationPct string) (*AnomalyGuard, error) {
	pct, err := ParseDecimal(maxDeviationPct)
	if err != nil {
		return nil, fmt.Errorf("%w: max deviation %q", ErrInvalidRate, maxDeviationPct)
	}
	return &AnomalyGuard{
		maxDeviation: new(big.Rat).Quo(pct, big.NewRat(100, 1)),
		resetWindow:  15 * time.Minute,
		last:         make(map[string]guardEntry),
		clock:        time.Now,
	}, nil
}

// SetClock sets the time source; it is used by configuration and tests.
func (g *AnomalyGuard) SetClock(clock func() time.Time) {
	if clock == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clock = clock
}

// SetResetWindow sets the resynchronization window after a market shift.
func (g *AnomalyGuard) SetResetWindow(window time.Duration) {
	if window <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetWindow = window
}

// Check returns the checked rate value.
// The first value of a pair is always accepted; when the deviation exceeds the limit,
// the last known value is returned with ok=false.
func (g *AnomalyGuard) Check(rate Rate) (Rate, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	key := Key(rate.Base, rate.Quote)
	previous, known := g.last[key]
	if !known || now.Sub(previous.at) > g.resetWindow {
		g.last[key] = guardEntry{rate: rate, at: now}
		return rate, true
	}
	value, err := ParseDecimal(rate.Value)
	if err != nil {
		return previous.rate, false
	}
	previousValue, err := ParseDecimal(previous.rate.Value)
	if err != nil {
		g.last[key] = guardEntry{rate: rate, at: now}
		return rate, true
	}
	deviation := new(big.Rat).Sub(value, previousValue)
	deviation.Abs(deviation)
	deviation.Quo(deviation, previousValue)
	if deviation.Cmp(g.maxDeviation) > 0 {
		return previous.rate, false
	}
	g.last[key] = guardEntry{rate: rate, at: now}
	return rate, true
}
