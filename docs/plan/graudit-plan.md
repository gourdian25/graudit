# graudit — Detailed Scope & Implementation Planning Document

**Repo path (to be created):** `~/Dev/gourdian25/graudit`
**Reference repos already in workspace:** `~/Dev/gourdian25/gourdiantoken`,
`~/Dev/gourdian25/grlog`, `~/Dev/gourdian25/grcache`,
`~/Dev/gourdian25/grevents`

---

## 0. Read this before anything else: assess whether to build this at all

> **Resolved 2026-07-09**: The build-vs-defer question below was answered
> during pre-implementation research: `gourdianerp` does not exist yet, and
> `grauth`/`grpolicy` are empty directories, exactly the "no real need yet"
> scenario this section warns about. Despite that, the user explicitly
> decided to build graudit now, and expanded its scope beyond what's
> described below: **three storage backends ship in v1** (`graudit/memory`,
> `graudit/postgres`, `graudit/mongo` — not just Postgres+memory), full
> release-tooling parity with sibling repos (goreleaser, golangci-lint,
> CHANGELOG, SECURITY.md), and >80% test coverage enforced per-package. This
> is a locked-in decision, not an open question — the rest of this document
> is retained as historical context for the reasoning that preceded it,
> matching how grcache's and grevents' own CLAUDE.md files treat their plan
> docs once real code exists. See `docs/architecture.md` (once written) and
> the implementation plan for the resolved design.

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
   signature and decide what topic name(s) graudit will publish.
   **Resolved**: real grevents examples (`role.assigned`, `order.placed`,
   `payment.failed`, `user.signup`) confirm the topic convention is
   **two segments**, `resource.pastTenseVerb` — graudit publishes
   `"audit.recorded"` (not the three-segment `"audit.entry.recorded"`
   originally sketched here).
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
- **Three backends ship in v1**: `graudit/memory` (test/dev only, never for
  anything you need to keep), `graudit/postgres`, and `graudit/mongo` (both
  production-eligible, durable) — following grcache's subpackage-per-backend
  layout, so a consumer using only one backend doesn't pull in the other's
  driver. (Originally scoped as Postgres-only + memory; expanded to include
  Mongo per explicit user decision — see §0.)
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

// GenesisPrevHash is the well-known PrevHash value for the first entry in a
// chain (64 zero characters, matching SHA-256's hex output length). Exported
// so every backend and Verify() agree on one value instead of each
// hardcoding its own — computed via strings.Repeat rather than a hand-typed
// literal, since a miscounted zero would be exactly the kind of subtle
// determinism bug this package exists to prevent.
var GenesisPrevHash = strings.Repeat("0", 64)

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

### 4.1a grevents injection (resolved)

`grevents.Bus` is **not** part of the `AuditLog` interface. It is a
construction-time dependency injected via each backend's `Config`/`Option`
(`EventBus grevents.Bus`, or `WithEventBus(bus)` for the memory backend),
exactly like the optional `Logger` — defaulting to `nil`, meaning no publish,
not an error. This is implemented once, in `events.go`:

```go
const TopicAuditRecorded = "audit.recorded"

func PublishRecorded(ctx context.Context, bus grevents.Bus, logger Logger, entry AuditEvent) {
	if bus == nil {
		return
	}
	if err := bus.Publish(ctx, grevents.Event{
		Topic:   TopicAuditRecorded,
		Payload: entry,
		Metadata: map[string]string{
			"actor_id": entry.ActorID, "entity_type": entry.EntityType, "entity_id": entry.EntityID,
		},
	}); err != nil {
		logger.Warnf("graudit: publish %s for entry %d failed: %v", TopicAuditRecorded, entry.ID, err)
	}
}
```

Every backend calls `PublishRecorded` once after its own durable write
commits. A publish failure is logged, never returned as a `Record()` error —
the durable write is the source of truth, grevents is a best-effort side
channel.

### 4.2 The hard problem: single-writer serialization

Because each entry's hash depends on the *immediately preceding* entry's
hash, `Record()` calls **cannot** be processed concurrently against the same
chain without a serialization point — two concurrent `Record()` calls
racing to compute "previous hash + my payload" against the same prior entry
would corrupt the chain (best case: one write fails a uniqueness constraint;
worst case, without a DB-level guard: two entries silently claim the same
chain position).

