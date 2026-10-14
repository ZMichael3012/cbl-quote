package grpcserver_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	commonv1 "github.com/cbl/cbl-protos/gen/go/billing/common/v1"
	quotev1 "github.com/cbl/cbl-protos/gen/go/billing/quote/v1"

	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/source"
	"github.com/cbl/cbl-quote/internal/storage"
	"github.com/cbl/cbl-quote/internal/transport/grpcserver"
	"github.com/cbl/cbl-quote/internal/usecase"
)

func newQuoteClient(t *testing.T, sources []domain.RateSource, opts ...usecase.Option) quotev1.QuoteServiceClient {
	t.Helper()
	aggregator, err := domain.NewAggregator("2")
	require.NoError(t, err)
	guard, err := domain.NewAnomalyGuard("10")
	require.NoError(t, err)
	service := usecase.NewService(sources, aggregator, guard, zap.NewNop(), opts...)

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(grpcmw.UnaryServer(zap.NewNop())))
	quotev1.RegisterQuoteServiceServer(server, grpcserver.NewQuote(service))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return quotev1.NewQuoteServiceClient(conn)
}

func configSource(t *testing.T, raw string) domain.RateSource {
	t.Helper()
	configSource, err := source.NewConfigSource("config", raw, nil)
	require.NoError(t, err)
	return configSource
}

func TestGetRateViaGrpc(t *testing.T) {
	client := newQuoteClient(t, []domain.RateSource{configSource(t, `{"USDT/RUB":"95.50"}`)})
	ctx := context.Background()

	response, err := client.GetRate(ctx, &quotev1.GetRateRequest{BaseAsset: "USDT", QuoteAsset: "RUB"})
	require.NoError(t, err)
	assert.Equal(t, "95.50", response.GetRate())
	require.NotNil(t, response.GetAsOf())

	_, err = client.GetRate(ctx, &quotev1.GetRateRequest{BaseAsset: "", QuoteAsset: "RUB"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = client.GetRate(ctx, &quotev1.GetRateRequest{BaseAsset: "BTC", QuoteAsset: "RUB"})
	require.Equal(t, codes.Unavailable, status.Code(err))

	_, err = client.GetRate(ctx, &quotev1.GetRateRequest{BaseAsset: "USDT", QuoteAsset: "USDT"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGetRateSpreadRejected(t *testing.T) {
	client := newQuoteClient(t, []domain.RateSource{
		configSource(t, `{"USDT/RUB":"95.00"}`),
		configSourceWithName(t, "second", `{"USDT/RUB":"100.00"}`),
	})

	_, err := client.GetRate(context.Background(), &quotev1.GetRateRequest{BaseAsset: "USDT", QuoteAsset: "RUB"})
	require.Equal(t, codes.Unavailable, status.Code(err))
	assert.Contains(t, status.Convert(err).Message(), "расхождение")
}

func TestGetRateIdenticalPairAllowed(t *testing.T) {
	client := newQuoteClient(t,
		[]domain.RateSource{configSource(t, `{"USDT/USD":"1.00"}`)},
		usecase.WithIdenticalPairs("USDT/USD"),
	)

	response, err := client.GetRate(context.Background(), &quotev1.GetRateRequest{BaseAsset: "USDT", QuoteAsset: "USD"})
	require.NoError(t, err)
	assert.Equal(t, "1.00", response.GetRate())
}

func configSourceWithName(t *testing.T, name, raw string) domain.RateSource {
	t.Helper()
	configSource, err := source.NewConfigSource(name, raw, nil)
	require.NoError(t, err)
	return configSource
}

// fakeQuoteStorage реализует usecase.QuoteStorage для транспортных тестов.
type fakeQuoteStorage struct {
	saved []storage.Quote
}

// Save запоминает котировку.
func (s *fakeQuoteStorage) Save(_ context.Context, quote storage.Quote) error {
	s.saved = append(s.saved, quote)
	return nil
}

// lockQuoteRequest собирает корректный запрос фиксации курса.
func lockQuoteRequest(ttl uint32) *quotev1.LockQuoteRequest {
	return &quotev1.LockQuoteRequest{
		FromAsset:  "USDT",
		ToAsset:    "RUB",
		Amount:     &commonv1.Money{MinorUnits: 100_000, Asset: "RUB", Decimals: 2},
		TtlSeconds: ttl,
	}
}

func TestLockQuoteViaGrpc(t *testing.T) {
	store := &fakeQuoteStorage{}
	client := newQuoteClient(t,
		[]domain.RateSource{configSource(t, `{"USDT/RUB":"95.50"}`)},
		usecase.WithQuoteStorage(store),
		usecase.WithLockTTL(5*time.Minute, 15*time.Minute),
	)

	response, err := client.LockQuote(context.Background(), lockQuoteRequest(60))
	require.NoError(t, err)
	assert.Equal(t, "95.50", response.GetRate())
	require.NotEmpty(t, response.GetQuoteId())
	require.NotNil(t, response.GetLockedUntil())
	require.Len(t, store.saved, 1)
	assert.Equal(t, response.GetQuoteId(), store.saved[0].ID)
	assert.Equal(t, "USDT", store.saved[0].BaseAsset)
}

func TestLockQuoteGrpcErrors(t *testing.T) {
	ctx := context.Background()

	withoutStorage := newQuoteClient(t, []domain.RateSource{configSource(t, `{"USDT/RUB":"95.50"}`)})
	_, err := withoutStorage.LockQuote(ctx, lockQuoteRequest(60))
	require.Equal(t, codes.Unavailable, status.Code(err))

	withStorage := newQuoteClient(t,
		[]domain.RateSource{configSource(t, `{"USDT/RUB":"95.50"}`)},
		usecase.WithQuoteStorage(&fakeQuoteStorage{}),
		usecase.WithLockTTL(5*time.Minute, 15*time.Minute),
	)
	_, err = withStorage.LockQuote(ctx, lockQuoteRequest(3600))
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = withStorage.LockQuote(ctx, &quotev1.LockQuoteRequest{FromAsset: "USDT", ToAsset: "USDT"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	missingAmount := lockQuoteRequest(60)
	missingAmount.Amount = nil
	_, err = withStorage.LockQuote(ctx, missingAmount)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
