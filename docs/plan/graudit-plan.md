# graudit — Detailed Scope & Implementation Planning Document

**Repo path (to be created):** `~/Dev/gourdian25/graudit`
**Reference repos already in workspace:** `~/Dev/gourdian25/gourdiantoken`,
`~/Dev/gourdian25/grlog`, `~/Dev/gourdian25/grcache`,
`~/Dev/gourdian25/grevents`

---

## 0. Read this before anything else: assess whether to build this at all

Unlike grcache and grevents, graudit was flagged from the start as a strong
**"Defer" candidate**. Hash-chaining and tamper detection are genuinely hard
to get right — this is architecturally closer to a minimal blockchain data
structure than to grlog or grcache. Before writing a single line of code,
the agent must answer:

1. Does `gourdianerp` (check if it exists yet, and if so what it actually
   does) have any real feature today that requires a tamper-evident audit
   trail, as opposed to a normal `created_by`/`updated_at` column and a
   plain log line via grlog?
2. Is there an actual compliance requirement (a real client, a real
   regulatory obligation) driving this now, or is this aspirational
   "SOC2/HIPAA readiness" scoping for a system that doesn't have users yet?
3. Given grevents and grcache both took real, non-trivial implementation
   effort, is building a hash-chained ledger the best use of the next chunk
   of solo development time — or would that time be better spent on
   `grpolicy`/`grauth`, which are on the critical path to `gourdianerp`
   shipping anything at all?

**If the honest answer is "no real need yet,"** the agent should say so
plainly and recommend deferring, rather than producing an implementation
plan just because one was asked for. If the answer is "yes, there's a real
need," proceed with the rest of this document. Either way, report the
assessment before doing anything else.

---

## 1. Instructions for the IDE agent (if proceeding)

1. **Read `~/Dev/gourdian25/grevents` in full.** graudit will be the first
   repo in the ecosystem to have a genuine *functional* dependency on
   another gourdian repo (not just a borrowed pattern) — `Record()` should
   publish an event via grevents after successfully writing an audit entry,
   so other consumers (a future notification system, a dashboard) can react
   without graudit knowing they exist. Confirm the exact `Bus.Publish`
   signature and decide what topic name(s) graudit will publish
   (e.g. `"audit.entry.recorded"`).
2. **Read `~/Dev/gourdian25/grcache` in full**, focusing on:
   - Its multi-backend subpackage structure (`grcache/redis`,
     `grcache/postgres`, etc.) — graudit's own storage backend decision
     (see §3) should follow the same subpackaging reasoning: don't force
     every consumer to pull in a DB driver they're not using.
   - Its `Close()` idempotency (`sync.Once`), sentinel-error style, optional
     `Logger` structural interface, and `conformance/` shared test suite —
     all four conventions should carry over unchanged.
   - The recent audit history in grcache's CHANGELOG (the `Pipeline` vs
     `TxPipeline` finding) — graudit's own claims about "tamper-evident" and
     "immutable" need the exact same scrutiny applied to them once built:
     don't let the docs claim more than the code actually enforces.
3. **Read `~/Dev/gourdian25/grlog` and `~/Dev/gourdian25/gourdiantoken`** for
   sentinel error conventions and any existing background-sweep-goroutine
   pattern relevant to retention/archival of old audit entries.
