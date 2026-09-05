# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.7.0] - 2026-09-05

Migrates the Mongo backend from `go.mongodb.org/mongo-driver` (v1,
upstream-deprecated) to `go.mongodb.org/mongo-driver/v2`, matching
grsentry's already-completed migration and bringing graudit in line with
the rest of the `gourdian25` org. `MongoConfig`/`NewMongoAuditLog` never
exposed a driver type in their exported signature — every `*mongo.Client`/
`*mongo.Collection` reference is confined to unexported fields and
unexported helper functions (`probeTransactionSupport`,
`ensureAuditIndexes`) — so this is an internal dependency swap only, no
change to `NewMongoAuditLog`'s signature or behavior for graudit's own
consumers.

### Changed

- `mongo.go` now imports `go.mongodb.org/mongo-driver/v2/{bson,mongo,mongo/options,mongo/readpref}`.
  `mongo.Connect` dropped its `context.Context` parameter in v2 (it never
  blocked on the network — `Ping` remains the real connectivity check), so
  the 5s connect timeout that used to bound `Connect` now bounds the
  subsequent `Ping` call instead.
- `mongo.SessionContext` is removed in v2 — `session.WithTransaction`'s
  callback now takes a plain `context.Context` rather than the old wrapper
  type, with the session travelling inside the context itself.
  `probeTransactionSupport` and `Record` (the two `WithTransaction` call
  sites) are rewritten accordingly; every inner `InsertOne`/`DeleteOne`/
  `FindOne`/`ReplaceOne` call inside those closures now takes the
  callback's context parameter directly instead of a session-context
  variable. Behavior is unchanged — `session.WithTransaction` still retries
  internally on `TransientTransactionError`/`UnknownTransactionCommitResult`,
  as before.
- `client.Ping(ctx, readpref.Primary())`, `readpref.Primary()`, and
  `options.Replace().SetUpsert(true)` are unchanged.
- `go.mod`: `go.mongodb.org/mongo-driver v1.17.9` replaced with
  `go.mongodb.org/mongo-driver/v2 v2.8.0`; no longer present at any version,
  direct or indirect.

### Documentation

- `docs.go`'s MongoDB backend description and `mongo.go`'s own file-header
  comment (previously explaining why graudit stayed on v1: "a breaking API
  rewrite, out of scope for a routine dependency choice") both updated to
  describe the completed v2 migration. `CLAUDE.md`'s equivalent v1 caveat
  removed the same way.

## [0.6.0] - 2026-08-14

**Breaking, for the Postgres backend only.** Closes a gap found while
wiring `NewPostgresAuditLog` into a real consumer using a deliberately
least-privilege runtime role: schema auto-apply on every connect required
`CREATE` on the target schema, which that role doesn't have, so
construction failed every time with `permission denied for schema ...` —
and this can't be worked around by pre-creating the tables some other way,
since `CREATE TABLE IF NOT EXISTS` still checks `CREATE` privilege before
checking whether the table exists, so the attempt fails regardless of who
creates the table or how.

Rather than add a flag to opt out of auto-apply on a per-call basis,
`NewPostgresAuditLog` now **never** applies schema at all — see
[docs/architecture.md](docs/architecture.md)'s "`NewPostgresAuditLog`
never applies its own schema" section for the full pattern and rationale
(graudit doesn't use gourdiantoken's dedicated-`docs/postgres.md`-per-backend
convention; every backend divergence, this one included, is documented in
`docs/architecture.md` instead).

No module path change: Go doesn't require a version-suffixed import path
until a `v2` major release, and graudit is still pre-1.0 — this ships as a
`0.x.y` breaking change under the same "allowed pre-1.0" precedent as the
`[0.3.0]`/`[0.4.0]` entries below, not a compatibility break requiring a
new import path the way gourdiantoken's own `v2.x` breaking releases have
had to reason about.

### Changed

- **`NewPostgresAuditLog(cfg)` no longer applies schema.** Signature is
  unchanged, but its behavior is: construction now only pings the pool (the
  caller-supplied `PostgresConfig.Pool`) or dials-then-pings one (from
  `PostgresConfig.DSN`). Apply the schema yourself, once, before
  constructing — see Migration below. Calling this against a database
  where `graudit_entries` doesn't exist yet no longer fails construction
  itself; it fails on the first actual `AuditLog` method call (`Record`,
  `Query`, ...) with a plain Postgres "relation does not exist" error
  instead. The schema itself is unchanged (still `CREATE TABLE/INDEX IF
  NOT EXISTS`, still the same `graudit_entries` table and its four
  indexes).

### Added

- **`PostgresSchemaSQL() string`**, returning graudit's Postgres schema
  (`internal/postgresdb/schema.sql`, unchanged) as text, for applying
  through your own project's migration tool (golang-migrate, Flyway, a
  plain SQL file run in CI, whatever you already use) — see
  docs/architecture.md.

