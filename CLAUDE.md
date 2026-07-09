# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Current state: pre-implementation, and possibly pre-approval

This repository contains **no Go code yet** — `go.mod` exists (module
`github.com/gourdian25/graudit`, Go 1.26.4) and the `bark` file-header tool is
configured (`.bark.toml`), but there are no `.go` files and no Makefile. The
only substantive content is [docs/plan/graudit-plan.md](docs/plan/graudit-plan.md),
a detailed scope/spec document. Read it in full before writing any code —
this file only summarizes it.

**Before anything else, read plan doc §0.** graudit was flagged from the
start as a strong "Defer" candidate — hash-chaining and tamper detection are
architecturally closer to a minimal blockchain than to any sibling repo, and
§0 requires answering three questions (does `gourdianerp` have a real feature
needing this today, is there an actual compliance driver, is this the best
use of time vs. `grauth`/`grpolicy`) before producing an implementation plan.
If asked to "build graudit" or "implement the plan," restate the §0
assessment explicitly first rather than jumping straight to code — that is
what the plan doc itself instructs (§9.1).

## Mandatory pre-implementation research

If §0 concludes this should proceed, the plan requires reading these sibling
repos **in full** first, because graudit is meant to reuse their patterns
rather than reinvent them:

- **`~/Dev/gourdian25/grevents`** — graudit is the first repo in the
  ecosystem with a genuine *functional* dependency on another gourdian repo
  (not just a borrowed pattern): `Record()` must publish an event via
  `Bus.Publish` after a successful write. Confirm the exact `Publish`
  signature and decide the topic name (e.g. `"audit.entry.recorded"`) before
  writing `events.go`.
- **`~/Dev/gourdian25/grcache`** — study its subpackage-per-backend layout
  (why `graudit/postgres` and `graudit/memory` should follow the same
  reasoning: don't force every consumer to pull in a DB driver they don't
  use), its `Close()` idempotency (`sync.Once`), sentinel-error style, optional
  structural `Logger` interface, and `conformance/` shared test suite — all
  four carry over unchanged. Also read its CHANGELOG's `Pipeline` vs
  `TxPipeline` finding: graudit's own "tamper-evident"/"immutable" claims need
  the same scrutiny — don't let the README claim more than `Verify()` actually
  enforces.
- **`~/Dev/gourdian25/grlog`** and **`~/Dev/gourdian25/gourdiantoken`** — for
  sentinel error conventions and the background-sweep-goroutine pattern
  relevant to retention/archival of old audit entries.

Only after these reads should an implementation plan be produced — see plan
doc §9 for the specific questions to answer (serialization strategy choice,
canonical JSON determinism proof, exact grevents topic name, a working
deliberate-tamper test shown before claiming `Verify()` works).

## What graudit is

An append-only, tamper-evident audit trail — it answers "what changed, who
did it, and can we prove the record hasn't been altered," which is a
different question from what `grlog` answers ("what happened during this
request"). It is not a logging library and not a compliance certification
product; it's one component that could contribute to SOC2/HIPAA/PCI
readiness, not the whole solution.

**Precise, non-aspirational claim to preserve everywhere (code comments,
README, doc.go):** hash-chaining proves internal consistency (nothing was
altered/removed without `Verify()` detecting it) but does **not** prove *who*
wrote an entry beyond the caller-supplied `ActorID`, and does **not** protect
against a privileged attacker with direct DB access regenerating the entire
chain from scratch. A hash chain only detects partial tampering, not
wholesale regeneration by someone who controls the storage.

## Planned public API (from the plan doc — subject to revision during implementation)

```go
package graudit

type AuditLog interface {
	Record(ctx context.Context, event AuditEvent) (EntryID, error)
	RecordChange(ctx context.Context, actorID, entityType, entityID string, before, after any) (EntryID, error)
	Verify(ctx context.Context, from, to EntryID) (ok bool, detail VerifyResult, err error)
	Query(ctx context.Context, filter QueryFilter) ([]AuditEvent, error)
	Close() error
}
```

`EntryID` is a strictly increasing `uint64` chain position, not a UUID.
`AuditEvent.Hash`/`PrevHash` are set by `Record`; the genesis entry has
`PrevHash = ""` (or a documented 64-zero-char constant) — `Verify()` must
treat entry #1 as a special case. Full field-level signatures are in plan
doc §4.1.

## The hard problem: single-writer serialization

Each entry's hash depends on the immediately preceding entry's hash, so
concurrent `Record()` calls against the same chain **cannot** run without a
serialization point, or the chain corrupts. Plan doc §4.2 lays out two
candidates and recommends the first:

- **DB-level serialization** (recommended): `SELECT ... FOR UPDATE` on the
  latest-entry row, or `pg_advisory_xact_lock`, inside the same transaction
  that inserts the new entry. Correct even across multiple `gourdianerp`
  replicas talking to the same Postgres instance — an in-process mutex is
  not.
- **Application-level single-writer goroutine**: simple, correct only within
  one process. Fragile if `gourdianerp` ever runs more than one replica, so
  treat this as the memory backend's approach only (plain `sync.Mutex`,
  test/dev-only, single-process by definition), not the Postgres backend's.

