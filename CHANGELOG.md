# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
