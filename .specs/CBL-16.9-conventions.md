# CBL-16.9. cbl-quote: English comments and documentation; e2e via E2E.md

| Field | Value |
| :-- | :-- |
| Epic | E16, `Convention alignments` |
| Repository | `cbl-quote` |
| Size | S |
| Wave and parallel group | W0-CORR |
| Depends on | `CBL-16.1` (convention), `CBL-16.2` (pilot pattern) |
| Contract version | not applicable (no contract changes) |
| Status | done, review closed |

## 1. Context and goal

Two cross-cutting customer corrections (16.10.2026): (1) comments and documentation are
written in English in all files (code, tests, migrations, specs, AGENTS.md, README,
configurations; log and error messages stay unchanged as executable contracts);
(2) e2e runs are not PowerShell scripts but an agent instruction `test/e2e/E2E.md` with
run artifacts in `test/e2e/.runs/`. The convention is `CBL-002` section 7.

## 2. Scope

In scope: English translation of all comments, docstrings, config comments, all three
specs (CBL-6.1/6.2/6.3), AGENTS.md and README.md; `test/e2e/E2E.md` (scenario
`E2E_Quote_LockQuote`: stand up, healthz, GetRate, LockQuote, the persisted row and the
audit fields); removal of `test/e2e/lock-quote.ps1` and the `make e2e` target;
Makefile/.gitignore updates.

Out of scope: changing behavior and runtime messages (gRPC status texts built by
`grpcmw.E`, zap log fragments quoted in specs as contracts; one test asserts against the
kept status text), other repositories (16.10 - 16.16).

## 3. Requirements

FR-1. Language: no Russian comments or docstrings remain; Russian string literals remain
only as runtime messages (gRPC status texts) and in the test assertion that checks that
contract; godoc docstrings keep the entity-name first line and end with a period;
AGENTS.md development rules state the English documentation rule.

FR-2. E2E: `test/e2e/E2E.md` contains preconditions, isolation (compose project per
lowercase UTC mark, fixed host ports), the run order, the scenario check list (healthz,
GetRate 95.50, LockQuote UUID + rate + lockedUntil, the persisted row, the audit fields
`true,true,true,false,true`), mandatory artifacts and cleanup; there are no PowerShell
run scripts; AGENTS.md references the instruction.

FR-3. Auxiliary files live in `test/e2e/.additionals/` (summary template, inventory);
`.runs/` is gitignored.

FR-4. Validation run: the scenario executed per `E2E.md`, artifacts in
`test/e2e/.runs/<mark>/`, `summary.md` filled with a PASS verdict.

## 4. Plan

Comment and documentation translation (parallel by file groups: cmd/domain/source,
usecase/storage/transport, specs, AGENTS.md/README) -> E2E.md and `.additionals` ->
removal of the ps1 script and the e2e target -> validation run -> 4 reviews -> commit
`CBL-16.9: English comments and docs; e2e via E2E.md`.

## 5. Reviews and runs

Verified: unit+integration tests green (no production code changes), lint 0, the e2e
scenario run per `E2E.md` with run artifacts, em-dash check clean, comment language
verified by a Cyrillic scan (remaining Cyrillic: gRPC status message literals in
`internal/transport/grpcserver/quote.go` and their assertion in `quote_test.go`; none
are comments).
