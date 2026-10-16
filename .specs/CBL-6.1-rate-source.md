# CBL-6.1. Rate source and anomaly protection

| Field | Value |
| :-- | :-- |
| Epic | E6, Quote Service |
| Repository | `cbl-quote` |
| Size | M |
| Wave and parallel group | W1-QUOTE |
| Depends on | 1.7 (CF-1), 1.1 |
| Contract version | `cbl-protos@cf-1` (`billing/quote/v1`) |
| Status | done (locally, awaiting commit) |

## 1. Context and goal

The rate is locked for the lifetime of the invoice: the user sees one amount and one address. The service
stores the rate source, caches it in Redis, and protects the money path from anomalous values.

## 2. Boundaries

In scope: rate providers (interface, config and http sources), aggregation, anomaly
protection, storage of locked quotes (audit), serving `GetRate` over gRPC, and
configuration via env (`RATE_SOURCE`, `RATE_CONFIG`, `RATE_HTTP_URL`).
Out of scope: `LockQuote` and the rate cache in Redis (6.2, KI-quote-02), exchange keys.

## 3. Functional requirements

FR-1. Source interface (internal/domain):

```go
type RateSource interface {
    Name() string
    Fetch(ctx context.Context, base, quote string) (Rate, error)
}
type Rate struct {
    Base, Quote string
    Value       string // decimal string, float forbidden
    AsOf        time.Time
}
```

FR-2. Sources: `config` (static pairs from env `RATE_CONFIG`, for dev and the mock stand) and
`http` (external API, URL from env; enabled by the `RATE_SOURCE` flag). Aggregation of several
sources: the median of the values; when the sources diverge by more than `RATE_MAX_SPREAD_PCT` (default 2)
the rate is considered anomalous.

FR-3. Anomaly protection: if the new value deviates from the last cached one by
more than `RATE_ANOMALY_PCT` (default 10), the value is not published, the last
known value is returned, and a WARN log with both values is written.

FR-4. Validation: a positive value, no more than 18 digits after the decimal point, both assets
non-empty and not equal to each other (except the allowed USDT/USD pair under configuration).

FR-5. The rate value is stored and transmitted as a string; arithmetic on the rate uses only
decimal operations (`math/big.Rat` is allowed internally), float is fully forbidden.

FR-6. Local dependencies: `replace` directives to `../cbl-kit` (middleware, storagex) and
`../cbl-protos` (`billing/quote/v1`) are already wired in 6.1 for the `GetRate` RPC and migrations.

## 4. Contracts

`GetRateResponse{rate, as_of}` and `LockQuoteResponse{quote_id, rate, locked_until}` -
`billing/quote/v1` (frozen by CF-1). Rate is a decimal string.

## 5. Data model and migrations

`migrations/00001_quotes.sql`: the table `quotes (id uuid PK, payment_id text NULL, base_asset
text NOT NULL, quote_asset text NOT NULL, rate text NOT NULL, locked_until timestamptz NOT NULL,
created_at timestamptz NOT NULL DEFAULT now())`, indexes `(payment_id)`,
`(base_asset, quote_asset, created_at)`. The rate cache is in Redis (task 6.2), TTL 5 - 10 seconds.

## 6. State machines

Not applicable.

## 7. Edge cases

Source unavailable (before 6.2 an error when all sources are unavailable, after - the rate from the cache),
malformed external API response, anomalous jump, zero value, unknown asset
pair, no last value during an anomaly (the earliest of the
available sources is returned with a marker), pairs with identical assets outside the whitelist.

## 8. Non-functional requirements

The rate cache (Redis, TTL 5 - 10 seconds) is implemented in 6.2 (KI-quote-02); until then `GetRate`
calls the sources on every invocation. All values are deterministically testable (the clock and the
source are injected via interfaces).

## 9. Test plan

| Level | Scenarios |
| :-- | :-- |
| Smoke | `go build ./...` |
| Unit | median aggregation, spoofed source, anomaly, rate string validation, clock control |
| Integration | testcontainers PostgreSQL + Redis: quote persistence, cache hit without a source call |
| E2E | not applicable before 6.2 |
| Load | not applicable (14.1) |

## 10. Work plan and commit

Commit `CBL-6.1: rate source and anomaly protection`; the spec is in the same commit.

## 11. Open questions

- The choice of the external rate source for prod: decided before task 14.2; until then `config`.

## 12. Review (CBL-001, step 6)

Four independent passes (13 - 14.10.2026): 1 blocker, 3 major, minor; all closed.

| Role | Key findings | Closure |
| :-- | :-- | :-- |
| No context | `AnomalyGuard` without synchronization (race and process crash); coverage 76.09% | mutex + concurrency test; `ConfigSource` and gRPC transport tests; coverage 90.5% |
| Application architecture | the `http` source and selection via `RATE_SOURCE` were missing; the service was not wired in main (it did not serve RPCs); guard state does not survive a restart | `HTTPSource` and env configuration implemented; main wires the service and registers `QuoteService.GetRate`; guard state - trade-off T-QUOTE-01 |
| With context | spread/anomaly defaults were not read; no clock test | env defaults (2%, 10%, window 15m) and tests with time injection |
| Security | after an anomaly `last` was not updated - a real shift was rejected forever; the identical pair had no exception | resynchronization window `RATE_ANOMALY_RESET`; whitelist `RATE_ALLOWED_IDENTICAL_PAIRS` |
