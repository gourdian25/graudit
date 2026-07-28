# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

graudit (`github.com/gourdian25/graudit`) is an append-only, tamper-evident
audit trail for the gourdian ecosystem — it answers "what changed, who did
it, and can we prove the record hasn't been altered," a different question
from what [grlog](https://github.com/gourdian25/grlog) answers ("what
happened during this request"). It is a **flat, single-package library**
(`package graudit` at the repo root) — no subpackages — matching every
other repo in the gourdian ecosystem's convention (graudit originally split
each backend into its own importable subpackage, like `grcache` did, to
keep unused client libraries out of a consumer's build; that layout was
reversed for ecosystem-wide consistency, see `docs/architecture.md`).
There is nothing to build or run, only lint and test.

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
make coverage-summary # coverage by function (go tool cover -func)
make coverage-check   # verify the root package meets 95% coverage
make bench            # go test -bench=. -benchmem -benchtime=10s ./...  (no Benchmark* funcs exist yet — builds/passes but times nothing)
make lint             # golangci-lint run ./...
make vet              # go vet ./...
make fmt              # gofmt
make docker-up        # start the shared Postgres/Mongo(auth)/Mongo(standalone) test containers (idempotent)
make docker-down      # stop those containers (state preserved for a fast restart)
make release VERSION=vX.Y.Z   # tag, push, goreleaser release --clean
make goreleaser-check         # dry run: goreleaser check + --snapshot --clean
```

Run a single test: `go test -run TestAuditLog_Contract/Postgres/ConcurrentRecordStress .` (swap `Postgres` for `Memory`/`Mongo`).

### Backend tests require live local services

The postgres and mongo backends need a real running service — no mocks,
mirroring every sibling repo's testing philosophy. The memory backend
needs nothing. These are **shared across the whole gourdian25 workspace**
(grnoti, graudit, grcache, gourdiantoken all test against the same running
Postgres/Mongo instances, each using its own database), with one deliberate
exception: the second, standalone Mongo container below exists only for
this repo's own replica-set-required regression test. Run `make docker-up`
to start (or reuse already-running) shared containers.

```sh
docker run -d --name gourdian-postgres -p 5432:5432 \
  -e POSTGRES_USER=postgres_user -e POSTGRES_PASSWORD=postgres_password postgres:16
createdb -U postgres_user -h localhost graudit_test

# Auth + --replSet requires a --keyFile (MongoDB enforces this even for a
# single-node set) — generate one once, in a named volume so file
# permissions/ownership survive correctly across container recreation:
docker volume create gourdian-mongo-keyfile
docker run --rm -v gourdian-mongo-keyfile:/keyfile-dir mongo:7 bash -c \
  "openssl rand -base64 756 > /keyfile-dir/mongo-keyfile && chmod 400 /keyfile-dir/mongo-keyfile && chown 999:999 /keyfile-dir/mongo-keyfile"
docker run -d --name gourdian-mongo-auth -p 27018:27017 \
  -e MONGO_INITDB_ROOT_USERNAME=root -e MONGO_INITDB_ROOT_PASSWORD=mongo_password \
  -v gourdian-mongo-keyfile:/etc/mongo-keyfile-dir \
  mongo:7 --replSet rs0 --keyFile /etc/mongo-keyfile-dir/mongo-keyfile
docker exec gourdian-mongo-auth mongosh -u root -p mongo_password \
  --authenticationDatabase admin --eval 'rs.initiate()'

# A second, genuinely standalone (no --replSet, no auth) instance is also
# required — TestNewMongoAuditLog_RequiresReplicaSet in mongo_test.go needs
# it, and must never be skipped/reverted to skipping: a pre-v0.1.0 audit
# found that probeTransactionSupport's original no-op transaction body
# never sent a real transaction-start command to the server, so it
# silently reported "transactions supported" even against a standalone
# deployment — this test is the only thing that catches that class of
# bug. Fixed by making the probe perform a real (self-cleaning) write
# inside the transaction.
docker run -d --name gourdian-mongo-standalone -p 27019:27017 mongo:7
```

The Mongo backend **requires** the instance to be a replica set (single-node
is sufficient) — `NewMongoAuditLog` fails fast, wrapping
`ErrReplicaSetRequired`, against a standalone instance. This is a hard
requirement (correctness, not just consistency-under-load — see
`docs/architecture.md`), unlike gourdiantoken's optional `useTransactions
bool` escape hatch.

**Note:** the primary (non-standalone) Mongo container above uses the
same authenticated single-node replica set every other gourdian25 repo's
test suite connects to — `mongo_test.go`'s own `mongoTestURI` embeds the
`root`/`mongo_password` credentials directly
(`mongodb://root:mongo_password@localhost:27018/?directConnection=true`),
matching grcache's and gourdiantoken's own test connection strings exactly.

## Architecture

- **Root package (`graudit`)** — `audit.go` (`AuditLog` interface,
  `AuditEvent`, `EntryID`, `QueryFilter`, `VerifyResult`,
  `GenesisPrevHash`); `hash.go` (`ComputeHash` — exported so all three
  backends compute hashes identically — plus the canonical-JSON encoder and
  the shared `marshalPayload`/`DecodeStoredPayload` pair, isolated for
  independent testing); `diff.go` (`RecordChange`'s before/after diff
  engine, `ChangeDiff`/`FieldDiff`, `BuildChangeEvent`); `errors.go`
  (sentinels); `logger.go` (optional structural `Logger` interface,
  `NopLogger`/`OrNop`); `events.go` (`TopicAuditRecorded`,
  `PublishRecorded` — the grevents integration every backend calls once);
  `docs.go` (package godoc only). All stdlib-only except `events.go` and
  each backend file (see below).