### Migration (only if your Postgres role doesn't already have `CREATE`)

If your Postgres role already has `CREATE` on the target schema (the
common case for a dev database or a single-role deployment), nothing
about your setup actually breaks except the timing: apply
`PostgresSchemaSQL()` once, via any method (even a one-off `psql -f`
piping its output), before your application first connects, and
everything else works exactly as before.

If your application connects with a least-privilege, migrations-excluded
role (the setup this release exists for), add a step to your own
migration tooling that applies `graudit.PostgresSchemaSQL()`'s text
against the target database, run once by whatever role already owns your
other migrations, before deploying a graudit-using binary against it.

### Testing

- `postgres_test.go`'s `truncatePostgresTestDB` (graudit's own Postgres
  test setup, not a real consumer) now explicitly calls the unexported
  `applyPostgresSchema` immediately after its `DROP TABLE IF EXISTS`,
  since `NewPostgresAuditLog` no longer does this implicitly. Order matters:
  applying schema *before* the drop would just recreate the table and then
  immediately discard it again, leaving nothing for the constructor to see.
  `internal_coverage_test.go`'s `TestPostgresAuditLog_ConfigTuning` (the one
  test that constructed `NewPostgresAuditLog` directly rather than through
  the shared `newPostgresLog` helper, then called `Record`) gained the same
  schema-ensuring pre-check `TestNewPostgresAuditLog_FullConfig` already
  used, since it could no longer rely on construction implicitly creating
  the table.

## [0.5.0] - 2026-07-30

Adds direct entry lookup and a reliable "verify the whole chain" call
shape, closing two gaps a consumer needing per-entry lookup and
whole-chain verification ran into: fetching one entry required an
unbounded `Query` plus a linear scan, and `Verify`'s `to` parameter had no
sentinel for "the end" — its Go zero value matched zero rows and returned
`Valid: true` vacuously, indistinguishable from a real full-chain pass.
Contains a **breaking change** (allowed pre-1.0) to `Verify`'s `to==0`
behavior; see Changed below.

### Added

- **`AuditLog.GetEntry(ctx, chainID string, id EntryID) (AuditEvent,
  error)`** — fetches a single entry by its position within `chainID` via a
  direct indexed/keyed lookup on every backend (postgres: the
  `(chain_id, entry_id)` primary key; mongo: the existing compound unique
  index; memory: a linear scan, matching `Query`'s own documented
  rationale for this test/dev-only backend) — O(1) on the two
  production-eligible backends, never a `Query`-and-scan. Returns
  `ErrEntryNotFound` for an `id` that doesn't exist in `chainID`.
- **`AuditLog.LatestEntryID(ctx, chainID string) (EntryID, error)`** —
  resolves `chainID`'s current tail `EntryID`, reusing each backend's
  existing tail-tracking lookup (memory's `memoryChainTail`, postgres's
  `GetLastEntry` query, mongo's chain-state singleton document) rather than
  a new one. Returns `ErrEntryNotFound` for a `chainID` with no entries
  recorded yet.
- **`VerifyResult.Empty bool`** — true only when `chainID` has zero entries
  recorded at all (independent of the requested range), letting a caller
  that passed `Verify`'s `to==0` sentinel distinguish "nothing to verify
  yet" from a real full-chain pass (both otherwise report `ok=true`).

### Changed

- **Breaking:** `Verify`'s `to` parameter gains a sentinel meaning "through
  the current latest entry": `to == 0` (never a real `EntryID`, which
  starts at 1) now resolves to `chainID`'s tail — the same position
  `LatestEntryID` returns — instead of matching zero rows. Existing code
  that called `Verify(ctx, chainID, from, 0)` expecting the old vacuous
  `Valid: true` result over an empty range will now actually verify the
  whole chain; an explicit non-zero `to` is unaffected and still selects a
  specific sub-range exactly as before.
