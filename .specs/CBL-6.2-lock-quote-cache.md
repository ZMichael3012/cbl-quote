# CBL-6.2. LockQuote, GetRate, and the rate cache in Redis

| Field | Value |
| :-- | :-- |
| Epic | E6, Quote Service |
| Repository | `cbl-quote` |
| Size | M |
| Wave and parallel group | W1-QUOTE |
| Depends on | 6.1, CF-1 |
| Contract version | `cbl-protos@cf-1` (`billing/quote/v1`) |
| Status | done (review closed, commit CBL-6.2) |

## 1. Context and goal

A payment locks the rate for the lifetime of the invoice (`CBL-000`, payment acceptance flow: Payment calls
`LockQuote` and receives `quote_id`, `rate`, `locked_until`). The task adds the `LockQuote` RPC, the
rate cache in Redis with a short TTL (T-QUOTE-02, KI-quote-02), and `GetRate` degradation to the
last known rate when the sources are unavailable (edge case 6.1). `CBL-003` budgets:
`LockQuote` 300 ms, `GetRate` 100 ms.

## 2. Boundaries

In scope: `LockQuote` (valuation, TTL limits, quote persistence in PostgreSQL for audit),
the Redis rate cache (fresh TTL and last-known for degradation), degradation when Redis
and the sources are unavailable, the e2e quote scenario (KI-quote-04).
Out of scope: audit finalization and linking with the payment (6.3), the external provider
source (14.2), k6 (14.1).

## 3. Functional requirements

FR-1. `GetRate` with the cache:
- the fresh rate key `rate:{BASE}:{QUOTE}` (CBL-003 convention), TTL `RATE_CACHE_TTL` (5s);
- a hit returns the cached rate with its `AsOf` without calling the sources; Redis values
  are treated as untrusted input: the format (ParseDecimal, at most 20 digits of the integer
  part), the age (not older than 2×TTL or 2 minutes), and the absence of a time from the future
  (skew 1 minute) are checked; a rejected value is logged and ignored as a miss;
- a miss: sources -> aggregation -> guard -> write of both keys (best effort);
- when all sources are unavailable, the last-known rate (`rate:last:{BASE}:{QUOTE}`,
  TTL `RATE_LAST_TTL` default 1h) is returned within its TTL with a WARN log; if absent -
  `ErrNoSource`; source failure details stay in the log, only the error class goes
  out (the provider URL with api_key does not reach the status);
- Redis errors degrade `GetRate` to operation without the cache (WARN with 30 s throttling), the
  service does not crash: Redis is only a fast path (T-02, T-11); client timeouts are bounded
  (dial 200 ms, read/write 300 ms, no hidden retries within the request context).

FR-2. `LockQuote`:
- validation: `from_asset`/`to_asset` non-empty, matching the asset code format (2 - 16
  Latin letters and digits), not identical (except the 6.1 whitelist); pair and amount violations return
  `ErrInvalidLock`;
- `ttl_seconds`: 0 means the default `LOCK_QUOTE_TTL` (default 300s), greater than
  `LOCK_QUOTE_MAX_TTL` (default 900s) - `ErrInvalidLock`; exactly the limit is allowed;
- `amount`: asset is filled and matches `from_asset` or `to_asset` (case-insensitive),
  `minor_units` > 0; the amount is not persisted (audit columns - 6.3) and does not appear
  in error texts;
- the rate is locked only from fresh source data: the Redis cache is untrusted and is not
  used for monetary locking (security review 6.2); when the sources are unavailable
  the caller receives `ErrNoSource` (an invoice is not created at a stale rate);
- a quote is created (`quote_id` UUIDv7, `locked_until = now + ttl`, `created_at = now`)
  and persisted in PostgreSQL (`quotes`); a write error - `ErrLockStorage` (unavailable,
  the caller retries), DB details stay in the log, the quote is not considered locked.

