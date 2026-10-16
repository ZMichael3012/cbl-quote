package usecase_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/usecase"
)

type fakeSource struct {
	name string
	rate domain.Rate
	err  error
}

func (f *fakeSource) Name() string { return f.name }

// Fetch returns a preset rate or an error.
func (f *fakeSource) Fetch(_ context.Context, _, _ string) (domain.Rate, error) {
	if f.err != nil {
		return domain.Rate{}, f.err
	}
	return f.rate, nil
}

func newTestService(t *testing.T, sources ...domain.RateSource) *usecase.Service {
	t.Helper()
	aggregator, err := domain.NewAggregator("2")
	require.NoError(t, err)
	guard, err := domain.NewAnomalyGuard("10")
	require.NoError(t, err)
	return usecase.NewService(sources, aggregator, guard, nil)
}

func TestGetRateMedian(t *testing.T) {
	t.Parallel()

	service := newTestService(t,
		&fakeSource{name: "a", rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.00", AsOf: time.Now()}},
		&fakeSource{name: "b", rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.50", AsOf: time.Now()}},
		&fakeSource{name: "c", rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.20", AsOf: time.Now()}},
	)

	rate, err := service.GetRate(context.Background(), "usdt", "rub")
	require.NoError(t, err)
	assert.Equal(t, "95.20", rate.Value)
}

func TestGetRateAllSourcesFail(t *testing.T) {
	t.Parallel()

	service := newTestService(t,
		&fakeSource{name: "a", err: errors.New("timeout")},
		&fakeSource{name: "b", err: errors.New("500")},
	)

	_, err := service.GetRate(context.Background(), "USDT", "RUB")
	require.ErrorIs(t, err, domain.ErrNoSource)
}

func TestGetRateSpreadRejected(t *testing.T) {
	t.Parallel()

	service := newTestService(t,
		&fakeSource{name: "a", rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.00", AsOf: time.Now()}},
		&fakeSource{name: "b", rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "100.00", AsOf: time.Now()}},
	)

	_, err := service.GetRate(context.Background(), "USDT", "RUB")
	require.ErrorIs(t, err, domain.ErrSpread)
}

type mutableSource struct {
	mu   sync.Mutex
	name string
	rate domain.Rate
}

func (m *mutableSource) Name() string { return m.name }

// Fetch returns the current rate value of the source.
func (m *mutableSource) Fetch(_ context.Context, _, _ string) (domain.Rate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rate, nil
}

func (m *mutableSource) set(rate domain.Rate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rate = rate
}

func TestGetRateAnomalyReturnsLastKnown(t *testing.T) {
	t.Parallel()

	source := &mutableSource{name: "a", rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.00", AsOf: time.Now()}}
	service := newTestService(t, source)

	first, err := service.GetRate(context.Background(), "USDT", "RUB")
	require.NoError(t, err)
	require.Equal(t, "95.00", first.Value)

	source.set(domain.Rate{Base: "USDT", Quote: "RUB", Value: "120.00", AsOf: time.Now()})
	second, err := service.GetRate(context.Background(), "USDT", "RUB")
	require.NoError(t, err)
	assert.Equal(t, "95.00", second.Value)

	source.set(domain.Rate{Base: "USDT", Quote: "RUB", Value: "96.00", AsOf: time.Now()})
	third, err := service.GetRate(context.Background(), "USDT", "RUB")
	require.NoError(t, err)
	assert.Equal(t, "96.00", third.Value)
}

func TestGetRateConcurrentSafe(t *testing.T) {
	source := &mutableSource{name: "a", rate: domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.00", AsOf: time.Now()}}
	service := newTestService(t, source)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if n%10 == 0 {
				source.set(domain.Rate{Base: "USDT", Quote: "RUB", Value: "95.50", AsOf: time.Now()})
			}
			_, _ = service.GetRate(context.Background(), "USDT", "RUB")
		}(i)
	}
	wg.Wait()
}

func TestGetRateInvalidPair(t *testing.T) {
	t.Parallel()

	service := newTestService(t)

	_, err := service.GetRate(context.Background(), "", "RUB")
	require.ErrorIs(t, err, domain.ErrInvalidRate)

	_, err = service.GetRate(context.Background(), "USDT", "usdt")
	require.ErrorIs(t, err, domain.ErrInvalidRate)
}
