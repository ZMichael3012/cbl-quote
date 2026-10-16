# AGENTS.md - cbl-quote

Rates and locked quotes for invoices.

Mandatory global rules: `../../AGENTS.md`, `../../.specs/CBL-001-development-process.md`,
`../../.specs/CBL-002-repos-and-docker.md`, contracts `../../.specs/CBL-003-contracts.md`.
The em dash (U+2014) is forbidden; only the hyphen is used.

## 1. Boundaries

In scope: LockQuote and GetRate, rate cache, audit of locked quotes.

Out of scope: other platform services; interaction only via `cbl-protos` contracts
(gRPC), Kafka and the perimeter REST (see `CBL-000`, section 2).

## 2. Contracts

- REST: administrative /healthz, /readyz
- gRPC: QuoteService server
- Kafka produces: none
- Kafka consumes: none
- Storage: PostgreSQL quote, Redis (rate cache)

Locked quotes are stored in the immutable `quotes` journal (append-only:
UPDATE/DELETE/TRUNCATE are forbidden by triggers; corrections only via a new quote).
Audit format (6.3): `sources` - a jsonb array of `{"name","value"}` of the successful sources
in configuration order; `spread` - the relative spread `(max-min)/median` as a decimal
string (6 digits); `guard_adjusted` - the flag of a rate substituted by the anomaly guard;
`as_of` - the time of the issued rate (when substituted, of the last known one); `payment_id`
remains NULL until the scenario appears in 4.2.

Contracts are frozen in `cbl-protos` (CF-1 for gRPC, CF-2/CF-3 for events), see `CBL-004`.

## 3. Architecture

Layers: `internal/transport` -> `internal/usecase` -> `internal/domain` -> `internal/storage`.
Libraries: the standard `CBL-002` set (Gin, GORM + pgx driver, franz-go + pkg/sr, go-redis,
goose, zap). `AutoMigrate` is forbidden; migrations are goose only, forward only.

## 4. Commands

```powershell
make build        # go build ./...
make test         # go test ./...
make test-integration  # integration (testcontainers: PostgreSQL, Redis)
make vet          # go vet ./...
make lint         # golangci-lint in Docker
make up           # docker compose -f deploy/docker-compose.yml up -d
make down         # docker compose -f deploy/docker-compose.yml down -v
```

Compose stand e2e runs follow the `test/e2e/E2E.md` instruction (scenario E2E_Quote_LockQuote;
artifacts in `test/e2e/.runs/<mark>/`); there are no PowerShell run scripts.

## 5. Ports and environment