FR-3. Storage: no new migrations; the 6.1 table is used. The cache implementation is
`internal/storage/RedisCache` (go-redis/v9, keys `rate:{BASE}:{QUOTE}` and
`rate:last:{BASE}:{QUOTE}`). The cache value is the decimal rate string plus the `AsOf` time
(RFC 3339 nano); a corrupted value is read as a miss.

FR-4. Configuration (env): `REDIS_URL` (redis:// or rediss://, takes priority) or
`REDIS_ADDR` + `REDIS_PASSWORD` (empty - cache disabled, warning), `RATE_CACHE_TTL`,
`RATE_LAST_TTL`, `LOCK_QUOTE_TTL`, `LOCK_QUOTE_MAX_TTL`; an invalid duration value falls back
to the default with a WARN. `PG_DSN` unchanged: empty - `LockQuote` returns `ErrLockStorage`.

FR-5. gRPC error mapping: `ErrInvalidLock` -> invalid_argument `invalid_lock_request`;
`ErrLockStorage` -> unavailable `quote_storage_unavailable`; `context.DeadlineExceeded` ->
unavailable `rate_timeout`; `context.Canceled` -> unavailable `request_canceled`;
the existing 6.1 mappings are preserved.

FR-6. Observability: WARN with 30 second throttling per key (Redis failure, source failure,
degradation); amounts and values do not enter the logs beyond the format already adopted in 6.1.

## 4. Contracts

- `QuoteService.LockQuote(LockQuoteRequest{from_asset, to_asset, amount: Money, ttl_seconds})
  -> LockQuoteResponse{quote_id, rate, locked_until}` (`cbl-protos@cf-1`, unchanged).
- `QuoteService.GetRate` with no contract changes; caching is added.
- Stores: PostgreSQL `quotes` (write), Redis `rate:{BASE}:{QUOTE}` and
  `rate:last:{BASE}:{QUOTE}` (TTL).

## 5. Data model and migrations

No new migrations. Write to `quotes`: `id`, `base_asset`, `quote_asset`, `rate`,
`locked_until`, `created_at`; `payment_id` remains NULL until linking in 6.3/4.2.

## 6. State machines and rules

Not applicable; a quote is immutable and lives until `locked_until` (freshness checking is
the consumer's responsibility).

## 7. Edge cases

Identical assets (whitelist), assets of invalid format or length, TTL 0, exactly the limit
and above the limit, mismatch of the amount asset with the pair, zero and negative amount, nil Amount,
Redis unavailability (GetRate degradation, LockQuote unaffected), all sources
unavailable (last-known for GetRate, ErrNoSource for LockQuote), corrupted, stale,
or future cache value (rejected as a miss), PostgreSQL write error,
concurrent `LockQuote` (independent writes), time zone (UTC).

## 8. Non-functional requirements

- `CBL-003` budgets are enforced by the handler deadline: `GetRate` 90 ms, `LockQuote`
  250 ms; exceeding them - `rate_timeout` (unavailable) instead of waiting 3 seconds for the source.
- Degradation without process errors: Redis and the sources may be unavailable.
- Money: the rate is only a decimal string, float is forbidden; amounts remain int64 unchanged.
- Unit+integration coverage at least 80%; Redis scenarios run on real Redis
  (testcontainers, CBL-001).

## 9. Test plan

| Level | Scenarios |
| :-- | :-- |
| Smoke | `go build ./...`, compose up: healthz, migrations |
| Unit | cache hit without a source call; rejection of future and stale values; last-known fallback and its read error; best-effort cache write on a Redis error; LockQuote validations (TTL 0/limit/above, amount asset, zero amount, identical pair, whitelist, asset format); UUIDv7; clock; storage error; WARN throttling |
| Integration | testcontainers: RedisCache round-trip, TTL expiry, corrupted value = miss; the service with real Redis+PostgreSQL: the second GetRate does not hit the source, degradation to last-known, LockQuote writes a row into `quotes` |
| Local E2E | run per `test/e2e/E2E.md` (scenario E2E_Quote_LockQuote, compose stand): GetRate, LockQuote, a row in PostgreSQL (KI-quote-04 closed) |
| Load | 14.1 |

## 10. Work plan and commit

1. Spec, tests (unit + integration), then implementation: `domain` errors, `storage.RedisCache`,
   `usecase` cache and `LockQuote`, transport, main, and env.
2. Four review passes, AGENTS.md (trade-offs, KI, journal), linters, e2e.
3. Commit `CBL-6.2: LockQuote and the rate cache in Redis`; the spec is in the same commit.

## 11. Open questions and decisions

- ADR: the Redis cache is untrusted (values are checked by age and format), and `LockQuote`
  locks only the fresh source rate: a poisoned cache key cannot enter a
  monetary quote (security review 6.2, the blocker was closed by this decision).
- ADR: the last-known TTL of 1h was chosen as the GetRate degradation window during a source outage;
  for LockQuote, source unavailability is an explicit error and a stale rate is not locked.
- ADR: WARN logs are throttled to 30 seconds per key so that a dependency failure does not create
  a log flood under high load.
- Accepted with a limitation: singleflight on a miss is not introduced; a burst of calls to the sources
  is bounded by the short cache TTL (5s), measurement and decision - 14.1.
- Accepted with a limitation: a concurrent last-known write may roll the value back to an earlier
  `AsOf` within adjacent updates; the guard bounds the deviations, impact - 14.1.
- KI-quote-06: in dev Redis is protected by a password but without TLS; in prod - ACL and a private network
  (0.6); rediss:// support is already implemented via `REDIS_URL`.
- Open question: the quote amount columns for audit - decided in 6.3 (a new migration
  if amount reconciliation is required).
- Open question: linking `payment_id` in `quotes` without an RPC - 6.3.

## 12. Review (CBL-001, step 6)

Four independent passes (14.10.2026). The security blocker (a poisonable cache as the rate source
for LockQuote) was closed by a design change: the rate is locked only from fresh data,
and cache values are validated by age and format. All major and minor findings are closed.

| Role | Key findings | Closure |
| :-- | :-- | :-- |
| No context | default Redis timeouts (3 - 5 s) violate the budgets; there is no e2e artifact; the UUID was checked without the version; error pairs for LockQuote | client timeouts 200/300 ms and handler deadlines 90/250 ms; `test/e2e/lock-quote.ps1` + `make e2e`; UUIDv7 check; a single `ErrInvalidLock` |
| Application architecture | budgets were not guaranteed on a cache miss; the e2e artifact; keys diverged from CBL-003; no trade-off about usecase -> storage; 6.2/6.3 boundaries; WARN storm; no singleflight; the last-known age was not visible in the audit | handler deadlines; keys aligned with CBL-003; T-QUOTE-05; the 6.3 plan clarified; WARN throttling; singleflight and the last-known rollback - accepted limitations to be revisited in 14.1; `rate_as_of` - a 6.3 candidate |
| With context | the status/journal ran ahead of the review; duplicate package comments; the env list diverged; WARN storm; copies of fakes in tests | statuses and the journal synchronized; package comments consolidated in doc.go; the env list corrected (MIGRATIONS_DIR, REDIS_URL/PASSWORD, no Kafka); throttling; fake copies kept per package |
| Security (BLOCK lifted) | the poisonable cache entered monetary locking; Redis without a password/TLS; default go-redis timeouts; source error details (URL with api_key) leaked to the client and into the logs; amounts in error texts | LockQuote only from fresh data + cache validation (age, format, 20 digits); REDIS_URL/PASSWORD, requirepass in compose, rediss:// for prod (KI-quote-06); timeouts and ContextTimeoutEnabled; sanitization of `url.Error` and PG errors, details only in the log; amounts removed from errors |
