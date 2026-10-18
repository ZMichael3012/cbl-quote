// main starts the HTTP and gRPC service of the CBL platform quote:
// aggregation of source rates and protection against anomalous values.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	quotev1 "github.com/cbl/cbl-protos/gen/go/billing/quote/v1"

	"github.com/cbl/cbl-kit/ginmw"
	"github.com/cbl/cbl-kit/grpcmw"
	"github.com/cbl/cbl-kit/storagex"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/source"
	"github.com/cbl/cbl-quote/internal/storage"
	"github.com/cbl/cbl-quote/internal/transport/grpcserver"
	"github.com/cbl/cbl-quote/internal/usecase"
)

// main starts the QuoteService gRPC server and the administrative HTTP server
// and shuts down on SIGINT/SIGTERM.
func main() {
	log, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer func() { _ = log.Sync() }()

	sources, err := buildSources(log)
	if err != nil {
		log.Error("build rate sources failed", zap.Error(err))
		return
	}
	aggregator, err := domain.NewAggregator(getenv("RATE_MAX_SPREAD_PCT", "2"))
	if err != nil {
		log.Error("build aggregator failed", zap.Error(err))
		return
	}
	guard, err := domain.NewAnomalyGuard(getenv("RATE_ANOMALY_PCT", "10"))
	if err != nil {
		log.Error("build anomaly guard failed", zap.Error(err))
		return
	}
	guard.SetResetWindow(durationEnv(log, "RATE_ANOMALY_RESET", 15*time.Minute))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// PG_DSN is optional: GetRate works without a database, LockQuote is unavailable without storage.
	var repo *storage.Repository
	if dsn := os.Getenv("PG_DSN"); dsn != "" {
		db, openErr := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if openErr != nil {
			log.Error("connect postgres failed", zap.Error(openErr))
			return
		}
		migrationsDir := getenv("MIGRATIONS_DIR", "./migrations")
		if migrateErr := storagex.Migrate(ctx, db, migrationsDir); migrateErr != nil {
			log.Error("apply migrations failed", zap.String("dir", migrationsDir), zap.Error(migrateErr))
			return
		}
		repo = storage.NewRepository(db)
	} else {
		log.Warn("PG_DSN is empty: LockQuote will be unavailable")
	}

	// Redis is only the fast path of the rate cache (T-02): its unavailability
	// degrades GetRate to the sources and does not take the service down.
	var cache usecase.RateCache
	if redisOptions := redisOptionsFromEnv(log); redisOptions != nil {
		redisClient := redis.NewClient(redisOptions)
		defer func() { _ = redisClient.Close() }()
		cache = storage.NewRedisCache(redisClient)
		log.Info("rate cache enabled", zap.String("addr", redisOptions.Addr))
	} else {
		log.Warn("REDIS_URL/REDIS_ADDR is empty: rate cache disabled")
	}

	opts := []usecase.Option{
		usecase.WithIdenticalPairs(splitPairs(getenv("RATE_ALLOWED_IDENTICAL_PAIRS", "USDT/USD"))...),
		usecase.WithLockTTL(
			durationEnv(log, "LOCK_QUOTE_TTL", 5*time.Minute),
			durationEnv(log, "LOCK_QUOTE_MAX_TTL", 15*time.Minute),
		),
	}
	// The per-pair minimums are configuration: an invalid value refuses the
	// start instead of silently disabling the limit (fail-fast, 17.10).
	minAmounts, minErr := usecase.ParseMinAmounts(os.Getenv("RATE_MIN_AMOUNTS"))
	if minErr != nil {
		log.Error("invalid RATE_MIN_AMOUNTS", zap.Error(minErr))
		os.Exit(1)
	}
	opts = append(opts, usecase.WithMinAmounts(minAmounts))
	if len(minAmounts) > 0 {
		log.Info("quote minimums configured", zap.Int("pairs", len(minAmounts)))
	} else {
		log.Warn("RATE_MIN_AMOUNTS is empty: pair minimums disabled")
	}
	if cache != nil {
		opts = append(opts, usecase.WithRateCache(cache,
			durationEnv(log, "RATE_CACHE_TTL", 5*time.Second),
			durationEnv(log, "RATE_LAST_TTL", time.Hour),
		))
	}
	if repo != nil {
		opts = append(opts, usecase.WithQuoteStorage(repo))
	}
	quoteSvc := usecase.NewService(sources, aggregator, guard, log, opts...)

	httpAddr := getenv("HTTP_ADDR", ":8070")
	grpcAddr := getenv("GRPC_ADDR", ":9070")
	metricsAddr := getenv("METRICS_ADDR", ":9270")

	router := gin.New()
	router.Use(ginmw.RequestID())
	router.Use(ginmw.AccessLog(log))
	router.Use(ginmw.Recovery(log))
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/readyz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	httpSrv := &http.Server{
		Addr:              httpAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	metricsSrv := &http.Server{Addr: metricsAddr, Handler: metricsHandler(), ReadHeaderTimeout: 5 * time.Second}

	grpcSrv := grpc.NewServer(
		grpc.MaxRecvMsgSize(1<<20),
		grpc.UnaryInterceptor(grpcmw.UnaryServer(log)),
	)
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(grpcSrv, healthSrv)
	quotev1.RegisterQuoteServiceServer(grpcSrv, grpcserver.NewQuote(quoteSvc))
	reflection.Register(grpcSrv)

	grpcListener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Error("grpc listen failed", zap.String("addr", grpcAddr), zap.Error(err))
		return
	}

	go func() {
		log.Info("http server started", zap.String("addr", httpAddr))
		if serveErr := httpSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Error("http server failed", zap.Error(serveErr))
			stop()
		}
	}()
	go func() {
		log.Info("metrics server started", zap.String("addr", metricsAddr))
		if serveErr := metricsSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Error("metrics server failed", zap.Error(serveErr))
			stop()
		}
	}()
	go func() {
		log.Info("grpc server started", zap.String("addr", grpcAddr))
		if serveErr := grpcSrv.Serve(grpcListener); serveErr != nil {
			log.Error("grpc server failed", zap.Error(serveErr))
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutdown started")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if shutdownErr := httpSrv.Shutdown(shutdownCtx); shutdownErr != nil {
		log.Error("http shutdown failed", zap.Error(shutdownErr))
	}
	if shutdownErr := metricsSrv.Shutdown(shutdownCtx); shutdownErr != nil {
		log.Error("metrics shutdown failed", zap.Error(shutdownErr))
	}
	grpcSrv.GracefulStop()
	log.Info("shutdown finished")
}