HTTP `8070`, gRPC `9070`, metrics `9270`.
Variables (applicable to the service): `HTTP_ADDR`, `GRPC_ADDR`, `METRICS_ADDR`, `PG_DSN`,
`MIGRATIONS_DIR`, `REDIS_URL` (redis:// or rediss://, takes priority) or `REDIS_ADDR` +
`REDIS_PASSWORD`, `RATE_SOURCE`, `RATE_CONFIG`, `RATE_HTTP_URL`, `RATE_HTTP_NAME`,
`RATE_MAX_SPREAD_PCT` (default 2), `RATE_ANOMALY_PCT` (default 10),
`RATE_ANOMALY_RESET` (default 15m), `RATE_ALLOWED_IDENTICAL_PAIRS`,
`RATE_CACHE_TTL` (default 5s), `RATE_LAST_TTL` (default 1h), `LOCK_QUOTE_TTL` (default 5m),
`LOCK_QUOTE_MAX_TTL` (default 15m).

## 6. Development rules

- Pipeline: SDD spec in `.specs/` -> architecture -> contracts -> tests first -> code ->
  review by 4 agents (no-context, architecture, with-context, security - blocking) ->
  AGENTS.md -> final runs -> linters -> commit.
- Unit+integration coverage is at least 80%; money scenarios only on real PostgreSQL and
  Kafka (testcontainers); a local e2e run in docker-compose is mandatory.
- Money is int64 in minor units plus decimals only; float is forbidden.
- Godoc docstrings are mandatory in English: the first line starts with the entity name and
  ends with a period; comments explain why; errors are wrapped with `%w`; panics in business
  code are forbidden.
- Commit: `CBL-<epic>.<number>: <short message>`, the spec with the same prefix in the same
  commit.

## 7. SLO and metrics

The SLO is specified in task 14.4. Base metrics: latency p95/p99, error rate, outbox lag,
consumer lag; dashboards after task 0.7.

## 8. Trade-offs

| ID | Decision | Pros | Cons and compensation |
| :-- | :-- | :-- | :-- |
| T-QUOTE-01 | AnomalyGuard state is stored in the instance memory | no external dependencies, minimum latency | with several replicas the state is not shared and is reset on restart | a 15-minute resynchronization window limits the risk; a shared cache in 14.x |
| T-QUOTE-02 | Redis is the rate cache (fresh TTL 5s and last-known 1h), not the source of truth | 6.2: load was removed from the sources, GetRate degrades when they are unavailable | cache values are untrusted (Redis can poison a key) | format and age validation on read; LockQuote locks only the fresh source rate; Redis errors degrade to the sources, the WARN is throttled to 30 s |
| T-QUOTE-03 | Pairs with identical assets are forbidden, except the whitelist | protection from malformed requests | a whitelist is needed for USDT/USD | env `RATE_ALLOWED_IDENTICAL_PAIRS` |
| T-QUOTE-04 | `LockQuote` is not idempotent at the RPC level (there is no key in contract CF-1) | simple contract | a repeated call creates a new quote | no effect on money; payment idempotency is provided by Billing API/Payment (4.x) |
| T-QUOTE-05 | the usecase imports storage (`storage.Quote` in the QuoteStorage signature) | no extra layer mapping, precedent ledger T-LED-01 | layer boundaries are blurred | the RateCache/QuoteStorage interfaces are declared on the consumer side; revision - as the number of storages grows |
| T-QUOTE-06 | The `quotes` journal is append-only (UPDATE/DELETE/TRUNCATE triggers), the audit is complete (sources, spread, amount, guard substitution flag) | immutable evidence of the rate lock, a second barrier at the DB level | the table grows with every lock; a third index on INSERT; DB owner level bypass (`DISABLE TRIGGER`) is outside the dev threat model; source names in the audit are not validated | retention/partitioning - KI-quote-07; prod roles with INSERT/SELECT rights - 0.4/0.6; name validation - KI-quote-08 |

## 9. Known issues

| ID | Problem | Impact | Plan | Status |
| :-- | :-- | :-- | :-- | :-- |
| KI-quote-01 | CI and pre-commit are not configured | linters are run manually | task 0.2 | open |
| KI-quote-02 | No rate cache and LockQuote | load on the sources, rate locking is not ready | CLOSED 14.10.2026: the Redis cache and LockQuote are implemented in 6.2 | closed |
| KI-quote-03 | Race detector is unavailable locally (no CGO) | the concurrency test runs without -race | enable -race in CI (0.2) | open |
| KI-quote-04 | The e2e and load directories are empty | there was no end-to-end quote check | CLOSED 14.10.2026: e2e LockQuote/GetRate on compose in 6.2; k6 in 14.1 | closed |
| KI-quote-05 | No mTLS on gRPC QuoteService | do not expose port 9070 outside the trusted network; prod deployment is forbidden | task 0.6 | open |
| KI-quote-06 | Redis in dev without TLS (a password is set); in prod there is no ACL and private network | the theoretical cache poisoning vector is closed by validation; keys can be written from a foreign network | rediss:// is already supported (`REDIS_URL`); ACL, private network and TLS in prod - 0.6 | open |
| KI-quote-07 | The `quotes` journal grows with every lock; retention and partitioning are not defined | DB and backup growth; reads by PK/indexes are not affected by growth | retention/partitioning when planning storage growth (wave W4, the owning task is not registered yet) | open |
| KI-quote-08 | The source name (`RATE_HTTP_NAME`, `config`) is not validated and enters the audit `sources[].name` as is | an operational error (a URL/token in the name) may enter the audit journal | name format validation when moving to multiple sources | open |

## 10. Task journal

| CBL | Date | Status | Spec | Commit |
| :-- | :-- | :-- | :-- | :-- |
| CBL-6.1 | 2026-10-14 | done, review closed (config+http sources, guard mutex, RPC GetRate, coverage 90.5%) | `.specs/CBL-6.1-rate-source.md` | ec98004 |
| CBL-6.2 | 2026-10-14 | done, review closed (LockQuote on fresh data, Redis cache with untrusted value validation, e2e script, coverage 91.2%) | `.specs/CBL-6.2-lock-quote-cache.md` | 5ab684d |
| CBL-6.3 | 2026-10-15 | done, review closed (append-only audit journal with a TRUNCATE trigger, source/spread/amount/guard fields, e2e with audit checks) | `.specs/CBL-6.3-quote-audit.md` | 511e496 |
| CBL-16.9 | 2026-10-16 | done (English comments and documentation across the repository: code, tests, configs, all three specs, AGENTS.md, README; e2e runs via `test/e2e/E2E.md` with scenario E2E_Quote_LockQuote, `lock-quote.ps1` and `make e2e` removed; validation run PASS; coverage unchanged) | `.specs/CBL-16.9-conventions.md` | this commit |

## 11. Runbook

Filled in by the first product task of the service. Typical incidents: dependency
unavailability, error growth, processing lag; actions: check health/metrics, logs with
request_id, the state of the Kafka consumer group and outbox lag, restart the service if
necessary.

Quote audit (6.3):

1. Quote breakdown: `SELECT id, base_asset, quote_asset, rate, as_of, amount_asset,
   amount_minor, sources, spread, guard_adjusted, locked_until FROM quotes WHERE id = '...'`
   (or by `payment_id`, pair and period - indexes `quotes_payment_idx`, `quotes_pair_idx`,
   `quotes_created_at_idx`).
2. `guard_adjusted=true`: the last known rate was locked instead of the rejected anomalous
   sample; the raw source values and the spread are visible in `sources`/`spread`.
3. Corrections are impossible (append-only): an erroneous lock is corrected by a new quote;
   UPDATE/DELETE/TRUNCATE are rejected by triggers; journal growth - KI-quote-07.
