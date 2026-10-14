// Пакет domain содержит модель курсов, агрегацию источников и защиту
// от аномальных значений сервиса quote.
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

// Ошибки домена quote.
var (
	// ErrInvalidRate сообщает о некорректном значении или паре курса.
	ErrInvalidRate = errors.New("quote: invalid rate")
	// ErrNoSource сообщает об отсутствии доступных источников курса.
	ErrNoSource = errors.New("quote: no rate source available")
	// ErrSpread сообщает о недопустимом расхождении источников.
	ErrSpread = errors.New("quote: source spread too high")
	// ErrInvalidLock сообщает о некорректном запросе фиксации курса.
	ErrInvalidLock = errors.New("quote: invalid lock request")
	// ErrLockStorage сообщает о недоступности хранилища зафиксированных котировок.
	ErrLockStorage = errors.New("quote: lock storage unavailable")
)

// maxDecimals задает предельное число знаков после точки в курсе.
const maxDecimals = 18

// maxIntDigits задает предельное число цифр целой части курса: значения
// из кеша и источников считаются недоверенным входом и ограничиваются по величине.
const maxIntDigits = 20

// Rate описывает курс пары активов десятичной строкой без float.
type Rate struct {
	// Base задает базовый актив.
	Base string
	// Quote задает котируемый актив.
	Quote string
	// Value задает значение курса десятичной строкой.
	Value string
	// AsOf задает время актуальности курса.
	AsOf time.Time
}

// Key возвращает нормализованный ключ пары активов.
func Key(base, quote string) string {
	return strings.ToUpper(strings.TrimSpace(base)) + "/" + strings.ToUpper(strings.TrimSpace(quote))
}

// ParseDecimal разбирает неотрицательную десятичную строку в рациональное число.
// Экспоненциальная запись, дроби и отрицательные значения запрещены.
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

// isDigits сообщает, что строка непуста и состоит только из цифр.
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

// RateSource описывает источник курса.
type RateSource interface {
	// Name задает имя источника.
	Name() string
	// Fetch возвращает курс пары активов.
	Fetch(ctx context.Context, base, quote string) (Rate, error)
}

// Aggregator агрегирует курсы источников медианой и отвергает разброс.
type Aggregator struct {
	maxSpread *big.Rat
}

// NewAggregator создает Aggregator с предельным разбросом источников в процентах.
func NewAggregator(maxSpreadPct string) (*Aggregator, error) {
	pct, err := ParseDecimal(maxSpreadPct)
	if err != nil {
		return nil, fmt.Errorf("%w: max spread %q", ErrInvalidRate, maxSpreadPct)
	}
	return &Aggregator{maxSpread: new(big.Rat).Quo(pct, big.NewRat(100, 1))}, nil
}

// Aggregate возвращает медианный курс; при разбросе источников выше предела - ErrSpread.
// Для четного числа источников берется нижняя медиана.
func (a *Aggregator) Aggregate(rates []Rate) (Rate, error) {
	if len(rates) == 0 {
		return Rate{}, ErrNoSource
	}
	type parsed struct {
		rate  Rate
		value *big.Rat
	}
	items := make([]parsed, 0, len(rates))
	for _, rate := range rates {
		value, err := ParseDecimal(rate.Value)
		if err != nil {
			return Rate{}, fmt.Errorf("%w: source value %q", ErrInvalidRate, rate.Value)
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
		return Rate{}, fmt.Errorf("%w: %s", ErrSpread, spread.FloatString(4))
	}
	return median.rate, nil
}

// guardEntry описывает последнее принятое значение пары.
type guardEntry struct {
	rate Rate
	at   time.Time
}

// AnomalyGuard защищает от скачков курса относительно последнего известного значения.
// Состояние защищено мьютексом: GetRate вызывается конкурентными gRPC-обработчиками.
// Если с последнего принятого значения прошло больше resetWindow, новое значение
// принимается заново: иначе реальный сдвиг рынка отвергался бы бессрочно.
type AnomalyGuard struct {
	mu           sync.Mutex
	maxDeviation *big.Rat
	resetWindow  time.Duration
	last         map[string]guardEntry
	clock        func() time.Time
}

// NewAnomalyGuard создает AnomalyGuard с допустимым отклонением в процентах
// и окном повторной синхронизации 15 минут.
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

// SetClock задает источник времени; используется конфигурацией и тестами.
func (g *AnomalyGuard) SetClock(clock func() time.Time) {
	if clock == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clock = clock
}

// SetResetWindow задает окно повторной синхронизации после сдвига рынка.
func (g *AnomalyGuard) SetResetWindow(window time.Duration) {
	if window <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetWindow = window
}

// Check возвращает проверенное значение курса.
// Первое значение пары принимается всегда; при отклонении выше предела
// возвращается последнее известное значение и ok=false.
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