// buildSources assembles rate sources according to the RATE_SOURCE configuration.
func buildSources(log *zap.Logger) ([]domain.RateSource, error) {
	switch rateSource := getenv("RATE_SOURCE", "config"); rateSource {
	case "config":
		configSource, err := source.NewConfigSource("config", os.Getenv("RATE_CONFIG"), nil)
		if err != nil {
			return nil, err
		}
		return []domain.RateSource{configSource}, nil
	case "http":
		httpSource, err := source.NewHTTPSource(
			getenv("RATE_HTTP_NAME", "http"),
			os.Getenv("RATE_HTTP_URL"),
			3*time.Second,
			nil,
		)
		if err != nil {
			return nil, err
		}
		return []domain.RateSource{httpSource}, nil
	default:
		return nil, fmt.Errorf("quote: unknown RATE_SOURCE %q", rateSource)
	}
}

// splitPairs parses a pair list in the form "USDT/USD,BTC/USD".
func splitPairs(raw string) []string {
	parts := strings.Split(raw, ",")
	pairs := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			pairs = append(pairs, trimmed)
		}
	}
	return pairs
}

// metricsHandler serves a metrics placeholder until task 0.7.
func metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintln(w, "# metrics endpoint placeholder (task 0.7)")
	})
	return mux
}

// redisOptionsFromEnv builds Redis options: REDIS_URL (redis:// or rediss://)
// or REDIS_ADDR with an optional REDIS_PASSWORD. Timeouts are bounded by the RPC
// budget so that an unavailable Redis does not push GetRate beyond 100 ms.
func redisOptionsFromEnv(log *zap.Logger) *redis.Options {
	options := &redis.Options{}
	switch {
	case os.Getenv("REDIS_URL") != "":
		parsed, err := redis.ParseURL(os.Getenv("REDIS_URL"))
		if err != nil {
			log.Warn("invalid REDIS_URL, rate cache disabled", zap.Error(err))
			return nil
		}
		options = parsed
	case os.Getenv("REDIS_ADDR") != "":
		options.Addr = os.Getenv("REDIS_ADDR")
		options.Password = os.Getenv("REDIS_PASSWORD")
	default:
		return nil
	}
	options.DialTimeout = 200 * time.Millisecond
	options.ReadTimeout = 300 * time.Millisecond
	options.WriteTimeout = 300 * time.Millisecond
	options.MaxRetries = 1
	options.ContextTimeoutEnabled = true
	return options
}

// getenv returns the environment variable value or the default value.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// durationEnv reads a duration from an environment variable;
// an invalid value is replaced with the default and a warning.
func durationEnv(log *zap.Logger, key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		log.Warn("invalid duration env, using default",
			zap.String("key", key),
			zap.String("value", raw),
			zap.Duration("default", fallback),
		)
		return fallback
	}
	return parsed
}