- `ErrEntryNotFound` (defined since `v0.1.0` but never actually returned by
  any method until now) is returned for the first time: by `GetEntry` for a
  nonexistent `id`, and by `LatestEntryID`/`Verify`'s `to==0` resolution for
  a `chainID` with no entries recorded yet.

## [0.4.0] - 2026-07-28

Multi-chain support: one `AuditLog` instance (one connection pool) can now
serve any number of independent hash chains — e.g. one per tenant in a
multi-tenant deployment, plus a separate chain for platform-operator
actions — instead of exactly one global chain per instance. Also adds
`PostgresConfig.Pool` injection. Contains **breaking changes** (allowed
pre-1.0); confirmed no existing graudit-backed deployment needed to be
preserved across this release, so no migration tooling ships (see
Migration below).

### Added

- **`ChainID`** is now a required field/parameter throughout the public
  API: `AuditEvent.ChainID` (first field), `QueryFilter.ChainID`,
  `RecordChange(ctx, chainID, actorID, entityType, entityID, before,
  after)`, `Verify(ctx, chainID, from, to)`. `EntryID` sequences and
  `PrevHash` linkage are now tracked per `ChainID`, not globally. There is
  no wildcard/query-all escape hatch — an empty `ChainID` fails loud
  (`ErrChainIDRequired`) rather than silently matching every chain, since
  a cross-tenant leak in an audit trail is worse than the ergonomic cost
  of always specifying one chain. See
  [docs/architecture.md](docs/architecture.md)'s "Multi-chain support"
  section.
- New sentinel `ErrChainIDRequired`, returned (dual-wrapped with
  `ErrInvalidEvent` from `Record`'s `Validate()` path) whenever a
  `ChainID`/`chainID` is empty.
- **`PostgresConfig.Pool`** — an already-open `*pgxpool.Pool` can now be
  supplied instead of `DSN`, letting graudit share a pool the rest of the
  application already owns rather than dialing its own per `AuditLog`
  instance. Exactly one of `DSN`/`Pool` is required. graudit never closes
  a pool it didn't dial itself (`Close()` is a no-op on the pool when
  `Pool` was supplied). Mirrors grnoti's own `PostgresConfig.Pool`.
- This repo's first `Benchmark*` function,
  `BenchmarkPostgresAuditLog_Record_ChainConcurrency`, comparing
  concurrent `Record` throughput on one shared chain against many
  independent chains.

### Changed

- **Breaking:** `ComputeHash` (exported) gains a new leading `chainID
  string` parameter, included first in the hash preimage. This is not
  cosmetic: without `chainID` in the preimage, an attacker with direct
  database access could rewrite one entry's `chain_id` column — splicing
  it from one chain into another — without invalidating its stored
  `Hash`, since `EntryID` sequences independently restart at 1 in every
  chain and two entries from different chains could otherwise share an
  identical `(EntryID, ActorID, EntityType, EntityID, Action, Payload,
  Timestamp, PrevHash)` tuple. `GenesisPrevHash` did not need to become
  chain-specific for the same reason: `chainID` is in every entry's
  preimage including entry #1's, so two chains' genesis entries sharing
  that one well-known `PrevHash` constant still hash differently.
- **Breaking:** `BuildChangeEvent` (exported) gains a new leading
  `chainID string` parameter.
- **Breaking:** `QueryFilter.ChainID` is now required — previously a
  zero-value `QueryFilter` matched every entry; now `Query`/`Verify` both
  return `ErrChainIDRequired` for an empty `ChainID`/`chainID` on every
  backend.
