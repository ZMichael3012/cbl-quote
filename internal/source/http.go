package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cbl/cbl-quote/internal/domain"
)

// maxResponseBytes задает предельный размер ответа внешнего источника.
const maxResponseBytes = 1 << 20

// HTTPSource получает курс пары от внешнего HTTP-источника.
// URL задается шаблоном с подстановками {base} и {quote}.
type HTTPSource struct {
	name        string
	urlTemplate string
	client      *http.Client
	clock       func() time.Time
}

// NewHTTPSource создает HTTPSource с таймаутом запроса.
func NewHTTPSource(name, urlTemplate string, timeout time.Duration, clock func() time.Time) (*HTTPSource, error) {
	if strings.TrimSpace(urlTemplate) == "" {
		return nil, fmt.Errorf("%w: empty url template", domain.ErrInvalidRate)
	}
	if !strings.Contains(urlTemplate, "{base}") || !strings.Contains(urlTemplate, "{quote}") {
		return nil, fmt.Errorf("%w: url template must contain {base} and {quote}", domain.ErrInvalidRate)
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if clock == nil {
		clock = time.Now
	}
	return &HTTPSource{
		name:        name,
		urlTemplate: urlTemplate,
		client:      &http.Client{Timeout: timeout},
		clock:       clock,
	}, nil
}

// Name возвращает имя источника.
func (s *HTTPSource) Name() string { return s.name }

// Fetch запрашивает курс пары и ожидает JSON {"rate": "<десятичная строка>"}.
func (s *HTTPSource) Fetch(ctx context.Context, base, quote string) (domain.Rate, error) {
	requestURL := strings.NewReplacer(
		"{base}", url.QueryEscape(strings.ToUpper(strings.TrimSpace(base))),
		"{quote}", url.QueryEscape(strings.ToUpper(strings.TrimSpace(quote))),
	).Replace(s.urlTemplate)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return domain.Rate{}, fmt.Errorf("%w: build request: %v", domain.ErrInvalidRate, err)
	}
	response, err := s.client.Do(request)
	if err != nil {
		// Полный *url.Error содержит URL с query, где у некоторых провайдеров
		// лежит api_key: наружу и в логи отдается только внутренняя причина.
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			err = urlErr.Err
		}
		return domain.Rate{}, fmt.Errorf("%w: request failed: %v", domain.ErrNoSource, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return domain.Rate{}, fmt.Errorf("%w: http status %d", domain.ErrNoSource, response.StatusCode)
	}

	var payload struct {
		Rate string `json:"rate"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&payload); err != nil {
		return domain.Rate{}, fmt.Errorf("%w: decode response: %v", domain.ErrInvalidRate, err)
	}
	if _, err := domain.ParseDecimal(payload.Rate); err != nil {
		return domain.Rate{}, err
	}
	return domain.Rate{
		Base:  strings.ToUpper(strings.TrimSpace(base)),
		Quote: strings.ToUpper(strings.TrimSpace(quote)),
		Value: strings.TrimSpace(payload.Rate),
		AsOf:  s.clock().UTC(),
	}, nil
}
