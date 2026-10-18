# CBL-17.10. Quote min-amount and audit evidence

| Field | Value |
| :-- | :-- |
| Epic | E17 (real provider integration) |
| Repository | `cbl-quote` |
| Size | S |
| Wave and parallel group | W5-PROVIDER |
| Depends on | 6.2 (LockQuote), 6.3 (audit journal), CF-1 contracts |
| Contract version | none (CF-1 unchanged; no `.proto` change) |
| Status | done |

## 1. Context and goal

Provider limits require a minimum invoice amount per pair: a provider refuses or
mis-handles a deposit below its own minimum, and the platform must not lock a
rate for an amount that cannot be paid. This increment adds a per-pair minimum
to `LockQuote`, rejects amounts below it with `invalid_lock`, and records the
applied minimum in the immutable quote journal as audit evidence. The provider
estimate feed (a provider-side rate/limits source in the quote service) stays
out of scope: no source contract exists yet (KI-quote-09).

## 2. Scope

In scope: `RATE_MIN_AMOUNTS` parsing (fail-fast), `WithMinAmounts`, the minimum
check in `LockQuote`, the `min_amount_minor` audit column (migration 00003), unit
and integration tests, the compose e2e scenario extension.

Out of scope: provider estimate source and the `provider`/`provider_rate`
columns (land together with that source, KI-quote-09); contracts (CF-1
unchanged); payment-side validation (payment 17.10/4.2).

## 3. Architecture sketch

- `usecase.ParseMinAmounts(raw string) (map[string]MinAmount, error)`: CSV
  `BASE/QUOTE@ASSET:MINOR`, assets validated by the same rules as the pair,
  ASSET is one side of the pair, MINOR > 0, duplicates rejected, the key is
  `domain.Key`; an empty value disables the feature, a whitespace-only value is
  an error.
- `WithMinAmounts(map[string]MinAmount)` sets `Service.minAmounts`.
- `LockQuote`: after the amount-asset check, a configured minimum for the pair
  requires the request amount asset to be the configured ASSET (the other side
  has a different scale and is rejected with `ErrInvalidLock`, so the scale
  cannot be used to bypass the limit); below the minimum -> `ErrInvalidLock`;
  the applied minimum is stored as `quotes.min_amount_minor` (NULL when the
  pair has no configured minimum).
- `main`: `RATE_MIN_AMOUNTS` is parsed at startup; an invalid value fails the
  service start (fail-fast, like the other rate variables).

## 4. Functional requirements

FR-1. For a pair with a configured minimum the request amount asset must equal
the configured ASSET (otherwise `ErrInvalidLock`: the scales are not comparable
and the limit would be bypassable); an amount below the minimum is rejected
with `ErrInvalidLock`; no quote is saved.

FR-2. An amount at or above the minimum is locked as before; the journal row
records the applied `min_amount_minor`.

FR-3. A pair without a configured minimum behaves exactly as before; the column
is NULL.

FR-4. An invalid `RATE_MIN_AMOUNTS` value (bad pair, asset outside the pair,
non-positive minor, duplicate, whitespace-only) refuses the service start with a
non-zero exit code.

## 5. Contracts

No `.proto` change: `LockQuote` request/response (CF-1) carry no minimum field;
the minimum is platform policy and audit evidence.

## 6. Data model and migrations

Migration 00003: `ALTER TABLE quotes ADD COLUMN min_amount_minor bigint CHECK
(min_amount_minor IS NULL OR min_amount_minor > 0)`; Down drops the column. The
append-only triggers are unchanged.

## 7. State machines and rules

None: `LockQuote` stays non-idempotent at the RPC level (T-QUOTE-04); the
minimum is a pure request check before the rate fetch.

## 8. Edge cases

E-1. The minimum is bound to one explicit pair side (ASSET); the request must
use that asset as the amount asset, otherwise it is rejected.
E-2. A pair value with different letter case or surrounding spaces parses to
the canonical key; identical pairs are allowed by the parser (the LockQuote
whitelist still decides whether the pair itself is usable).
E-3. An empty `RATE_MIN_AMOUNTS` disables the feature (a WARN at startup); a
whitespace-only value is a configuration error.
E-4. A duplicate pair with different minimums is a configuration error.

## 9. Non-functional requirements

- NFR-1. Coverage stays at least 80% unit+integration.
- NFR-2. No new dependencies.
- NFR-3. Money stays int64 minor units; no float.
- NFR-4. English only, no em dash, godoc on exported entities.

## 10. Test plan

### 10.1. Unit matrix

| ID | Case | Input | Expected |
| :-- | :-- | :-- | :-- |
| P-01 | parse valid | single, multiple, lowercase, spaces, identical pair | canonical map with the asset |
| P-02 | parse invalid | missing/non-positive minor, missing asset, asset outside the pair, bad pair, whitespace-only, duplicate | error |
| P-03 | below minimum | rule RUB/100000, amount 99999 | `ErrInvalidLock`, nothing saved |
| P-04 | at the minimum | rule RUB/100000, amount 100000 | locked, `min_amount_minor` = 100000 |
| P-05 | no minimum | pair absent from the map | locked, NULL recorded |
| P-06 | other pair side | rule RUB, amount asset USDT | `ErrInvalidLock`, nothing saved |

### 10.2. Integration matrix (PostgreSQL, Redis)

| ID | Case | Input | Expected |
| :-- | :-- | :-- | :-- |
| I-01 | minimum persisted | configured min, amount at the minimum | row stores `min_amount_minor`; below-min rejected with no row |

### 10.3. Smoke set

`make test`, `make test-integration`, `make vet`, e2e `E2E_Quote_LockQuote`
(extended below).

### 10.4. E2E scenario descriptions

| Scenario | Description | Tag or stand |
| :-- | :-- | :-- |
| E2E_Quote_LockQuote (extended) | compose stand started with `RATE_MIN_AMOUNTS=USDT/RUB@RUB:100000`: the existing GetRate/LockQuote/audit checks stay; a lock below the minimum answers `InvalidArgument`; the audit check additionally verifies `min_amount_minor` = 100000 | `test/e2e/E2E.md` |

### 10.5. Load plan

Not applicable (one map lookup per lock).

### 10.6. Test-first evidence

Red run recorded before implementation (18.10.2026):
`go test ./internal/usecase/` failed to compile with `undefined:
usecase.WithMinAmounts` and `storage.Quote has no field or method
MinAmountMinor` (the parser test compiled against the missing function, the
lock test against the missing option and column), i.e. the feature was absent;
`go mod tidy` leftovers of the Go 1.26 wave were folded in first so the module
built at HEAD. After implementation: unit green; the full battery with
integration (`go test -tags integration -coverpkg=./internal/... ./...`) green,
total coverage 92.0%; the compose run `E2E_Quote_LockQuote` (extended with
`RATE_MIN_AMOUNTS=USDT/RUB@RUB:100000`) PASS 10/10 (mark `20261018t143418z`:
GetRate 95.50, a lock at the minimum succeeds, a below-minimum lock answers
InvalidArgument, the journal row stores as_of/amount/sources/spread/guard and
`min_amount_minor=100000`; artifacts in `test/e2e/.runs/20261018t143418z/`).
Review of the staged diff by four reviewers: findings closed before the commit
(journal).

## 11. Work plan and commits

Single commit: spec, parser/option/check, migration 00003, storage field, tests,
compose e2e extension, AGENTS journal; the `go mod tidy` leftovers of the
toolchain wave are folded in.