- Postgres: `Record`'s `pg_advisory_xact_lock` narrowed from a single
  global key to Postgres's two-`int32` `pg_advisory_xact_lock(key1, key2)`
  overload — `key1` stays the fixed `chainLockKey` namespace, `key2` is an
  FNV-1a 32-bit hash of `chainID` — so concurrent `Record` calls on
  different chains no longer serialize against each other; same-chain
  calls still fully serialize as before. See
  [docs/architecture.md](docs/architecture.md)'s "Postgres advisory-lock
  chain scoping" section.
- Schema: `internal/postgresdb/schema.sql`'s `graudit_entries` table gains
  a `chain_id TEXT NOT NULL` column; the primary key becomes `(chain_id,
  entry_id)`; the `actor`/`entity`/`timestamp` indexes become composite
  with a leading `chain_id`.
- Mongo: `entryDocument` gains a `chainId` field; the unique index becomes
  compound `{chainId:1, entryId:1}`; the other three indexes gain a
  leading `chainId` component; each chain's chain-state singleton
  document is now keyed by the real `chainID` string instead of a fixed
  `"tail"` sentinel.

### Migration (only if upgrading an existing pre-0.4.0 deployment)

No automated migration tooling ships with this release, matching the
`[0.3.0]` precedent — greenfield deployments need no action.

- **Postgres:** there is no `ALTER TABLE` path — `CREATE TABLE IF NOT
  EXISTS` no-ops against an existing, differently-shaped `graudit_entries`
  table. An existing deployment needs manual recreation (export any data
  you need to keep first).
- **Mongo:** the legacy unique index `{entryId:1}` must be manually
  dropped (`db.graudit_entries.dropIndex("entryId_1")`) before upgrading —
  left in place alongside the new compound `{chainId:1, entryId:1}` index,
  it would incorrectly reject every second chain's `entryId=1`.
  `ensureAuditIndexes` deliberately does not auto-detect and drop this
  index itself, since an unconditional drop would error against a fresh
  deployment where the index never existed.

### Testing

- Coverage: 95.5% on the root package (up from 95.2%).
- `contract_audit_test.go` gained three new scenarios run against all
  three backends: `ChainIsolation` (two chains on one instance, each
  `EntryID` sequence independently starting at 1, no cross-chain leakage
  via `Verify`/`Query`), `ChainIsolationTamperContainment` (tampering one
  chain must never affect another chain's `Verify` result), and
  `ConcurrentRecordStressMultiChain` (concurrent `Record` calls
  interleaved across chains stay gap-free/duplicate-free per chain).

## [0.3.0] - 2026-07-23

Ecosystem-wide Stage 3 pass: flattened to a single package, GORM removed,
Mongo backend adopted the workspace-standard authenticated replica set,
a real gap in `Verify()`'s test coverage closed, and coverage raised.
Contains **breaking changes** (allowed pre-1.0).

### Changed

- **Breaking:** `Logger`'s three printf-style methods (`Infof`/`Warnf`/
  `Errorf(format string, args ...interface{})`) replaced with four
  `log/slog`-shaped methods (`Debug`/`Info`/`Warn`/`Error(msg string, args
  ...any)`), matching `*slog.Logger`'s own signatures exactly so any
  slog-based logger — including `*grlog.Logger` via
  `slog.New(grlog.NewSlogHandler(...))` — satisfies it with no adapter.
  Consistent with the same change landing across
  grcache/grevents/grpolicy/grnoti/gourdiantoken in this pass. Real
  structured field values (previously flattened into printf format
  strings) now reach any structured-output logger intact.
