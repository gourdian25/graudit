# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