This is the single hardest and most safety-critical design problem in this
repo. **Resolved design, per backend:**

- **`graudit/postgres`**: `pg_advisory_xact_lock(chainLockKey)` (a single
  constant key — one global chain in v1, no per-tenant sub-chains) taken
  inside the same transaction that reads the current tail and inserts the
  new entry; released automatically on commit/rollback. `EntryID` is
  **explicitly assigned by application code inside the locked transaction,
  not a `SERIAL`/`BIGSERIAL` column** — a Postgres sequence advances even
  when its transaction rolls back, which would silently create a gap in
  `EntryID` with no corresponding entry, breaking the "strictly increasing,
  no gaps" contract and making a legitimate rollback-gap indistinguishable
  from tampering. Chosen over `SELECT ... FOR UPDATE` on the latest row
  because that requires a row to lock, which doesn't exist for the
  empty-chain (genesis) case without adding a separate sentinel row — the
  advisory lock handles genesis and non-genesis uniformly. Remains correct
  across multiple app replicas talking to the same Postgres instance.
- **`graudit/mongo`**: a singleton tail document (`{_id: "tail",
  lastEntryId, lastHash}`) updated inside a multi-document ACID transaction
  (`session.WithTransaction`) alongside the new entry's insert. **Requires
  the target deployment to be a replica set unconditionally** — there is no
  `useTransactions bool` escape hatch (unlike gourdiantoken's Mongo repo),
  because graudit's correctness, not just performance, depends on the
  transaction. `NewMongoAuditLog` fails fast at construction (a probe
  transaction) against a standalone instance rather than silently
  degrading to non-transactional writes that could corrupt the chain.
  `session.WithTransaction` already retries internally on
  `TransientTransactionError`/`UnknownTransactionCommitResult` — no
  additional manual retry loop is needed or should be added.
- **`graudit/memory`**: a single `sync.Mutex` guarding the in-memory tail
  state and entry slice together — sufficient because this backend is
  single-process, test/dev-only by definition.

### 4.3 Hash computation

```go
hash = SHA256(entryID || actorID || entityType || entityID || action ||
              canonicalJSON(payload) || timestamp || prevHash)
```

**Resolved canonical encoding**: round-trip `payload` through `json.Marshal`
→ `json.Decoder` with `UseNumber()` into `map[string]any`/`[]any`/scalars
(`UseNumber()` avoids the float64-reformatting-large-integers trap that
plain `json.Unmarshal` into `any` would introduce), then recursively
re-encode with object keys sorted, no whitespace, arrays keeping order, and
leaf strings encoded via `json.Marshal` to reuse Go's correct escaping — a
custom recursive encoder, not a third-party canonical-JSON library (none
exists elsewhere in the ecosystem, and the payload shape is fully within
graudit's control). `timestamp` is fixed to `time.RFC3339Nano` in UTC (not
default `Time.String()`, which is timezone/precision-dependent). This
determinism claim is covered by an explicit `hash_test.go` test: two
`AuditEvent`s with logically identical payloads but different map key
insertion order must hash identically.

### 4.4 Genesis entry

The very first entry in a chain has `PrevHash = graudit.GenesisPrevHash`
(64 zero characters, matching SHA-256's hex output length) — document this
explicitly so `Verify()` knows how to treat entry #1 as a special case
rather than looking for a non-existent "entry #0."

---

## 5. Architecture / folder structure

```
graudit/
├── audit.go              // AuditLog interface, AuditEvent, EntryID, QueryFilter, VerifyResult, GenesisPrevHash
├── hash.go                 // ComputeHash (exported for subpackage use) + canonical JSON encoding, isolated for independent testing
├── diff.go                   // snapshot diff engine used by RecordChange
├── errors.go
├── logger.go                    // optional structural Logger interface, NopLogger/OrNop
├── events.go                       // grevents integration: TopicAuditRecorded, PublishRecorded
├── docs.go                           // package godoc only
├── postgres/
│   └── postgres.go                     // pg_advisory_xact_lock serialization; explicit (non-serial) EntryID
├── memory/
│   └── memory.go                         // test/dev-only backend; sync.Mutex-serialized
├── mongo/
│   └── mongo.go                            // session.WithTransaction serialization; entries + chain_state collections; replica set required
├── example/
│   └── example.go                            // runnable demo against the memory backend
└── conformance/
    └── conformance.go                          // shared behavioral suite: hash-chain integrity, tamper detection, concurrent Record() ordering, Verify() correctness on a deliberately-corrupted chain, grevents publish/publish-failure
```

---

## 6. Testing strategy (this repo needs more adversarial testing than any prior repo)

Every test below is implemented **once**, in `conformance/conformance.go`,
and parameterized across all three backends via each backend's own
`TestConformance(t *testing.T)` — not hand-duplicated per backend.

- **Concurrent `Record()` stress test**: fire N concurrent `Record()` calls
  at the same chain and confirm afterward that (a) every entry got a unique,
  strictly sequential `EntryID`, and (b) `Verify()` over the whole chain
  passes. This is the most important test in the whole repo — it directly
  validates §4.2's serialization strategy actually works (mutex / advisory
  lock / Mongo transaction, depending on backend), not just that it
  compiles.
- **Deliberate tamper test**: write N entries, then directly mutate one
  entry's payload in the underlying storage (bypassing graudit's own API via
  a per-backend `WithTamperHook`: raw SQL for postgres, raw driver call for
  mongo, direct struct mutation for memory), then call `Verify()` and
  confirm it correctly reports `Valid: false` with the right `BrokenAt`
  entry ID.