4. **Only after these reads**, and only if §0's assessment concluded this
   should be built now, produce an implementation plan covering:
   - Which storage backend(s) graudit ships with in v1 (see §3 — this
     should be minimal, not a repeat of grcache's five-backend scope).
   - How the hash-chain's single-writer serialization requirement (see §4.2)
     will be implemented, since this is the single hardest and most
     safety-critical piece of this repo.
   - Where grevents is actually wired in, and what the graudit README should
     honestly claim about "immutable" and "tamper-evident" — backed by
     exactly what `Verify()` can and cannot detect.

---

## 2. Vision

graudit is an append-only, tamper-evident audit trail — it answers "what
changed, who did it, and can we prove the record hasn't been altered,"
which is a fundamentally different question from what grlog answers ("what
happened during this request"). It is not a logging library, and it is not
a compliance certification product — it's one component that could
contribute to SOC2/HIPAA/PCI readiness, not the whole solution.

---

## 3. Scope

### 3.1 In scope (v1)

- **Append-only entry storage with hash-chaining.** Each entry's stored hash
  is computed over (entry payload + previous entry's hash), so altering or
  deleting any historical entry breaks the chain from that point forward in
  a way `Verify()` can detect.
- **One durable backend for v1: PostgreSQL.** Not five backends like
  grcache — an audit trail's entire value proposition depends on durability
  and queryability by indexed fields (actor, entity, time range), which
  points at a relational store as the sensible default, not in-memory. An
  **in-memory backend also ships**, but explicitly for testing/dev use only
  (same framing as grcache's Postgres/Mongo backends being test/dev-labeled)
  — never recommended for anything you actually need to keep.
- **`Verify(from, to)`**: recomputes the hash chain across the given entry
  range and confirms every entry's stored hash matches what recomputing it
  from its payload + the previous entry's hash would produce. Returns
  `false` (not an error) if a mismatch is found, plus enough detail to
  identify which entry broke the chain.
- **Snapshot + diff engine**: given a caller-supplied "before" and "after"
  state for an entity (as JSON-serializable values), computes a field-level
  diff and stores that diff as the entry's payload, rather than requiring
  every caller to hand-roll their own diffing.
- **Basic querying**: by actor ID, by entity (type + ID), by time range,
  and combinations thereof — backed by ordinary indexed SQL columns, not a
  bespoke query language.
- **Publishes an event via grevents** after each successful `Record()` (see
  §1.1) — this is the *only* interaction with grevents; graudit never
  subscribes to events to auto-generate audit entries in v1 (see 3.2).

### 3.2 Explicitly out of scope (v1)

- **Full SOC2/HIPAA/PCI/ISO certification tooling.** graudit can be *a*
  component of achieving compliance; it is not a compliance program, a
  policy engine, or an auditor-facing reporting product. Resist scope
  creep in this direction.
- **Real-time alerting.** That's grevents' (and eventually a consumer's)
  job. graudit publishes an event after recording; it does not evaluate
  rules or notify anyone itself.
- **Auto-instrumentation.** graudit does not automatically intercept calls
  in `grauth` or elsewhere to generate audit entries by magic. Every audit
  entry is the result of an explicit `Record()` call by the caller. Building
  automatic interception (e.g. via middleware that wraps every grauth
  mutation) is a reasonable future idea but adds real complexity and
  coupling — deliberately deferred.
- **Multiple simultaneous storage backends beyond Postgres + in-memory.**
  No Mongo, no Redis-backed audit storage in v1 — an audit trail in a cache
  that can evict entries is a contradiction in terms.
- **Cryptographic non-repudiation (digital signatures per entry).** Hash-
  chaining proves internal consistency (nothing was altered/removed without
  detection) but does NOT prove *who* wrote a given entry beyond whatever
  `ActorID` the caller supplied, and does not protect against a
  privileged attacker with direct DB access rewriting the *entire* chain
  from scratch (a hash chain only detects partial tampering, not wholesale
  regeneration by someone who controls the storage). This distinction must
  be stated explicitly and prominently in the README — this is exactly the
  kind of claim that needs to be precise, not aspirational.

---

## 4. Public API

### 4.1 Core interface

```go
package graudit

import (
	"context"
	"time"
)

type AuditLog interface {
	// Record computes the entry's hash (payload + previous entry's hash),
	// appends it durably, and publishes an event via grevents on success.
	Record(ctx context.Context, event AuditEvent) (EntryID, error)

	// RecordChange is a convenience wrapper: computes a diff between before
	// and after, and calls Record with that diff as the payload.
	RecordChange(ctx context.Context, actorID, entityType, entityID string, before, after any) (EntryID, error)

	// Verify recomputes the hash chain across [from, to] and confirms
	// integrity. ok=false (with a non-nil detail, no error) means tampering
	// or corruption was detected; err is reserved for genuine operational
	// failures (e.g. can't reach the DB).
	Verify(ctx context.Context, from, to EntryID) (ok bool, detail VerifyResult, err error)

	// Query returns entries matching filter, ordered oldest-first unless
	// filter specifies otherwise.
	Query(ctx context.Context, filter QueryFilter) ([]AuditEvent, error)

	Close() error
}

type EntryID uint64 // strictly increasing, chain position — not a UUID

type AuditEvent struct {
	ID         EntryID // set by Record; zero-value on input
	ActorID    string
	EntityType string
	EntityID   string
	Action     string // e.g. "create", "update", "delete", freeform but recommend a small controlled vocabulary
	Payload    any    // diff or arbitrary JSON-serializable detail
	Timestamp  time.Time // set by Record if zero
	Hash       string // set by Record; hex-encoded
	PrevHash   string // set by Record; hex-encoded, "" for the genesis entry
}

type QueryFilter struct {
	ActorID    string
	EntityType string
	EntityID   string
	From, To   time.Time
	Limit      int
}

type VerifyResult struct {
	Valid       bool
	BrokenAt    EntryID // zero if Valid
	Expected    string  // expected hash at BrokenAt
	Actual      string  // actual stored hash at BrokenAt
}
```

### 4.2 The hard problem: single-writer serialization

Because each entry's hash depends on the *immediately preceding* entry's
hash, `Record()` calls **cannot** be processed concurrently against the same
chain without a serialization point — two concurrent `Record()` calls
racing to compute "previous hash + my payload" against the same prior entry
would corrupt the chain (best case: one write fails a uniqueness constraint;
worst case, without a DB-level guard: two entries silently claim the same
chain position).

This is the single hardest and most safety-critical design problem in this
repo. Candidate approaches for the agent to evaluate against the actual
Postgres backend:

- **Database-level serialization**: an `EntryID` column that's a Postgres
  `SERIAL`/`BIGSERIAL` combined with a `SELECT ... FOR UPDATE` on the
  "latest entry" row (or an advisory lock, `pg_advisory_xact_lock`) inside
  the same transaction that inserts the new entry — guarantees correctness
  at the DB layer regardless of how many app-process goroutines call
  `Record()` concurrently, and correctly serializes even across multiple
  app instances (unlike an in-process mutex, which only protects a single
  process).
- **Application-level single-writer goroutine**: all `Record()` calls
  funnel through one channel to a single dedicated writer goroutine. Simple
  and correct *within one process*, but does NOT protect against a second
  process (a second `gourdianerp` replica) writing to the same Postgres
  table concurrently — this only works if you can guarantee graudit is a
  process-wide singleton talking to a database no other process writes to
  directly, which is a fragile assumption to build a "tamper-evident" system
  on.

**Recommendation for the agent to validate:** the DB-level advisory-lock or
`SELECT FOR UPDATE` approach is very likely the right one for the Postgres
backend, specifically *because* it's the only approach that remains correct
if `gourdianerp` ever runs more than one replica. The in-memory backend
(test/dev only, single-process by definition) can safely use a plain
`sync.Mutex`.

### 4.3 Hash computation

```go
hash = SHA256(entryID || actorID || entityType || entityID || action ||
              canonicalJSON(payload) || timestamp || prevHash)
```

Use a canonical JSON encoding (sorted keys, no ambiguous whitespace — Go's
`encoding/json` on a `map[string]any` does not guarantee key order across
runs by default, so confirm the actual serialization approach produces a
stable byte sequence for the same logical payload every time, or hashing
will be non-deterministic and `Verify()` will falsely report tampering).
This determinism check is worth its own explicit test.

### 4.4 Genesis entry

The very first entry in a chain has `PrevHash = ""` (or a well-known
constant, e.g. 64 zero characters) — document this explicitly so `Verify()`
knows how to treat entry #1 as a special case rather than looking for a
non-existent "entry #0."

---

## 5. Architecture / folder structure

```
graudit/
├── audit.go              // AuditLog interface, AuditEvent, EntryID, QueryFilter, VerifyResult
├── hash.go                 // hash computation + canonical JSON encoding, isolated for independent testing
├── diff.go                   // snapshot diff engine used by RecordChange
├── errors.go
├── postgres/
│   └── postgres.go              // primary durable backend; owns the serialization strategy from §4.2
├── memory/
│   └── memory.go                  // test/dev-only backend; sync.Mutex-serialized
├── events.go                         // grevents integration: what gets published, topic naming
└── conformance/
    └── conformance.go                  // shared behavioral suite: hash-chain integrity, tamper detection, concurrent Record() ordering, Verify() correctness on a deliberately-corrupted chain
```

---

## 6. Testing strategy (this repo needs more adversarial testing than any prior repo)

- **Concurrent `Record()` stress test**: fire N concurrent `Record()` calls
  at the same chain and confirm afterward that (a) every entry got a unique,
  strictly sequential `EntryID`, and (b) `Verify()` over the whole chain
  passes. This is the most important test in the whole repo — it directly
  validates §4.2's serialization strategy actually works, not just that it
  compiles.
- **Deliberate tamper test**: write N entries, then directly mutate one
  entry's payload in the underlying Postgres table (bypassing graudit's own
  API, simulating an attacker or a bug elsewhere touching the table
  directly), then call `Verify()` and confirm it correctly reports
  `Valid: false` with the right `BrokenAt` entry ID.
- **Hash determinism test**: construct two `AuditEvent`s with logically
  identical payloads but different in-memory map key insertion order, and
  confirm they hash identically (validates the canonical-encoding claim in
  §4.3).
- **Genesis entry test**: confirm `Verify()` handles a chain of length 1
  correctly (no prior entry to compare against).
- Race detector mandatory, same as every prior repo.

---

## 7. Dependencies

- `grevents` — real functional dependency for the first time in the
  ecosystem: `Record()` publishes an event on success.
- `grlog` — optional structural `Logger`, same pattern as siblings.
- `grcache` — optional, for caching `Query()` results (read path only;
  never used to cache/skip a `Record()` write, since that would undermine
  the durability guarantee). Explicitly note this is a v1.x nice-to-have,
  not required for correctness.
- PostgreSQL driver (GORM, matching grcache's existing Postgres backend
  conventions, for ecosystem consistency).

---

## 8. Roadmap / explicitly deferred

- Auto-instrumentation / middleware-based automatic audit entry generation.
- Digital signatures per entry (true non-repudiation beyond hash-chain
  consistency).
- Additional storage backends beyond Postgres + in-memory, if a real need
  emerges.
- Archival/retention tooling (e.g. moving old verified segments to cold
  storage) — the background-cleanup-goroutine pattern from gourdiantoken is
  relevant here but is a v1.x concern, not v1.

---

## 9. Evaluation questions for the agent

1. **Restate the §0 assessment explicitly**: build now, or defer? Justify
   with whatever was actually found about `gourdianerp`'s current state.
2. If proceeding: which serialization strategy from §4.2 does the agent
   recommend, and why — specifically addressing the multi-replica
   correctness concern, not just single-process correctness?
3. Does the canonical JSON encoding approach in §4.3 actually produce
   deterministic output for logically-equivalent Go values (maps, nested
   structs) — what specific encoding function/library call was chosen, and
   how was determinism verified?
4. What exact grevents topic name(s) will `Record()` publish, and does this
   conflict with or duplicate anything already established as convention in
   `grevents`' own docs/examples?
5. Does `Verify()`'s claim of detecting tampering hold up against the
   deliberate-tamper test in §6 — show the actual test and its result before
   claiming this works.
6. Given everything read from grcache and grevents, is there anything in
   graudit's planned API (§4.1) that's inconsistent with sibling
   conventions (naming, error style, Close() pattern) that should be
   fixed before implementation starts?