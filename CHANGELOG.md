# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Ecosystem-wide Stage 3 pass: flattened to a single package, GORM removed,
Mongo backend adopted the workspace-standard authenticated replica set,
a real gap in `Verify()`'s test coverage closed, and coverage raised.
Contains **breaking changes** (allowed pre-1.0).

### Changed

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