- **Hash determinism test**: construct two `AuditEvent`s with logically
  identical payloads but different in-memory map key insertion order, and
  confirm they hash identically (validates the canonical-encoding claim in
  §4.3) — a pure unit test in root `hash_test.go`, plus re-asserted
  end-to-end through `Record` in conformance to catch a backend
  accidentally re-serializing the payload differently before hashing.
- **Genesis entry test**: confirm `Verify()` handles a chain of length 1
  correctly (no prior entry to compare against).
- **`grevents` publish test**: a stub `Bus` test double records `Publish`
  calls; confirm exactly one call per successful `Record`, with topic
  `graudit.TopicAuditRecorded` and the recorded `AuditEvent` as payload.
- **`grevents` publish-failure test**: a stub bus whose `Publish` always
  errors; confirm `Record` still succeeds and the entry is durably
  queryable — validates the ordering guarantee in §4.1a.
- **`RecordChange` diff test**: confirm the diff engine produces the
  expected `ChangeDiff` payload for a representative before/after pair.
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
- MongoDB driver (`go.mongodb.org/mongo-driver` **v1**, not the breaking
  `/v2` rewrite — matching grcache/mongo's own justification for staying on
  v1).

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

## 9. Evaluation questions for the agent (resolved — answers below, see the implementation plan for full detail)

1. **§0 assessment**: `gourdianerp` does not exist; `grauth`/`grpolicy` are
   empty. The honest answer was "no real need yet" — the user was informed
   of this and explicitly chose to build anyway with expanded scope. See §0.
2. **Serialization strategy**: resolved per-backend in §4.2 —
   `pg_advisory_xact_lock` + explicit `EntryID` (postgres),
   `session.WithTransaction` + mandatory replica set (mongo), `sync.Mutex`
   (memory). All three address multi-writer correctness at the layer
   appropriate to that backend (DB-level for postgres/mongo, since those are
   the multi-replica-safe backends; process-level for memory, since it's
   explicitly single-process by design).
3. **Canonical JSON**: resolved in §4.3 — `json.Decoder.UseNumber()` round
   trip + custom recursive sorted-key encoder, `time.RFC3339Nano` UTC
   timestamps, verified by an explicit `hash_test.go` determinism test.
4. **grevents topic**: resolved — `"audit.recorded"`, matching the confirmed
   real two-segment convention (§1, §4.1a).
5. **Verify() tamper detection**: resolved design uses two checks per entry
   (per-entry stored-hash recomputation + adjacent stored-`PrevHash`
   linkage) so `BrokenAt` correctly pinpoints the first broken link rather
   than cascading every subsequent entry as broken — see the implementation
   plan for the full reasoning. The conformance suite's
   `VerifyDetectsTamper` scenario is the test that proves this, run against
   all three backends.
6. **API consistency with siblings**: `Close()` stays on `AuditLog` (every
   sibling top-level interface includes it); sentinel errors follow the
   universal `errors.New`/no-`IsX()`-helper convention; `Logger` is the same
   structural optional interface as grcache's; `grevents.Bus` is injected
   via Config/Option, not the interface, keeping `AuditLog` itself free of a
   grevents import.