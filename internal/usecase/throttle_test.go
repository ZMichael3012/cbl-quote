package usecase

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWarnThrottle(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	throttle := newWarnThrottle(30*time.Second, func() time.Time { return now })

	assert.True(t, throttle.allow("cache"), "первое предупреждение проходит")
	assert.False(t, throttle.allow("cache"), "повтор в пределах интервала подавляется")
	assert.True(t, throttle.allow("source"), "другой ключ не подавляется")

	now = now.Add(31 * time.Second)
	assert.True(t, throttle.allow("cache"), "после интервала предупреждение проходит снова")
}

func TestWarnThrottleNoInterval(t *testing.T) {
	throttle := newWarnThrottle(0, time.Now)
	assert.True(t, throttle.allow("cache"))
	assert.True(t, throttle.allow("cache"), "нулевой интервал не подавляет")
}
