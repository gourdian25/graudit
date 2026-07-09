# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

graudit (`github.com/gourdian25/graudit`) is an append-only, tamper-evident
audit trail for the gourdian ecosystem — it answers "what changed, who did
it, and can we prove the record hasn't been altered," a different question
from what [grlog](https://github.com/gourdian25/grlog) answers ("what
happened during this request"). Like `grcache`, it uses one subpackage per
storage backend (`graudit/memory`, `graudit/postgres`, `graudit/mongo`) so a
consumer using only one backend doesn't pull in the others' client
libraries. Read `docs/architecture.md` for the deliberate divergences from
sibling conventions (why Postgres doesn't use `SERIAL` for `EntryID`, why
Mongo requires a replica set unconditionally, `Verify()`'s two-check
design) before "fixing" something that looks inconsistent with `grcache`.

**Precise, non-aspirational claim (preserved everywhere — code comments,
README, docs.go, SECURITY.md):** hash-chaining proves internal consistency
(nothing was altered/removed without `Verify()` detecting it) but does
**not** prove *who* wrote an entry beyond the caller-supplied `ActorID`, and
does **not** protect against a privileged attacker with direct DB access
regenerating the entire chain from scratch. Do not let "tamper-evident" read
as "cryptographically un-forgeable" in any documentation change.

## Commands

```sh
make test             # go test -cover ./...
make race             # go test -race ./...  (mandatory before any commit touching the hash-chain or serialization code)
make coverage         # HTML coverage report
make coverage-check   # verify each package independently meets 80% coverage
make bench            # go test -bench=. -benchmem -benchtime=10s ./...
make lint             # golangci-lint run ./...
make vet              # go vet ./...
make fmt              # gofmt
make release VERSION=vX.Y.Z   # tag, push, goreleaser release --clean
make goreleaser-check         # dry run: goreleaser check + --snapshot --clean
```

Run a single test: `go test -run TestConformance/ConcurrentRecordStress ./postgres/...` (or `./memory/...`, `./mongo/...`).

### Backend tests require live local services

`postgres` and `mongo` need a real running service — no mocks, mirroring
every sibling repo's testing philosophy. `memory` needs nothing.

```sh
docker run -d --name graudit-postgres -p 5432:5432 \
  -e POSTGRES_USER=postgres_user -e POSTGRES_PASSWORD=postgres_password postgres:16
createdb -U postgres_user -h localhost graudit_test

# No auth env vars — MONGO_INITDB_ROOT_USERNAME/PASSWORD enables auth,
# which requires a keyFile once --replSet is also set. Unnecessary for a
# local test replica set.
docker run -d --name graudit-mongo -p 27018:27017 mongo:7 --replSet rs0
docker exec graudit-mongo mongosh --eval 'rs.initiate()'

# A second, genuinely standalone (no --replSet) instance is also required —
# TestNewMongoAuditLog_RequiresReplicaSet in mongo/mongo_test.go needs it,
# and must never be skipped/reverted to skipping: a pre-v0.1.0 audit found
# that probeTransactionSupport's original no-op transaction body never sent
# a real transaction-start command to the server, so it silently reported
# "transactions supported" even against a standalone deployment — this
# test is the only thing that catches that class of bug. Fixed by making
# the probe perform a real (self-cleaning) write inside the transaction.
docker run -d --name graudit-mongo-standalone -p 27019:27017 mongo:7
```

The Mongo backend **requires** the instance to be a replica set (single-node
is sufficient) — `NewMongoAuditLog` fails fast, wrapping
`ErrReplicaSetRequired`, against a standalone instance. This is a hard
requirement (correctness, not just consistency-under-load — see
`docs/architecture.md`), unlike gourdiantoken's optional `useTransactions
bool` escape hatch.

## Architecture

- **Root package (`graudit`)** — `audit.go` (`AuditLog` interface,
  `AuditEvent`, `EntryID`, `QueryFilter`, `VerifyResult`,
  `GenesisPrevHash`); `hash.go` (`ComputeHash` — exported so all three
  backends compute hashes identically — plus the canonical-JSON encoder
  isolated for independent testing); `diff.go` (`RecordChange`'s
  before/after diff engine, `ChangeDiff`/`FieldDiff`, `BuildChangeEvent`);
  `errors.go` (sentinels); `logger.go` (optional structural `Logger`
  interface, `NopLogger`/`OrNop`); `events.go` (`TopicAuditRecorded`,
  `PublishRecorded` — the grevents integration every backend calls once);
  `docs.go` (package godoc only). All stdlib-only except `events.go`
  (imports `grevents` for the `Bus`/`Event` types).
- **`memory/`** — test/dev only, never for anything you need to keep. A
  single `sync.Mutex` is both the storage guard and the chain's
  serialization point. Takes functional options (`WithLogger`,
  `WithEventBus`), not a Config struct.
- **`postgres/`** — production-eligible, via GORM. Chain serialization is a
  `pg_advisory_xact_lock` held for the transaction that reads the tail and
  inserts the new entry; `EntryID` is explicitly assigned inside that
  transaction, **never** a `SERIAL`/`BIGSERIAL` column (a sequence advances
  even on rollback, silently creating a gap that would be indistinguishable
  from tampering).
- **`mongo/`** — production-eligible, via `go.mongodb.org/mongo-driver`
  **v1** (not `/v2` — breaking rewrite, out of scope). Chain serialization
  is a multi-document ACID transaction (`session.WithTransaction`) covering
  a singleton chain-state document (`<collection>_chain_state`) and the new
  entry's insert. No TTL index anywhere — unlike `grcache/mongo`, audit
  entries are never expired.
- **`conformance/`** — a shared behavioral test suite
  (`conformance.Run(t, newLog, newLogWithBus, opts...)`) every backend's own
  `_test.go` calls with its own constructor closures. Imports only the root
  `graudit` package, never a backend subpackage (avoids the same
  import-cycle problem grcache's design avoids). 11 scenarios per backend,
  including the two most important tests in the repo:
  `ConcurrentRecordStress` (proves the serialization strategy actually
  works under real concurrent `Record()` calls) and `VerifyDetectsTamper`
  (proves `Verify()`'s tamper-detection claim against a backend-specific
  `WithTamperHook` that bypasses the API entirely — raw SQL for postgres,
  raw driver call for mongo, direct struct mutation for memory).
- **`example/`** — a runnable demo (`go run ./example`, zero setup) against
  the memory backend, including `WithLogger(grlog.NewDefaultLogger())`.
- **Logging** — every backend's `Config` (or, for `memory`,
  `WithLogger(...)`) accepts an optional `graudit.Logger`. `*grlog.Logger`
  satisfies it with no adapter; `grlog` is used only in this module's own
  test files (`logger_test.go`, each backend's `TestWithLogger`/
  `TestNewPostgresAuditLog_FullConfig`-style tests) to prove this, and is
  never a dependency of any backend's non-test code.
- **grevents** — a real functional dependency, not just a borrowed pattern:
  every backend calls `graudit.PublishRecorded` once after its durable
  write commits, publishing one `"audit.recorded"` event (two segments,
  matching the real `resource.pastTenseVerb` convention). A nil/
  unconfigured `EventBus` or a publish failure never fails `Record` — the
  durable write is the guarantee, grevents delivery is best-effort.

## Testing conventions

Real local services, no mocks, `-race` mandatory (the concurrent-write
stress test and every backend's serialization strategy depend on it).
Coverage is checked **per-package**, not just in aggregate — `make
coverage-check` fails if any single package (root, `memory`, `postgres`,
`mongo`) drops below 80%. `conformance` and `example` are excluded (no
`_test.go` of its own / not library code under test).

## Repo conventions

- Every `.go` file (and the `Makefile`) starts with a `// File:
  <relative-path>` header line, maintained by the `bark` tool
  (`.bark.toml`). Run `bark check` before committing; `bark tag` to fix.
- `docs/plan/graudit-plan.md` is the original scope/spec document this repo
  was built from; treat it as historical context, not a live source of
  truth for current behavior (the actual code, this file, and
  `docs/architecture.md` are authoritative) — matching how grcache's and
  grevents' own CLAUDE.md files treat their plan docs once code exists.
- Sentinel errors: `errors.Is`-compatible, defined once in `errors.go`, no
  `IsX(err) bool` helper functions.
- `Close()` is idempotent (`sync.Once` + `atomic.Bool`), matching every
  sibling repo.
- No `.github/workflows/` — consistent with every sibling repo's actual
  state (a real ecosystem gap, not something to fix here specifically).