- **Breaking:** flattened from one root package plus a subpackage per
  backend (`graudit/memory`, `graudit/postgres`, `graudit/mongostore`)
  into a single flat package, matching every other repo in the gourdian
  ecosystem's convention (see `docs/architecture.md`). Every backend's
  `New<Backend>AuditLog` constructor and `<Backend>Config` type now live
  directly in `github.com/gourdian25/graudit` — update imports from e.g.
  `github.com/gourdian25/graudit/postgres` + `postgres.NewPostgresAuditLog(...)`
  to `github.com/gourdian25/graudit` + `graudit.NewPostgresAuditLog(...)`.
  This also resolves the reason `graudit/mongo` was renamed to
  `graudit/mongostore` in `v0.2.0` (avoiding a package-name collision with
  `go.mongodb.org/mongo-driver/mongo`) — as a file within one flat package
  rather than a separate importable package, `mongo.go` no longer
  collides with anything, so the file (and its internal error-message
  prefix) reverts to the shorter, clearer `mongo` name.
- **Breaking:** `memory`'s functional-option type renamed from `Option` to
  `MemoryOption` (`WithLogger`/`WithEventBus` stay unprefixed) — an
  overly generic name at the root-package level now that all three
  backends share one package.
- **Breaking:** the PostgreSQL backend (`NewPostgresAuditLog`) no longer
  uses GORM — rewritten on `pgx/v5` with sqlc-generated queries (see
  `internal/postgresdb`), matching gourdiantoken's, grnoti's, and
  grcache's own Postgres backend pattern. `entry_id` is still explicitly
  assigned inside the same `pg_advisory_xact_lock`-held transaction as
  before (never a `BIGSERIAL` column — see `docs/architecture.md`); a
  *second*, distinct advisory lock (`grauditSchemaLockKey`) now
  serializes schema application at connect time, the same pattern
  grcache adopted for its own GORM removal.
- The Mongo backend's own tests (and this repo's documented connection
  settings) now use the workspace-standard **authenticated** single-node
  replica set (`root`/`mongo_password` on port `27018`), matching
  grcache's and gourdiantoken's own test setup, instead of the previous
  no-auth connection string. The separate, deliberately no-auth
  standalone container (port `27019`) used only by
  `TestNewMongoAuditLog_RequiresReplicaSet` is unchanged.
