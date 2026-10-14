package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-quote/internal/domain"
)

func TestParseDecimal(t *testing.T) {
	t.Parallel()

	valid := []string{"1", "95.50", "0.000001", "60000.123456789012345678"}
	for _, value := range valid {
		_, err := domain.ParseDecimal(value)
		require.NoErrorf(t, err, "value=%q", value)
	}

	invalid := []string{"", "-1", "+1", "1e2", "1/2", ".5", "5.", "0", "1.2345678901234567890", "abc", " 1 000",
		"123456789012345678901", "123456789012345678901.5"}
	for _, value := range invalid {
		_, err := domain.ParseDecimal(value)
		require.ErrorIsf(t, err, domain.ErrInvalidRate, "value=%q", value)
	}
}

func TestKey(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "USDT/RUB", domain.Key(" usdt ", "rub"))
}

func TestAggregator(t *testing.T) {
	t.Parallel()

	aggregator, err := domain.NewAggregator("2")
	require.NoError(t, err)

	median, err := aggregator.Aggregate([]domain.Rate{
		{Base: "USDT", Quote: "RUB", Value: "95.00"},
		{Base: "USDT", Quote: "RUB", Value: "95.50"},
		{Base: "USDT", Quote: "RUB", Value: "95.20"},
	})
	require.NoError(t, err)
	assert.Equal(t, "95.20", median.Value)

	even, err := aggregator.Aggregate([]domain.Rate{
		{Base: "USDT", Quote: "RUB", Value: "95.00"},
		{Base: "USDT", Quote: "RUB", Value: "95.40"},
	})
	require.NoError(t, err)
	assert.Equal(t, "95.00", even.Value)

	_, err = aggregator.Aggregate([]domain.Rate{
		{Base: "USDT", Quote: "RUB", Value: "95.00"},
		{Base: "USDT", Quote: "RUB", Value: "99.00"},
	})
	require.ErrorIs(t, err, domain.ErrSpread)

	_, err = aggregator.Aggregate(nil)
	require.ErrorIs(t, err, domain.ErrNoSource)

	_, err = aggregator.Aggregate([]domain.Rate{{Base: "USDT", Quote: "RUB", Value: "bad"}})
	require.ErrorIs(t, err, domain.ErrInvalidRate)

	_, err = domain.NewAggregator("-2")
	require.ErrorIs(t, err, domain.ErrInvalidRate)
}

func TestAnomalyGuard(t *testing.T) {
	t.Parallel()

	guard, err := domain.NewAnomalyGuard("10")
	require.NoError(t, err)

	first := domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.00"}
	checked, ok := guard.Check(first)
	require.True(t, ok)
	assert.Equal(t, "95.00", checked.Value)

	within := domain.Rate{Base: "USDT", Quote: "RUB", Value: "96.00"}
	checked, ok = guard.Check(within)
	require.True(t, ok)
	assert.Equal(t, "96.00", checked.Value)

	jump := domain.Rate{Base: "USDT", Quote: "RUB", Value: "120.00"}
	checked, ok = guard.Check(jump)
	require.False(t, ok)
	assert.Equal(t, "96.00", checked.Value)

	invalid := domain.Rate{Base: "USDT", Quote: "RUB", Value: "bad"}
	checked, ok = guard.Check(invalid)
	require.False(t, ok)
	assert.Equal(t, "96.00", checked.Value)

	other := domain.Rate{Base: "BTC", Quote: "USDT", Value: "60000"}
	checked, ok = guard.Check(other)
	require.True(t, ok)
	assert.Equal(t, "60000", checked.Value)

	_, err = domain.NewAnomalyGuard("x")
	require.ErrorIs(t, err, domain.ErrInvalidRate)
}

func TestAnomalyGuardResetWindow(t *testing.T) {
	t.Parallel()

	guard, err := domain.NewAnomalyGuard("10")
	require.NoError(t, err)

	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	guard.SetClock(func() time.Time { return now })
	guard.SetResetWindow(15 * time.Minute)

	base := domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.00"}
	_, ok := guard.Check(base)
	require.True(t, ok)

	jump := domain.Rate{Base: "USDT", Quote: "RUB", Value: "120.00"}
	checked, ok := guard.Check(jump)
	require.False(t, ok)
	assert.Equal(t, "95.00", checked.Value)

	now = now.Add(16 * time.Minute)
	checked, ok = guard.Check(jump)
	require.True(t, ok)
	assert.Equal(t, "120.00", checked.Value)
}