This is the single hardest and most safety-critical piece of the repo —
validate the recommendation against the actual Postgres backend rather than
assuming it.

## Hash computation

```
hash = SHA256(entryID || actorID || entityType || entityID || action ||
              canonicalJSON(payload) || timestamp || prevHash)
```

Go's `encoding/json` on `map[string]any` does not guarantee key order across
runs by default — confirm whatever canonical-encoding approach is chosen
actually produces a stable byte sequence for logically-identical payloads
(same keys, different insertion order), or `Verify()` will falsely report
tampering. This determinism claim needs its own explicit test before it's
trusted (plan doc §6).

## Planned architecture

```
graudit/
├── audit.go              // AuditLog interface, AuditEvent, EntryID, QueryFilter, VerifyResult
├── hash.go                // hash computation + canonical JSON encoding, isolated for independent testing
├── diff.go                // snapshot diff engine used by RecordChange
├── errors.go
├── postgres/
│   └── postgres.go        // primary durable backend; owns the serialization strategy above
├── memory/
│   └── memory.go           // test/dev-only backend; sync.Mutex-serialized, never for anything you need to keep
├── events.go               // grevents integration: what gets published, topic naming
└── conformance/
    └── conformance.go       // shared suite: hash-chain integrity, tamper detection, concurrent Record() ordering, Verify() on a deliberately-corrupted chain
```

## Explicitly out of scope for v1 (resist scope creep here)

- Full SOC2/HIPAA/PCI/ISO certification tooling.
- Real-time alerting (that's grevents'/a consumer's job — graudit only
  publishes after recording, never evaluates rules or notifies).
- Auto-instrumentation/middleware that intercepts `grauth` calls to generate
  entries automatically — every entry comes from an explicit `Record()` call.
- Storage backends beyond Postgres + in-memory (no Mongo, no Redis-backed
  audit storage — an audit trail in a cache that can evict entries is a
  contradiction).
- Cryptographic per-entry signatures (true non-repudiation) — see the
  precise claim above about what hash-chaining does and doesn't prove.

## Testing strategy (needs more adversarial testing than any prior sibling repo)

- **Concurrent `Record()` stress test** — the most important test in the
  repo: fire N concurrent `Record()` calls at the same chain, confirm every
  entry got a unique strictly-sequential `EntryID` and `Verify()` passes
  afterward. Directly validates the §4.2 serialization strategy actually
  works, not just that it compiles.
- **Deliberate tamper test** — write N entries, mutate one entry's payload
  directly in the underlying Postgres table (bypassing graudit's API), call
  `Verify()`, confirm `Valid: false` with the correct `BrokenAt` ID.
- **Hash determinism test** — two `AuditEvent`s with logically identical
  payloads but different map key insertion order must hash identically.
- **Genesis entry test** — `Verify()` on a chain of length 1.
- Race detector mandatory, same as every sibling repo.

## Dependencies

- `grevents` — real functional dependency: `Record()` publishes on success.
- `grlog` — optional structural `Logger`, same pattern as siblings.
- `grcache` — optional, read-path only (caching `Query()` results); never
  used to cache or skip a `Record()` write, since that would undermine
  durability. A v1.x nice-to-have, not required for correctness.
- PostgreSQL driver (GORM, matching grcache's Postgres backend, for
  ecosystem consistency).

## Sibling repo conventions to match once code exists

- Module path is `github.com/gourdian25/graudit`; subpackage-per-backend
  (`postgres/`, `memory/`) following grcache's reasoning, unlike grlog's/
  gourdiantoken's flat single-package layout.
- Source files use a `// File: <relative-path>` header maintained by the
  `bark` tool (`.bark.toml` already present in this repo).
- Expect a `Makefile` with targets equivalent to siblings' `test`, `race`
  (mandatory), `bench`, `lint`, `coverage-check`, `release VERSION=vX.Y.Z`.
- Sentinel errors: `errors.Is`-compatible, defined once in `errors.go`, no
  `IsX(err) bool` helpers.
- `Close()` idempotent via `sync.Once`, matching grlog/grcache/gourdiantoken.
- `docs.go` for package-level godoc only, no logic.
- Once implementation exists, treat `docs/plan/graudit-plan.md` as
  historical context (matching grcache's/grevents' own CLAUDE.md treatment
  of their plan docs) — the actual code and this file become authoritative.
