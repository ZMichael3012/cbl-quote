package usecase

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWarnThrottle(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	throttle := newWarnThrottle(30*time.Second, func() time.Time { return now })

	assert.True(t, throttle.allow("cache"), "the first warning passes")
	assert.False(t, throttle.allow("cache"), "a repeat within the interval is suppressed")
	assert.True(t, throttle.allow("source"), "another key is not suppressed")

	now = now.Add(31 * time.Second)
	assert.True(t, throttle.allow("cache"), "after the interval the warning passes again")
}

func TestWarnThrottleNoInterval(t *testing.T) {
	throttle := newWarnThrottle(0, time.Now)
	assert.True(t, throttle.allow("cache"))
	assert.True(t, throttle.allow("cache"), "a zero interval does not suppress")
}
