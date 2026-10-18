# E2E cbl-quote: GetRate and LockQuote on the compose stand

Run instruction for the local e2e scenario of the service. A run is performed by an
agent platform-independently: docker and a command shell. There are no PowerShell run
scripts (customer decision of 16.10.2026, `CBL-002` section 7).

Repository: `cbl-quote`. Stand: `deploy/docker-compose.yml` (postgres, redis, quote in
the `app` profile; the config rate source provides the stand rate USDT/RUB = 95.50).

## 1. Scenarios

| Scenario | Checks | Contracts |
| :-- | :-- | :-- |
| `E2E_Quote_LockQuote` | compose stand up, healthz, `GetRate` returns the configured rate, `LockQuote` returns a UUID quote id with the same rate and a locked_until, the quote row is persisted with complete audit fields (as_of, amount_minor, sources, guard_adjusted, spread) | CBL-6.2 (LockQuote, Redis cache), CBL-6.3 (append-only audit journal); gRPC QuoteService (CF-1) |

## 2. Isolation and parallelism

- Run mark: UTC lowercase `yyyymmddthhmmssz` (uppercase is rejected by compose project
  naming rules).
- Artifact directory: `test/e2e/.runs/<mark>/` (create before the start).
- Compose project: `cbl-quote-e2e-<mark>` (the compose file declares `name: cbl-quote`;
  the `-p` flag overrides it).
- Host ports 5432, 6379, 8070, 9070, 9270 are published; do not run two quote stands in
  parallel and stop other quote stands first.
- All commands run from the repository root.

## 3. Common run order

1. `<MARK>` = current UTC time in lowercase; create `test/e2e/.runs/<MARK>/`.
2. Stop the previous stand: `docker compose -p cbl-quote-e2e-<MARK> -f deploy/docker-compose.yml --profile app down -v`
   (output to `compose-down.log`; the first run has nothing to stop).
3. Start the stand: `docker compose -p cbl-quote-e2e-<MARK> -f deploy/docker-compose.yml --profile app up -d --build --wait`
   (`compose-up.log`); `docker compose ps` into `compose-ps.txt`.
   Set `RATE_MIN_AMOUNTS=USDT/RUB@RUB:100000` in the environment before the
   start (the compose file passes the host value into the quote container; the
   default empty value disables the minimums, but the extended scenario expects
   the minimum and the audit check below).
4. Wait for `GET http://localhost:8070/healthz` = 200 (up to 60 s, 2 s interval) and
   `readyz` = 200 (curl.exe).
5. Run the scenario checks (section 4), recording every OK/FAIL line in
   `lock-quote-checks.txt` and transcripts in `lock-quote.log`.
6. Capture logs: `docker compose -p ... logs --no-color > compose-logs.txt`.
7. `summary.md` by the `.additionals/summary.template.md` template; stop the stand:
   `down -v` (append to `compose-down.log`).

Utility notes:

- grpcurl helper (JSON via stdin; `host.docker.internal` reaches the published gRPC
  port from the utility container; do not use argv for the body - shells mangle quotes):
  `echo <JSON> | docker run --rm -i fullstorydev/grpcurl:v1.9.1 -plaintext -max-time 10 -d @ host.docker.internal:9070 billing.quote.v1.QuoteService/<Method>`
- psql helper: `docker compose -p ... exec -T postgres psql -U cbl -d quote -t -A -c "<SQL>"`.

## 4. E2E_Quote_LockQuote

1. Common run order, steps 1-4.
2. `GetRate` with `{ "base_asset": "USDT", "quote_asset": "RUB" }`: the `rate` field is
   exactly `95.50` (the config source value of the stand).
3. `LockQuote` with
   `{ "from_asset": "USDT", "to_asset": "RUB", "amount": { "minor_units": 100000, "asset": "RUB", "decimals": 2 }, "ttl_seconds": 120 }`:
   - `quoteId` is a non-empty UUID (matches `^[0-9a-fA-F-]{36}$`);
   - `rate` is exactly `95.50`;
   - `lockedUntil` is present.
4. Persistence: `SELECT count(*) FROM quotes WHERE id = '<quoteId>'` returns `1`.
5. Audit fields (6.3, extended by 17.10):
   `SELECT (as_of IS NOT NULL)::text || ',' || (amount_minor IS NOT NULL)::text || ',' || (jsonb_array_length(coalesce(sources, '[]'::jsonb)) > 0)::text || ',' || guard_adjusted::text || ',' || (spread IS NOT NULL)::text || ',' || (min_amount_minor IS NOT NULL)::text || ',' || coalesce(min_amount_minor::text, '') FROM quotes WHERE id = '<quoteId>'`
   must be exactly `true,true,true,false,true,true,100000`.
6. A lock below the minimum answers `InvalidArgument`:
   `{ "from_asset": "USDT", "to_asset": "RUB", "amount": { "minor_units": 99999, "asset": "RUB", "decimals": 2 }, "ttl_seconds": 120 }`
   (the error text mentions `minimum`), and
   `SELECT count(*) FROM quotes WHERE amount_minor = 99999` returns `0`.
   A lock with the other pair side as the amount asset (`USDT`) is rejected
   too: the minimum is bound to the configured asset.
7. Checks go to `lock-quote-checks.txt`.

## 5. Mandatory run artifacts

- `summary.md` (template `.additionals/summary.template.md`; PASS/FAIL verdict, steps,
  key values, artifact list, time);
- `compose-ps.txt`, `compose-up.log`, `compose-down.log`, `compose-logs.txt`;
- `lock-quote.log` (all grpcurl and psql transcripts) and `lock-quote-checks.txt`;
- on failure: container states, the quote container log fragments, the failing grpcurl
  call and its raw output.

## 6. Auxiliary files (.additionals)

- `summary.template.md` - the `summary.md` template;
- `inventory.md` - the list of auxiliary files and their purpose.

## 7. Cleanup

`docker compose -p cbl-quote-e2e-<MARK> -f deploy/docker-compose.yml --profile app down -v`
at the end; `.runs/` is not committed; a repeated run must start from a clean stand.
