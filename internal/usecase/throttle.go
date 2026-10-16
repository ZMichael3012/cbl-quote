package usecase

import (
	"sync"
	"time"
)

// warnThrottle limits the rate of same-kind WARN messages
// so that a dependency failure does not create a flood of logs under high load.
type warnThrottle struct {
	mu       sync.Mutex
	interval time.Duration
	clock    func() time.Time
	last     map[string]time.Time
}

// newWarnThrottle creates a limiter with the given interval; a non-positive
// interval means no limit.
func newWarnThrottle(interval time.Duration, clock func() time.Time) *warnThrottle {
	if clock == nil {
		clock = time.Now
	}
	return &warnThrottle{
		interval: interval,
		clock:    clock,
		last:     make(map[string]time.Time),
	}
}

// allow reports whether a warning with key can be written:
// true no more than once per interval per key.
func (t *warnThrottle) allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	if last, ok := t.last[key]; ok && t.interval > 0 && now.Sub(last) < t.interval {
		return false
	}
	t.last[key] = now
	return true
}
