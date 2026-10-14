package source_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/source"
)

var fixedClock = func() time.Time {
	return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
}

func TestNewConfigSourceValidation(t *testing.T) {
	t.Parallel()

	invalid := []string{"", "{bad json", `{"USDT/RUB":"95.50"` + ``, `{"USDT":"95.50"}`, `{"USDT/RUB":"abc"}`}
	for _, raw := range invalid {
		_, err := source.NewConfigSource("config", raw, fixedClock)
		require.ErrorIsf(t, err, domain.ErrInvalidRate, "raw=%q", raw)
	}
}

func TestConfigSourceFetch(t *testing.T) {
	t.Parallel()

	configSource, err := source.NewConfigSource("config", `{"USDT/RUB":"95.50"}`, fixedClock)
	require.NoError(t, err)
	assert.Equal(t, "config", configSource.Name())

	rate, err := configSource.Fetch(context.Background(), "usdt", "rub")
	require.NoError(t, err)
	assert.Equal(t, "USDT", rate.Base)
	assert.Equal(t, "RUB", rate.Quote)
	assert.Equal(t, "95.50", rate.Value)
	assert.Equal(t, fixedClock(), rate.AsOf)

	_, err = configSource.Fetch(context.Background(), "BTC", "RUB")
	require.ErrorIs(t, err, domain.ErrNoSource)
}
