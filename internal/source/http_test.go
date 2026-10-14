package source_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/source"
)

func TestNewHTTPSourceValidation(t *testing.T) {
	t.Parallel()

	_, err := source.NewHTTPSource("http", "", time.Second, fixedClock)
	require.ErrorIs(t, err, domain.ErrInvalidRate)

	_, err = source.NewHTTPSource("http", "http://example.com/rate/{base}", time.Second, fixedClock)
	require.ErrorIs(t, err, domain.ErrInvalidRate)
}

func TestHTTPSourceFetch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/rate/USDT/RUB", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rate":"95.50"}`))
	}))
	defer server.Close()

	httpSource, err := source.NewHTTPSource("http", server.URL+"/rate/{base}/{quote}", time.Second, fixedClock)
	require.NoError(t, err)

	rate, err := httpSource.Fetch(context.Background(), "usdt", "rub")
	require.NoError(t, err)
	assert.Equal(t, "USDT", rate.Base)
	assert.Equal(t, "RUB", rate.Quote)
	assert.Equal(t, "95.50", rate.Value)
	assert.Equal(t, fixedClock(), rate.AsOf)
}

func TestHTTPSourceErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{name: "server error", status: http.StatusInternalServerError, body: `{}`, wantErr: domain.ErrNoSource},
		{name: "bad json", status: http.StatusOK, body: `not-json`, wantErr: domain.ErrInvalidRate},
		{name: "invalid rate", status: http.StatusOK, body: `{"rate":"abc"}`, wantErr: domain.ErrInvalidRate},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			httpSource, err := source.NewHTTPSource("http", server.URL+"/{base}/{quote}", time.Second, fixedClock)
			require.NoError(t, err)

			_, err = httpSource.Fetch(context.Background(), "USDT", "RUB")
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