- **`memory.go`** (`memoryAuditLog`) — test/dev only, never for anything
  you need to keep. A single `sync.Mutex` is both the storage guard and
  the chain's serialization point. Takes functional options
  (`MemoryOption`: `WithLogger`, `WithEventBus`), not a Config struct.
- **`postgres.go`** (`postgresAuditLog`) — production-eligible, via pgx/v5
  with sqlc-generated queries (see `internal/postgresdb`, generated from
  `internal/postgresdb/schema.sql`/`queries/audit.sql` via `sqlc generate`
  — never hand-edit generated files), replacing an earlier GORM
  implementation. Chain serialization is a `pg_advisory_xact_lock` held
  for the transaction that reads the tail and inserts the new entry;
  `EntryID` is explicitly assigned inside that transaction, **never** a
  `BIGSERIAL` column (a sequence advances even on rollback, silently
  creating a gap that would be indistinguishable from tampering). Schema
  is applied on connect via an embedded `//go:embed` schema string,
  serialized by a *separate* Postgres advisory lock
  (`grauditSchemaLockKey`, distinct from the per-Record `chainLockKey`).
- **`mongo.go`** (`mongoAuditLog`) — production-eligible, via
  `go.mongodb.org/mongo-driver` **v1** (not `/v2` — breaking rewrite, out
  of scope). Chain serialization is a multi-document ACID transaction
  (`session.WithTransaction`) covering a singleton chain-state document
  (`<collection>_chain_state`) and the new entry's insert. No TTL index
  anywhere — unlike `grcache`'s Mongo backend, audit entries are never
  expired. This file was previously the standalone `graudit/mongostore`
  package (renamed away from the bare `mongo` name only because it
  collided with the upstream driver's own `package mongo` for a consumer
  importing both in the same file); as a file within one flat package
  rather than a separate importable package, that collision no longer
  applies, so the shorter `mongo` name (file name and error-message
  prefix) is safe again — matching grcache's own `mongostore` → `mongo`
  reversion during its own flattening.
- **`contract_audit_test.go`** — the shared behavioral test suite
  (`runAuditContract`, run via `TestAuditLog_Contract`'s per-backend
  subtests: `Memory`/`Postgres`/`Mongo`, each skipping gracefully if its
  live service isn't reachable). This was originally a separate,
  publicly-importable `conformance` package — folded into the root
  package's own tests for ecosystem consistency; see
  `docs/architecture.md`'s closing section. 11 scenarios per backend,
  including the two most important tests in the repo:
  `ConcurrentRecordStress` (proves the serialization strategy actually
  works under real concurrent `Record()` calls) and `VerifyDetectsTamper`
  (proves `Verify()`'s Check-A tamper-detection claim against a
  backend-specific `withTamperHook` that bypasses the API entirely — raw
  SQL for postgres, raw driver call for mongo, direct struct mutation for
  memory). `Verify()`'s *other* check (Check B, chain-linkage integrity)
  is proven separately per backend in `internal_coverage_test.go`'s
  `Test<Backend>AuditLog_VerifyDetectsChainLinkageBreak` tests, since the
  contract suite's tamper hook only ever corrupts `Payload`, not
  `PrevHash`.
- **Per-backend test files** (`memory_test.go`, `postgres_test.go`,
  `mongo_test.go`) — each supplies its own `new<Backend>Log`/
  `new<Backend>LogWithBus`/`tamper<Backend>Entry` factories to the
  contract suite and adds backend-specific tests for anything that can't
  be expressed generically (connection failures, replica-set requirement,
  schema/index idempotency, malformed stored payloads).
- **`internal_coverage_test.go`** (white-box, still `package graudit`) —
  reaches into each networked backend's unexported concrete type
  (`postgresAuditLog.pool`, `mongoAuditLog.client`) to close/disconnect it
  directly while the `AuditLog` object itself still believes it's open,
  deterministically covering every method's `ErrBackendUnavailable`-
  wrapping branch — the same technique used throughout the gourdian
  ecosystem (see gourdiantoken's own repository coverage tests). Also
  covers a handful of branches only reachable via direct fault injection
  against a live service (a Postgres `CHECK (false)` constraint rejecting
  an insert, a MongoDB collection validator rejecting the chain-state
  upsert, a table dropped out from under an open pool) — each documented
  inline with why the branch can't be reached any other way.

## Testing conventions

Real local services, no mocks, `-race` mandatory (the concurrent-write
stress test and every backend's serialization strategy depend on it).
Coverage is checked on the root package only (`.`, not `./...` —
`internal/postgresdb` is sqlc-generated and `example/` is a runnable demo,
neither is library code under test) — `make coverage-check` fails if it
drops below 95%. A small number of branches are accepted as permanently
unreachable rather than force-covered (see comments at each site in
`hash.go`/`postgres.go`/`mongo.go`/their test files) — e.g. `json.Marshal`
of a plain Go string can never fail, and a Postgres `jsonb` column
guarantees valid JSON at the type level, so `DecodeStoredPayload`'s error
branch is genuinely unreachable via SQL-level corruption on that backend.

`.golangci.yml` enables `gosec`/`misspell`/`gocritic`/`revive` on top of
the standard set, with two deliberate exclusions: `gosec`/`errcheck`/
`gocritic` are off for `_test.go` files and `example/example.go` (test
doubles and demo code use test-controlled inputs by design), and revive's
`unused-parameter` rule is disabled repo-wide because `AuditLog`/`Logger`/
`grevents.Bus` are fixed interface shapes where dropping an unused
parameter's name would make implementations harder to read against the
interface they satisfy.

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
