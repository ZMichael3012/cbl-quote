# CBL-6.3. Storage of locked quotes for audit

| Field | Value |
| :-- | :-- |
| Epic | E6, Quote |
| Repository | `cbl-quote` |
| Size | S |
| Wave and parallel group | W1-QUOTE |
| Depends on | 6.2 |
| Contract version | `cbl-protos@cf-1` (contract unchanged) |
| Status | done, review closed |

## 1. Context and goal

`LockQuote` already persists a row in `quotes` (`CBL-6.2`), but the record contains only the
rate and the term. Auditing a monetary lock requires an immutable journal with an evidence
base: which source rates and when were locked, for which amount, with what spread, and
whether the rate was substituted by the anomaly guard. The task closes the open questions of
6.2 (whether to store the amount and `rate_as_of`) and makes the table append-only.

## 2. Scope

In scope: migration 00002 (audit fields, append-only trigger, index), `AggregateDetailed`,
field population in `LockQuote`, tests, e2e extension.
Out of scope: gRPC contract change (CF-1 is frozen; `LockQuoteResponse` is unchanged),
`payment_id` population (not in the contract; will appear with 4.2/CF-2), retention and
partitioning (14.2), admin journal reads (12.x, Back-office), k6 (14.1).

## 3. Functional requirements

FR-1. Audit journal: every locked quote is stored immutably; `quotes` is an append-only
table: UPDATE and DELETE are forbidden by a row trigger, TRUNCATE by a statement trigger
(following the pattern of the append-only trigger on ledger `entries`). An erroneous record
can only be corrected by a new quote. Bypass at the DB owner level (`DISABLE TRIGGER`,
`DROP TABLE`) is outside the dev stand threat model; in prod, application rights are
restricted by roles (0.4/0.6).

FR-2. Migration 00002 extends `quotes`: `as_of` (the actuality time of the locked source
rate), `ttl_seconds` (the requested lock term), `amount_asset` + `amount_minor` (the request
amount for which the rate was locked), `sources` (jsonb array of `{"name","value"}` sources
that produced the rate), `spread` (relative spread of sources as a decimal string, 6
digits), `guard_adjusted` (the rate was replaced with the last known one because of an
anomaly; `NOT NULL DEFAULT false`, safe for legacy rows). The remaining new columns are
nullable (legacy rows of dev stands); when populated, they are validated by CHECK:
`ttl_seconds > 0`, `amount_minor > 0`. Audit index `quotes_created_at_idx (created_at, id)`.

FR-3. Domain: `Aggregator.AggregateDetailed` returns the median rate and the relative spread
`(max - min) / median` as a decimal string (6 digits); `Aggregate` remains a wrapper
(compatibility). The spread is computed from the same values as the threshold check, without
re-parsing; equality to the threshold is allowed (the check is strict).

FR-4. `LockQuote` records the audit fields: `as_of` from the source rate (when substituted
by the anomaly guard, the time of the issued last known rate), `ttl_seconds` from the
accepted TTL (sub-second configuration values are replaced with the default: the audit
stores whole seconds, CHECK forbids zero), the amount and asset from the request (after
verifying they match the pair), `sources` - the successful sources in configuration order
(raw sample values, even if the guard rejected the sample), `spread` from the aggregator,
`guard_adjusted=true` when `AnomalyGuard` returned the last known value (exactly the issued
rate is recorded; the substitution fact is visible in the audit). `payment_id` remains NULL:
it is not in the CF-1 contract; the decision on populating it is 4.2/CF-2.

FR-5. The e2e run per `test/e2e/E2E.md` (scenario E2E_Quote_LockQuote, compose stand)
additionally checks audit field population in PostgreSQL (as_of, amount_minor, sources not
empty, guard_adjusted=false).

## 4. Contracts

External contracts are unchanged: `LockQuoteResponse` (CF-1) is not extended; audit data is
accessible only via PostgreSQL (reads for Back-office are 12.x, out of scope). The format of
`spread` and `sources` is fixed in this spec and in the service AGENTS.md.

## 5. Data model and migrations

`migrations/00002_quote_audit.sql`: ALTER TABLE + CHECK + index + append-only trigger (DDL
following the pattern of ledger `00001_ledger_init.sql`: function `quotes_append_only`).

## 6. State machines

Not applicable; a quote is immutable, the term is in `locked_until` (the actuality check is
the Payment/Billing API task).

## 7. Edge cases