- Folded the standalone `conformance` package into the root package as
  `contract_audit_test.go` (`runAuditContract`, run via
  `TestAuditLog_Contract`'s per-backend subtests), matching the rest of
  the gourdian ecosystem's convention.
- Every networked backend's test factory now skips gracefully (`t.Skipf`)
  rather than failing hard when its live service isn't reachable, matching
  the rest of the gourdian ecosystem's convention.
- `make coverage-check`'s threshold raised from 80% to 95%, and it now
  checks only the root package (previously iterated over one directory
  per backend subpackage, including a stale `./mongo` entry that never
  matched the actual `mongostore` directory name and so silently reported
  "no coverage output" for it on every run — moot now that there's only
  one package to measure).

### Fixed

- `Verify()`'s "Check B" (chain-linkage integrity: each entry's stored
  `PrevHash` must equal the immediately preceding entry's stored `Hash`)
  had no dedicated regression test on any backend — the shared contract
  suite's `VerifyDetectsTamper` scenario only ever corrupts a stored
  entry's `Payload`, which exercises "Check A" (per-entry hash integrity)
  but never touches `PrevHash`. The implementation itself was already
  correct (confirmed by reading the code before writing new tests); this
  was a test-coverage gap, not a behavioral bug. Each backend now has its
  own `Test<Backend>AuditLog_VerifyDetectsChainLinkageBreak` test.
- `encodeCanonical`'s two `json.Marshal`-of-a-plain-Go-string error checks
  (one for object keys, one for string values) were dead code — marshaling
  a Go `string` can never fail — removed rather than defended, per the
  "don't add error handling for scenarios that can't happen" convention.

### Testing

- Coverage raised to 95.2% on the root package (previously 89.9%
  aggregate before this pass, measured per-package rather than
  aggregate), closing gaps in every backend's `ErrBackendUnavailable`-
  wrapping branches (via a new white-box `internal_coverage_test.go` that
  closes/disconnects each backend's underlying pool/client directly, the
  same technique used throughout the gourdian ecosystem), each backend's
  own `event.Validate()`-failure branch inside `Record` called directly
  (previously only reachable indirectly through `RecordChange`, which
  never actually exercises it), and a handful of branches only reachable
  via deliberate fault injection against a live service — a Postgres
  `CHECK (false)` constraint rejecting an insert, a MongoDB collection
  validator rejecting the chain-state upsert, and a table dropped out
  from under an already-open connection pool.
- A small number of statements are documented as permanently unreachable
  rather than force-covered — e.g. `DecodeStoredPayload`'s error branch on
  the Postgres backend, since a `jsonb` column guarantees valid JSON at
  the type level, so SQL-level corruption can never produce an
  undecodable stored payload on that backend (see the comment in
  `postgres_test.go`).

### Documentation

- README: added a Contributing section (previously went straight from
  "Out of scope" to "License"), pointing at the same `fmt`/`vet`/`lint`/
  `test`/`race`/`coverage-check` Makefile targets grcache's own
  Contributing section documents.
- README: stated the precise root-package coverage number (95.2%, via
  `make coverage-check`) near Testing, previously unstated.
- README: linked `SECURITY.md` and `CHANGELOG.md` from the closing section
  (neither was previously linked).
- README: added a short "Why this shape" note near Backends explaining the
  flattened-package, GORM-removed history, pointing at this entry for the
  full rationale rather than duplicating it.

## [0.2.0] - 2026-07-10

Ecosystem-alignment pass ahead of `grauth`. Contains a **breaking change**
(allowed pre-1.0).

### Changed

- **Breaking:** `graudit/mongo` renamed to `graudit/mongostore`. The bare
  `mongo` package name collided with the upstream driver's own package
  (`go.mongodb.org/mongo-driver/mongo` also declares `package mongo`),
  forcing every consumer that imports both in the same file (as `grauth`
  will need to) to alias one manually. Update imports from
  `github.com/gourdian25/graudit/mongo` to
  `github.com/gourdian25/graudit/mongostore`; the package's exported API
  (`NewMongoAuditLog`, `MongoConfig`, ...) is otherwise unchanged.
- Dependency alignment ahead of `grauth`: bumped `jackc/pgx/v5` (v5.6.0 →
  v5.10.0, closes reachable vulnerabilities GO-2026-5004/4772/4771),
  `golang.org/x/crypto` (v0.31.0 → v0.53.0, clears stale SSH/openpgp
  advisories), `klauspost/compress`, `montanaflynn/stats`, `xdg-go/scram`,
  `golang.org/x/sync`, `golang.org/x/text` to match the versions already
  used by `grcache`, to avoid two versions of the same library in
  `grauth`'s future dependency graph.
- `go.mod`'s `go` directive was already `1.26.4` (no change needed).
- README: added a full "part of the gourdian25 ecosystem" section
  (previously only scattered functional mentions of grlog/grevents).
- Bumped `github.com/gourdian25/grlog` and `github.com/gourdian25/grevents`
  to `v0.1.1`.

## [0.1.0] - 2026-07-09

### Added

- Initial release: `AuditLog` interface (`Record`, `RecordChange`, `Verify`,
  `Query`, `Close`).
- Hash-chained, append-only entries — `Verify(from, to)` detects tampering
  or deletion anywhere in a range via two checks per entry (stored-hash
  recomputation and adjacent stored-`PrevHash` linkage).
- Three backends: `graudit/memory` (test/dev only, `sync.Mutex`-serialized),
  `graudit/postgres` (`pg_advisory_xact_lock`-serialized, explicit
  non-`SERIAL` `EntryID`), `graudit/mongo` (transaction-serialized,
  replica-set required).
- `RecordChange` diff engine (`ChangeDiff`/`FieldDiff`).
- `grevents` integration: publishes one `"audit.recorded"` event per
  successful write; a nil/unconfigured bus or a publish failure never
  fails `Record`.
- Deterministic canonical JSON encoding for hash computation (sorted keys,
  `UseNumber()`-based numeric round-tripping, fixed `RFC3339Nano` UTC
  timestamps).
- Shared `conformance` test suite run against all three backends:
  concurrent-`Record()` stress test, deliberate-tamper test, hash
  determinism test, genesis-entry test, grevents publish/publish-failure
  tests.
