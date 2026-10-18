package usecase

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/cbl/cbl-quote/internal/domain"
)

// MinAmount describes the minimum amount of a pair in minor units of an
// explicit asset: the amount asset of a LockQuote request must match it when
// the pair has a configured minimum (17.10).
type MinAmount struct {
	// Asset sets the amount asset the minimum is expressed in.
	Asset string
	// Minor sets the minimum in minor units of Asset.
	Minor int64
}

// ParseMinAmounts parses the RATE_MIN_AMOUNTS configuration: a comma-separated
// list of BASE/QUOTE@ASSET:MINOR entries, where ASSET is one side of the pair
// and MINOR is the minimum in its minor units (a positive integer). The pair
// forms the canonical domain.Key; duplicate pairs and an ASSET outside the pair
// are configuration errors; an empty value disables the minimums.
func ParseMinAmounts(raw string) (map[string]MinAmount, error) {
	if raw == "" {
		return nil, nil
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("quote: RATE_MIN_AMOUNTS is set but empty")
	}
	minimums := make(map[string]MinAmount)
	for _, entry := range strings.Split(trimmed, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("quote: empty RATE_MIN_AMOUNTS entry")
		}
		pairAndAsset, amountRaw, ok := strings.Cut(entry, ":")
		if !ok {
			return nil, fmt.Errorf("quote: RATE_MIN_AMOUNTS entry %q: expected BASE/QUOTE@ASSET:MINOR", entry)
		}
		pair, asset, ok := strings.Cut(pairAndAsset, "@")
		if !ok {
			return nil, fmt.Errorf("quote: RATE_MIN_AMOUNTS entry %q: the amount asset is required", entry)
		}
		base, quoteAsset, pairOK := strings.Cut(pair, "/")
		base, quoteAsset, asset = strings.TrimSpace(base), strings.TrimSpace(quoteAsset), strings.TrimSpace(asset)
		if !pairOK || !validAsset(base) || !validAsset(quoteAsset) || !validAsset(asset) {
			return nil, fmt.Errorf("quote: RATE_MIN_AMOUNTS entry %q: invalid pair or asset", entry)
		}
		if !strings.EqualFold(asset, base) && !strings.EqualFold(asset, quoteAsset) {
			return nil, fmt.Errorf("quote: RATE_MIN_AMOUNTS entry %q: asset must be a side of the pair", entry)
		}
		amount, err := strconv.ParseInt(strings.TrimSpace(amountRaw), 10, 64)
		if err != nil || amount <= 0 {
			return nil, fmt.Errorf("quote: RATE_MIN_AMOUNTS entry %q: minimum must be a positive integer", entry)
		}
		key := domain.Key(base, quoteAsset)
		if _, duplicate := minimums[key]; duplicate {
			return nil, fmt.Errorf("quote: RATE_MIN_AMOUNTS entry %q: duplicate pair %s", entry, key)
		}
		minimums[key] = MinAmount{Asset: strings.ToUpper(asset), Minor: amount}
	}
	return minimums, nil
}