Legacy rows without audit fields (`Get` reads do not fail); UPDATE, DELETE and TRUNCATE are
rejected by triggers; a repeated INSERT with the same id is rejected by uniqueness (upsert
is impossible: `Create` without `ON CONFLICT`); the guard substituted the rate (the audit
holds `guard_adjusted=true`, the issued rate, the `as_of` of the issued rate and the raw
values of the rejected sample); spread exactly at the threshold boundary (the record is
stored with the actual spread); a single source (spread 0); sub-second `LOCK_QUOTE_TTL`
(replaced with the default, `ttl_seconds` cannot be zero); TTL at the maximum boundary; the
amount in minor units of the other asset of the pair; DB failure between the check and the
write (`ErrLockStorage` error, no audit is created).

## 8. Non-functional requirements

Journal immutability is guaranteed by the DB, not the application (UPDATE/DELETE/TRUNCATE
triggers; DB owner level bypass is outside the dev threat model, prod roles are 0.4/0.6);
the audit write is the same single INSERT transaction as before (the LockQuote budget is
unchanged); the size of `quotes` grows with every lock, a third index was added on the
INSERT path (retention is KI-quote-07); no request data except the amount and assets enters
the journal (neither source URLs nor secrets; source names are set by the operator,
KI-quote-08).

## 9. Test plan

| Level | Scenarios |
| :-- | :-- |
| Unit (domain) | `AggregateDetailed`: spread, threshold/value errors, `Aggregate` equivalence |
| Unit (usecase) | audit fields in the stored quote (fake storage): as_of, ttl, amount, sources, spread; guard scenario (adjusted=true, the issued rate) |
| Integration (storage) | round-trip of the new fields (jsonb sources); UPDATE and DELETE rejected by the trigger; a legacy row (without the fields) is read |
| E2E | run per `test/e2e/E2E.md` (scenario E2E_Quote_LockQuote, compose stand): audit fields in PostgreSQL |
| Load | 14.1 |

## 10. Work plan and commit

Tests first (domain, usecase, integration), then migration, storage, domain, usecase, e2e,
documentation. Commit `CBL-6.3: immutable quote journal for audit`; the spec is in the same
commit.

## 11. Open questions

- `payment_id` in `quotes` will remain NULL: it is not in the CF-1 contract; it will be
  populated when the scenario appears in 4.2 (Payment) or in a separate RPC (CF-2+,
  backward compatible).
- Retention and partitioning of `quotes` (grows with every lock) - KI-quote-07; the owning
  task in the global plan is not registered yet (candidate - wave W4, storage).
- Admin journal reads (Back-office 12.x) - a separate contract, outside the scope of 6.3.
- Source names in `sources` are not validated (operator configuration): when moving to
  multiple sources, add name format validation - KI-quote-08.

## 12. Review (CBL-001, step 6)

Four independent passes (15.10.2026). No blockers. The MAJOR (AGENTS.md not updated - step 7:
journal, KI, trade-off, audit format) was closed by this commit; the security MAJOR (TRUNCATE
bypasses the row trigger) was closed by a statement trigger and a test; the sub-second TTL
regression was closed by normalization in `WithLockTTL`; the remaining findings were closed
by tests (CHECK boundaries, spread boundary, `as_of` semantics on substitution, PK
duplicate), spec clarifications and e2e script hygiene.

| Role | Key findings | Closure |
| :-- | :-- | :-- |
| No context | FR-2 vs `NOT NULL` on `guard_adjusted`; sub-second TTL breaks the CHECK; `as_of` semantics under `guard_adjusted` not pinned down; no spread boundary test; a reference to a non-existent duplicate name check; a typo | FR-2 clarified; `WithLockTTL` normalizes < 1 s; a test with different `AsOf` values and the decision recorded in FR-4; boundary test; the reference removed; the typo fixed |
| Architecture | sub-second TTL; AGENTS not updated; a reference to 14.2 without an owning task; function `search_path`; TRUNCATE/owner outside the threat model; no PK duplicate test; e2e header; extra `fetchRate` work for GetRate | TTL normalized; AGENTS updated; the reference replaced with KI-quote-07; `search_path` hardened; boundaries recorded in the spec; duplicate test; header updated; allocations accepted (NIT, nanoseconds against the network) |
| With context | MAJOR: AGENTS not updated (journal, KI-quote-07, trade-off, format); sub-second TTL; no CHECK tests; spread boundary; dead marshal branch; `Save` docstring; `TTLSeconds` in the CustomTTL test; e2e NULL diagnostics | AGENTS updated; TTL normalized; CHECK tests added; boundary covered; the branch kept as defensive (NIT); docstring fixed; assertion added; `coalesce` in SQL |
| Security | MAJOR: TRUNCATE not covered by the row trigger (owner role in dev); SQL concatenation of quoteId in e2e; source names without validation; sub-second TTL | statement trigger + test; UUID format validation; KI-quote-08; TTL normalized |
