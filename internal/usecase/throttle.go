package usecase

import (
	"sync"
	"time"
)

// warnThrottle ограничивает частоту однотипных WARN-сообщений,
// чтобы отказ зависимости не создавал поток логов при высокой нагрузке.
type warnThrottle struct {
	mu       sync.Mutex
	interval time.Duration
	clock    func() time.Time
	last     map[string]time.Time
}

// newWarnThrottle создает ограничитель с заданным интервалом; неположительный
// интервал означает отсутствие ограничения.
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

// allow сообщает, можно ли записать предупреждение с ключом key:
// true не чаще одного раза в интервал на ключ.
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
