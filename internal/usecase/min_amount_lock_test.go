package usecase_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/usecase"
)

func TestLockQuoteEnforcesPairMinimum(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	minimums := map[string]usecase.MinAmount{"USDT/RUB": {Asset: "RUB", Minor: 100_000}}

	t.Run("below the minimum is rejected", func(t *testing.T) {
		t.Parallel()
		store := &fakeQuoteStorage{}
		service := newLockService(t, store, fixedClock(now), usecase.WithMinAmounts(minimums))

		_, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
			FromAsset: "usdt", ToAsset: "rub", AmountAsset: "RUB", AmountMinor: 99_999,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrInvalidLock)
		assert.Contains(t, err.Error(), "minimum")
		_, ok := store.last()
		assert.False(t, ok, "a rejected lock saves nothing")
	})

	t.Run("the other pair side cannot bypass the minimum", func(t *testing.T) {
		t.Parallel()
		store := &fakeQuoteStorage{}
		service := newLockService(t, store, fixedClock(now), usecase.WithMinAmounts(minimums))

		_, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
			FromAsset: "usdt", ToAsset: "rub", AmountAsset: "USDT", AmountMinor: 100_000,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrInvalidLock)
		assert.Contains(t, err.Error(), "must be RUB")
	})

	t.Run("at the minimum is locked and recorded", func(t *testing.T) {
		t.Parallel()
		store := &fakeQuoteStorage{}
		service := newLockService(t, store, fixedClock(now), usecase.WithMinAmounts(minimums))

		_, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
			FromAsset: "usdt", ToAsset: "rub", AmountAsset: "RUB", AmountMinor: 100_000,
		})
		require.NoError(t, err)
		saved, ok := store.last()
		require.True(t, ok)
		require.NotNil(t, saved.MinAmountMinor)
		assert.Equal(t, int64(100_000), *saved.MinAmountMinor)
	})

	t.Run("a pair without a minimum records NULL", func(t *testing.T) {
		t.Parallel()
		store := &fakeQuoteStorage{}
		service := newLockService(t, store, fixedClock(now))

		_, err := service.LockQuote(context.Background(), usecase.LockQuoteRequest{
			FromAsset: "usdt", ToAsset: "rub", AmountAsset: "RUB", AmountMinor: 1,
		})
		require.NoError(t, err)
		saved, ok := store.last()
		require.True(t, ok)
		assert.Nil(t, saved.MinAmountMinor)
	})
}
