# graudit: Multi-chain support + PostgresConfig.Pool injection

> Source of truth for this initiative, matching the sibling repos'
> `docs/plan/<topic>-plan.md` convention. **Pause after each stage for
> review before starting the next**, mirroring how gourdiantoken's own
> multi-tenant work was staged (that plan doc, `docs/plan/multi-tenant-
> support-plan.md`, was recovered from git history for this purpose — it
> was deleted after completion, following this ecosystem's convention of
> treating plan docs as point-in-time, not living documents, once code
> exists — see `graudit-plan.md`'s own treatment in this repo's CLAUDE.md).
> Completion notes are appended to each stage's section as work lands.

## Context

The user is building a multi-tenant SaaS ERP backend (schema-per-tenant
Postgres) that wants graudit for tamper-evident audit trails: one
independent hash chain per tenant, plus one separate chain for
platform-operator actions (tenant provisioning/suspension) that happen
outside any tenant's schema. Critically, they want **one** `graudit.AuditLog`
instance (one connection pool) serving every tenant — not one instance per
tenant, which doesn't scale to hundreds of tenants (hundreds of extra
Postgres connections, no lifecycle story).

graudit today has no concept of multiple independent chains — confirmed
directly in the code: `EntryID`'s doc comment describes one global,
strictly-increasing sequence per `AuditLog` instance; `postgres.go`'s
`chainLockKey` comment literally says *"one global chain in v1... see
docs/architecture.md for the extension note"* (the codebase already
anticipates this exact split); `internal/postgresdb/schema.sql`'s
`graudit_entries` table has no chain/tenant column at all; `PostgresConfig`
only accepts a `DSN` and always dials its own dedicated `pgxpool.Pool`, with
no way to reuse an externally-supplied one (unlike grnoti's Postgres stores).

**Confirmed facts governing scope:**
- Zero consumers of graudit exist anywhere in the gourdian25 workspace
  (grepped every sibling repo — grnoti, grcache, grpolicy, grevents,
  gourdiantoken, grlog — none import it). The only real consumer is the
  ERP project this plan is for.
- graudit is pre-1.0 (`v0.3.0`). Its own `CHANGELOG.md` `[0.3.0]` entry
  establishes "breaking changes allowed pre-1.0" as precedent — that
  release flattened three subpackages into one and replaced GORM with
  pgx/v5, shipping with **no migration tooling**.
- **Confirmed with the repo owner: greenfield.** No real graudit-backed
  Postgres/Mongo data exists anywhere that needs to be preserved across
  this change. This plan therefore ships the schema/index changes as a
  documented breaking change (manual one-time operator steps if ever
  needed), matching the v0.3.0 precedent exactly — no `ALTER TABLE`
  migration tooling is built.

This plan was produced by: direct full-file reading of every file this
change touches (`audit.go`, `hash.go`, `diff.go`, `errors.go`, `events.go`,
`memory.go`, `postgres.go`, `mongo.go`, both `internal/postgresdb` SQL and
generated files, `docs/architecture.md`, `docs.go`, `CHANGELOG.md`,
`version.go`, `example/example.go`, all five test files, `.golangci.yml`,
`sqlc.yaml`), cross-referencing grnoti's `PostgresConfig.Pool`/
`connectPostgres` pattern directly, recovering and reading gourdiantoken's
full historical multi-tenant plan doc from git history for staging-format
precedent, and a Plan-agent design pass that verified exact call-site
counts by grep (`RecordChange`: 8 sites; `.Verify(ctx`: 17; `ComputeHash(`:
7 in production code, 23 in `hash_test.go`), confirmed Postgres's
two-int32 `pg_advisory_xact_lock` overload exists, confirmed Mongo's
per-document transaction-conflict granularity gives free cross-chain
concurrency with no lock needed, and caught a real pre-existing test-infra
gotcha (see decision #11 below).

## Tracker

| Stage | Scope | Status |
|---|---|---|
| Stage 1 | Core multi-chain support (hash, interface, all 3 backends, schema/queries, test retrofit + isolation tests) | ✅ Done |
| Stage 2 | Postgres advisory-lock chain-scoping (performance only, no behavior change) | ✅ Done |
| Stage 3 | `PostgresConfig.Pool` injection (mirrors grnoti) | ✅ Done |
| Stage 4 | Docs / CHANGELOG / version bump / example.go | Not started |
| Stage 5 | Full validation pass | Not started |

## Design decisions locked in for the executor

1. **`ChainID` is mandatory everywhere, fail loud, no wildcard/query-all
   escape hatch in v1.** An audit-trail library silently letting an
   empty/omitted `ChainID` return every tenant's entries is a real
   cross-tenant data-leak risk — worse than the ergonomic cost of always
   specifying one. Matches `docs.go`'s own existing "Out of Scope (v1)"
   precedent of keeping the surface minimal; a caller needing a
   platform-admin cross-chain view can loop over known chain IDs.
2. **`AuditEvent` gains `ChainID string` as its first field** (before
   `ID`), checked first in `Validate()`'s switch — "scope first" ordering
   applied consistently everywhere chainID appears.
3. **`RecordChange`/`Verify` gain `chainID` as a *leading* parameter**
   (immediately after `ctx`), not trailing:
   `RecordChange(ctx, chainID, actorID, entityType, entityID string, before, after any)`,
   `Verify(ctx, chainID string, from, to EntryID)`. Reasoning: `Verify`'s
   shape is only unambiguous as leading — `from`/`to` are meaningless
   before you know which chain they're positions within, unlike
   gourdiantoken's `CreateAccessToken(..., sessionID, tenantID)` where a
   trailing reading was natural. Once `Verify` is leading, making
   `RecordChange` leading too gives one exceptionless rule: *"ChainID is
   always the parameter immediately after ctx."* graudit's blast radius
   (8 `RecordChange` sites, 17 `Verify` sites, confirmed by grep) is tiny
   next to gourdiantoken's 245 — the codemod-transposition-risk reasoning
   that justified gourdiantoken's *trailing* choice doesn't transfer here.
   **Executor note:** hand-edit each of the ~25 call sites individually
   (no scripted codemod — volume doesn't warrant one); use visually
   distinct fixture chain IDs in tests (e.g. `"test-chain-a"`, not a
   generic `"id2"`) so an accidental argument transposition is obvious in
   diff review, since `chainID`/`actorID`/`entityID` are adjacent
   same-type `string` params.
4. **`QueryFilter` gains `ChainID string`, now required** — a behavior
   change from today's "zero-value `QueryFilter` matches every entry."
   Both `Verify` and `Query` validate it in **all three backends**
   (neither validates `QueryFilter`/its own `chainID` param at all today —
   this is new validation, not a tightening of existing checks), checked
   immediately after the existing `closed` check, before any other logic —
   matching `Record`'s existing `closed → Validate()` ordering.
5. **New sentinel `ErrChainIDRequired`** (`errors.go`), not a reuse of
   `ErrInvalidEvent` (whose doc comment specifically scopes it to "an
   `AuditEvent` passed to `Record`") and no collision risk with the
   existing `ErrChainCorrupted` (different failure class — caller input
   vs. backend invariant violation). **Record's path dual-wraps both**
   sentinels via Go's multi-`%w` support:
   ```go
   case e.ChainID == "":
       return fmt.Errorf("%w: %w: ChainID is required", ErrInvalidEvent, ErrChainIDRequired)
   ```
   This keeps `ErrInvalidEvent`'s existing doc-comment claim true while
   making `errors.Is(err, ErrChainIDRequired)` true uniformly regardless of
   which method caught the empty `ChainID`. `Verify`/`Query`'s direct
   `chainID`/`filter.ChainID` checks wrap `ErrChainIDRequired` alone (no
   `ErrInvalidEvent` — correctly out of scope per its own doc comment).
6. **`ComputeHash` gains `chainID string` as a new leading parameter,
   included *first* in the hash preimage** — a correctness-critical fix,
   not cosmetic. Without it, an attacker with direct DB access could
   rewrite one entry's `chain_id` column (splicing it from tenant A's
   chain into tenant B's) without invalidating its `Hash`, since `EntryID`
   sequences independently restart at 1 per chain and two entries from
   different chains could otherwise share an identical `(EntryID, ActorID,
   EntityType, EntityID, Action, Payload, Timestamp, PrevHash)` tuple.
   `ComputeHash` is **exported** — this is a breaking signature change to
   public API, called out explicitly. `GenesisPrevHash` does **not** need
   to become chain-specific: since `chainID` is now in every entry's
   preimage including entry #1's, two chains' genesis entries sharing the
   same well-known `GenesisPrevHash` constant still hash differently.
   **This safety claim only holds if `Verify`'s own row-fetch is
   chain-scoped independently from `Query`'s** — `postgres.go`'s
   `ListEntriesInRange` (used by `Verify`, a separate code path from
   `Query`'s `QueryEntries`) and `mongo.go`'s `Verify`
   (`a.entries.Find(ctx, bson.M{"entryId": ...})`) must both get an
   explicit `chain_id`/`chainId` predicate — confirm both, not just
   `Query`'s filter, or `Verify` would silently walk across chain
   boundaries even after `Query` is correctly scoped.
7. **`BuildChangeEvent` (`diff.go`) is also exported and also breaking** —
   gains `chainID` as a new leading parameter (`RecordChange` delegates to
   it in all three backends). Called out together with `ComputeHash` as
   this plan's two exported-breaking-signature changes.
8. **Postgres locking — two stages, not one.** Stage 1 ships with the
   existing single global `chainLockKey` **unchanged** (still correct, not
   yet scale-optimal: over-serializing unrelated chains is a pure
   throughput cost once `GetLastEntry`/`InsertEntry`/`ListEntriesInRange`
   are chain-filtered, never a correctness gap). Stage 2 narrows it via
   Postgres's two-`int32` `pg_advisory_xact_lock(key1, key2)` overload:
   `key1` = the existing `chainLockKey` constant (already fits `int32`,
   `892374651 < 2^31-1`), `key2` = FNV-1a 32-bit hash of `chainID`
   (`hash/fnv`, deterministic across connections — **not** `hash/maphash`,
   which is deliberately randomly reseeded per-process and unsuitable
   here). A hash collision between two different `chainID`s only costs
   extra (harmless) serialization, never corruption, since the SQL
   `chain_id` filter stays authoritative regardless of lock granularity.
   Accepted as a documented low-severity trade-off (chainIDs are
   tenant-provisioning-assigned, not raw adversarial end-user input) — same
   "document, don't guard" convention this repo already uses for
   `probeDocID` and the schema-lock-key collision comments.
9. **Mongo needs no equivalent Stage-2 lock-scoping follow-up.** Keying
   each chain's chain-state singleton document's `_id` directly by the
   real `chainID` means concurrent `Record` calls on different chains
   touch disjoint documents and never trigger
   `session.WithTransaction`'s conflict-retry against each other — "no
   artificial serialization graudit's own design imposes," not a
   throughput guarantee beyond normal single-primary write ordering.
10. **No migration tooling for existing deployments** (confirmed
    greenfield). `CHANGELOG.md`'s breaking-change entry documents the
    manual one-time operator steps if ever needed later: Postgres has no
    `ALTER TABLE` path (`CREATE TABLE IF NOT EXISTS` no-ops against an
    existing differently-shaped table — a real deployment needs manual
    recreation); Mongo's legacy unique index `{entryId:1}` must be
    manually dropped (`db.graudit_entries.dropIndex("entryId_1")`) before
    upgrading an existing deployment, or it will incorrectly reject every
    second chain's `entryId=1`. `ensureAuditIndexes` must **not**
    auto-detect-and-drop this index — that would break fresh deployments
    where it doesn't exist yet.
11. **Test-infra fix bundled into Stage 1:** `postgres_test.go`'s
    `truncatePostgresTestDB` (currently `TRUNCATE TABLE`-only) becomes
    `DROP TABLE IF EXISTS graudit_entries`. Without this, the very first
    Stage-1 test run against an already-existing local `graudit_test`
    database silently keeps the old (pre-`chain_id`) schema — `CREATE
    TABLE IF NOT EXISTS` no-ops — and every test fails with a confusing
    "column does not exist" error instead of an obviously schema-related
    one. `mongo_test.go`'s `dropMongoTestDB` already does a full
    `Database.Drop()` and needs no equivalent change.
12. **Version bump: `v0.3.0` → `v0.4.0`** (pre-1.0 MINOR bump for a
    breaking change — exact precedent set by this repo's own `v0.2.0` →
    `v0.3.0` breaking release).
13. **`PostgresConfig.Pool` (Stage 3) scoped to Postgres only**, mirroring
    grnoti's exact DSN-xor-Pool + `ownsPool bool` pattern. grnoti's
    `SkipSchemaEnsure bool` is explicitly **not** added here (not
    requested — flagged only as an easy, unrequested follow-on). No
    equivalent Mongo `*mongo.Client` injection in this plan (not
    requested).

## New sentinel errors introduced

| Sentinel | Stage | Triggered by |
|---|---|---|
| `ErrChainIDRequired` | 1 | `Record` (via `Validate()`, dual-wrapped with `ErrInvalidEvent`) / `RecordChange` / `Verify` / `Query`, whenever `chainID`/`ChainID` is empty |

## Stage 1 — Core multi-chain support

The largest stage by necessity: `var _ AuditLog = (*postgresAuditLog)(nil)`
(and the mongo/memory equivalents) force every concrete backend to
implement the *same* interface the moment `RecordChange`/`Verify`'s
signatures change — there is no intermediate compiling state with only
some backends updated, so this cannot be split further without leaving the
package non-compiling at a stage boundary (confirmed; same reason
gourdiantoken's own Stage 1 was its largest).

**Files touched:**

- **`audit.go`** — `AuditEvent` gains `ChainID string` (first field, before
  `ID`); `Validate()`'s switch gains a `ChainID == ""` case first,
  dual-wrapping `ErrInvalidEvent`+`ErrChainIDRequired` (decision #5);
  `AuditLog.RecordChange`/`AuditLog.Verify` doc comments + signatures
  updated (decision #3); `QueryFilter` gains `ChainID string`, doc comment
  changed from "zero value matches every entry" to "ChainID is required."
- **`errors.go`** — add `ErrChainIDRequired = errors.New("graudit: ChainID is required")`.
- **`hash.go`** — `ComputeHash(chainID string, entryID EntryID, actorID, entityType, entityID, action string, payload any, timestamp time.Time, prevHash string) (string, error)`,
  `chainID` written into the preimage buffer first (before `entryID`). Doc
  comment gains the splice-attack rationale (decision #6), mirrored into
  `docs/architecture.md`.
- **`diff.go`** — `BuildChangeEvent(chainID, actorID, entityType, entityID string, before, after any) (AuditEvent, error)` (decision #7).
- **`events.go`** — `PublishRecorded`'s `Metadata` map gains
  `"chain_id": entry.ChainID` alongside the existing `actor_id`/
  `entity_type`/`entity_id` — purely additive, no signature change.
- **`memory.go`** — `Record`/`RecordChange`/`Verify`/`Query` updated for
  per-chain state: replace the single `lastHash`/`lastID` pair with a
  `map[string]*memoryChainTail` (or equivalent), still guarded by the same
  one `mu` — correctness over micro-optimization for the test/dev-only
  backend.
- **`postgres.go`** — `chainLockKey`'s doc comment updated to state the
  Stage-1 (unchanged, global) / Stage-2 (chain-scoped) split explicitly;
  `Record`/`Verify`/`Query`/`RecordChange` updated to pass/filter
  `chainID` at every query call (decision #6's `ListEntriesInRange`
  requirement applies here).
- **`internal/postgresdb/schema.sql`** — `chain_id TEXT NOT NULL` column
  added; primary key becomes `PRIMARY KEY (chain_id, entry_id)`;
  `idx_graudit_actor`/`idx_graudit_entity`/`idx_graudit_timestamp` become
  composite with a leading `chain_id`; `idx_graudit_hash` stays a plain
  unique index on `hash` alone (global uniqueness is even more meaningful
  now that `chain_id` is baked into the hash itself).
- **`internal/postgresdb/queries/audit.sql`** — `GetLastEntry`,
  `InsertEntry`, `ListEntriesInRange`, `QueryEntries` all gain a
  **mandatory** (not `sqlc.narg`-optional, unlike the existing
  actor/entity/timestamp filters) `chain_id = $N` predicate; regenerate
  `internal/postgresdb/{models,querier,audit.sql}.go` via `sqlc generate`
  (never hand-edit).
- **`mongo.go`** — `entryDocument` gains `ChainID string \`bson:"chainId"\``;
  `ensureAuditIndexes`'s index list replaced: unique index becomes
  compound `{chainId:1, entryId:1}`, the other three (`actorId`,
  `entityType+entityId`, `timestamp`) gain a leading `chainId` component;
  chain-state singleton's `_id` keyed directly by the real `chainID`
  string instead of the fixed `chainStateID = "tail"` constant, which is
  removed as dead code; `Record`/`Verify`/`Query`/`RecordChange` updated
  to filter/pass `chainID` (decision #6's Mongo `Verify` `Find()`
  requirement applies here). `probeDocID` unchanged (accepted negligible
  collision risk, same category as existing advisory-lock-key comments).
- **`docs/architecture.md`** — inline updates: the `chainLockKey` section
  (state the Stage-1/Stage-2 split — this finishes a comment the file
  already anticipates, doesn't add a new caveat), the mongo chain-state
  section, plus a new section explaining why `chainID` must be in the hash
  preimage (decision #6's splice-attack rationale).
- **Test retrofit:**
  - `contract_audit_test.go` — add `const testChainID = "test-chain-a"` and
    a second constant for the isolation scenarios' second chain (e.g.
    `testChainID2 = "test-chain-b"`); thread through all 11 existing
    scenarios; `tamperHookFunc` signature gains `chainID string`
    (`func(t *testing.T, log AuditLog, chainID string, entryID EntryID)`);
    add three new scenarios: **`ChainIsolation`** (record into two chains
    on the same instance, confirm each chain's `EntryID` sequence
    independently starts at 1, confirm `Verify`/`Query` scoped to one
    chain never see the other's entries despite colliding `EntryID`
    values), **`ChainIsolationTamperContainment`** (tamper an entry in
    chain A via the tamper hook, confirm `Verify(chainB, ...)` still
    reports `ok=true` — tampering in one tenant's chain must never cascade
    into another's `Verify` result), **`ConcurrentRecordStressMultiChain`**
    (concurrent `Record` calls interleaved across 2+ chains, asserting
    each chain's own `EntryID` sequence is gap-free/duplicate-free and
    `Verify` passes per chain — correctness only, no timing assertions, to
    avoid CI flakiness).
  - `memory_test.go`/`postgres_test.go`/`mongo_test.go` — `tamper<Backend>Entry`
    signatures updated; `postgres_test.go`'s `truncatePostgresTestDB`
    changes `TRUNCATE TABLE` → `DROP TABLE IF EXISTS graudit_entries`
    (decision #11).
  - `internal_coverage_test.go` — all direct `Record`/`RecordChange`/
    `Verify` call sites (~15, confirmed by grep) updated with a chainID
    argument; new coverage cases for the `ErrChainIDRequired` branch on
    each backend's `Verify`/`Query`.
  - `audit_test.go` — new `Validate()` cases for empty `ChainID`
    (including the dual-wrap `errors.Is` assertions for both
    `ErrInvalidEvent` and `ErrChainIDRequired`).
  - `hash_test.go` — all `ComputeHash` call sites (23, confirmed by grep)
    updated with the new leading `chainID` argument.
  - `events_test.go` — assert `Metadata["chain_id"]` is present and
    correct.

**New/changed public API:**
```go
type AuditEvent struct { ChainID string; ID EntryID; /* ... unchanged ... */ }
type QueryFilter struct { ChainID string; /* ... unchanged ... */ } // now required
var ErrChainIDRequired = errors.New("graudit: ChainID is required")
RecordChange(ctx context.Context, chainID, actorID, entityType, entityID string, before, after any) (EntryID, error)
Verify(ctx context.Context, chainID string, from, to EntryID) (bool, VerifyResult, error)
func ComputeHash(chainID string, entryID EntryID, actorID, entityType, entityID, action string, payload any, timestamp time.Time, prevHash string) (string, error)
func BuildChangeEvent(chainID, actorID, entityType, entityID string, before, after any) (AuditEvent, error)
```

**Dependencies:** none — foundation stage; everything else depends on it.

**Verification:** `go build ./...`, `go vet ./...`, `bark check`,
`gofmt -l .`, `make race`, `make coverage-check` (95% gate); then
`make docker-up`, manually drop/recreate the local `graudit_test` Postgres
DB and Mongo DB once (decision #11's fix makes subsequent runs
self-healing), and run the full contract suite live against both networked
backends — specifically confirm `ChainIsolation`,
`ChainIsolationTamperContainment`, and `ConcurrentRecordStressMultiChain`
pass on all three backends.

### Stage 1 completion notes

Implemented exactly as scoped, plus three real bugs caught and fixed along
the way that the plan's mechanical retrofit description didn't anticipate:

- **`hash/fnv`'s package name collides with this repo's own `hash.go`
  filename** — not an issue (Go imports by package identifier, not
  filename), noting only because it's worth remembering for Stage 2, which
  will actually import `hash/fnv`.
- **`sqlc generate` strips the `bark`-maintained `// File: ...` header** on
  every regenerated file (confirmed: this already happened before this
  stage too, just not previously visible in a diff) — `bark tag` restores
  it; this is now a required step after any future `sqlc generate` run,
  not just this one.
- **Three genuine test bugs found via `go vet`/local reasoning, not just
  mechanical signature fixes**:
  1. `audit_test.go`'s `TestAuditEvent_Validate_OK`/`_NilPayloadOK`/
     `_NonSerializablePayload` would have started failing at runtime (not
     just missed their intended assertion) — none of their `AuditEvent{}`
     literals had set `ChainID`, and `Validate()`'s new `ChainID` check now
     fires before anything else in the switch. Fixed by adding
     `ChainID: "c1"` to each. `TestAuditEvent_Validate_MissingFields`'s
     existing per-field cases had the same problem in a quieter form: they
     still passed (the dual-wrapped error still satisfies
     `errors.Is(err, ErrInvalidEvent)`), but were silently testing "missing
     ChainID" instead of their named field. Fixed by giving every case a
     `ChainID` except the new dedicated "missing ChainID" one.
  2. `internal_coverage_test.go`'s `TestPostgresAuditLog_OperationsAfterPoolClosed`/
     `TestMongoAuditLog_OperationsAfterClientDisconnected` both call
     `log.Query(ctx, QueryFilter{})` specifically to prove the closed
     pool/disconnected client produces `ErrBackendUnavailable` — but an
     empty `QueryFilter{}` now hits the new `ErrChainIDRequired` check
     before the method ever touches the pool/client, which would have
     made both tests assert the wrong sentinel silently passing for the
     wrong reason. Fixed by adding `ChainID: testChainID` to both.
  3. Postgres's/Mongo's `VerifyDetectsChainLinkageBreak` tests' raw
     tamper-injection SQL/driver calls (`UPDATE ... WHERE entry_id = 2` /
     `bson.M{"entryId": uint64(2)}`) needed a `chain_id`/`chainId`
     predicate added — with `entry_id`/`entryId` no longer globally unique
     across chains, an untargeted tamper could in principle hit the wrong
     chain's entry once more than one chain's data coexists in the test
     database (harmless today since these tests only ever seed one chain,
     but was a latent correctness gap in the tamper injection itself,
     fixed proactively).
- **`postgres_test.go`'s `truncatePostgresTestDB` changed from `TRUNCATE
  TABLE` to `DROP TABLE IF EXISTS`** exactly as planned (decision #11) —
  confirmed necessary by testing against this session's own already-
  existing local `graudit_test` database, which was on the pre-`chain_id`
  schema; `TRUNCATE`-only would have left it there silently.
- All three new contract scenarios (`ChainIsolation`,
  `ChainIsolationTamperContainment`, `ConcurrentRecordStressMultiChain`)
  pass on all three backends. Full verification green: `go build ./...`,
  `go vet ./...`, `gofmt -l .` (clean), `bark check` (clean, after `bark
  tag` restored the sqlc-regenerated files' headers), `golangci-lint run
  ./...` (0 issues), `go test -race -timeout 5m .` (full suite, all 3
  backends via `make docker-up`), `make coverage-check` (95.4%, meets the
  95% gate), `go run ./example` (updated with a minimal single-chain
  `ChainID` fix to keep `go build ./...` compiling — the full two-chain
  demo is Stage 4's job per the plan, not redone here).
- `docs/architecture.md` gained two new sections (multi-chain support /
  hash-preimage rationale; Postgres advisory-lock chain scoping, describing
  Stage 2 ahead of time) plus small in-place fixes to the pre-existing
  "No SERIAL/BIGSERIAL" and "mongo backend requires a replica set"
  sections, which referenced the old single global chain / fixed `"tail"`
  chain-state document and were now stale.

## Stage 2 — Postgres advisory-lock chain-scoping

Performance-only refinement, no behavior change (decision #8).

**Files touched:**
- **`postgres.go`** — `Record`'s `pg_advisory_xact_lock` call switches from
  the single-`bigint` form to the two-`int32` form:
  `SELECT pg_advisory_xact_lock($1, $2)` with `$1 = int32(chainLockKey)`
  (unchanged constant value, already fits `int32`) and `$2` = FNV-1a
  32-bit hash of `chainID` (`hash/fnv`'s `fnv.New32a()`).
- **`docs/architecture.md`** — document the FNV-1a rationale, explicitly
  note `hash/maphash` was considered and rejected (randomly reseeded
  per-process, unsuitable for a key needing cross-connection determinism),
  and the accepted adversarial-chainID trade-off (decision #8).

**New/changed public API:** none — purely internal.

**Test-file impact:** no new hard-asserting unit test (wall-clock timing
assertions are a flakiness risk this repo doesn't take elsewhere). Add
this repo's **first-ever `Benchmark*` function** (`make bench` currently
builds/passes but exercises nothing) comparing concurrent same-chain vs.
cross-chain `Record` throughput — a manually-run verification step, not a
CI gate.

**Dependencies:** Stage 1 (needs chain-filtered queries already in place;
without them this change would be strictly cosmetic).

**Verification:** `make race`; run the new benchmark before/after to
confirm cross-chain writes no longer serialize against each other while
same-chain writes still do; re-run `ConcurrentRecordStressMultiChain` from
Stage 1 to confirm correctness is unchanged.

### Stage 2 completion notes

Implemented exactly as scoped, with one small deviation from the plan's
literal wording worth recording:

- **`chainLockKey`'s declared type changed from `int64` to `int32`
  directly**, rather than casting `int32(chainLockKey)` inline at the one
  call site as the plan's decision #8 literally described. Since the
  constant's only use is now as the first argument to the two-`int32`
  `pg_advisory_xact_lock` overload, declaring it as `int32` from the start
  is simpler and makes an accidental future reintroduction of the
  single-`bigint` call form (which would silently need a different-typed
  key) a compile error instead of a silent implicit conversion. The
  constant's *value* (`892374651`) is unchanged.
- New `chainLockSubKey(chainID string) int32` (`postgres.go`) computes the
  FNV-1a hash via `hash/fnv`'s `fnv.New32a()`, exactly as planned; the
  `gosec` G115 conversion warning on `int32(h.Sum32())` is suppressed with
  a `//nolint:gosec` comment matching the existing `pgEntryID`/
  `toPgEntryID` precedent in the same file.
- `Record`'s advisory-lock call changed from
  `SELECT pg_advisory_xact_lock($1)` to
  `SELECT pg_advisory_xact_lock($1, $2)` with `(chainLockKey,
  chainLockSubKey(event.ChainID))` — the only functional change in this
  stage. The stale inline comment above the call ("A single constant key:
  one global chain in v1, no per-tenant sub-chains") was also updated;
  `chainLockKey`'s own doc comment was largely already-Stage-2-aware from
  Stage 1's writing (it explicitly named this exact refinement as
  upcoming), so only needed finishing, not rewriting.
- `docs/architecture.md`'s "Postgres advisory-lock chain scoping" section
  (written in Stage 1, ahead of this stage's own implementation) updated
  from future/planned tense to describe the landed implementation,
  including the `chainLockKey` type-change detail above.
- **New benchmark** (`postgres_test.go`,
  `BenchmarkPostgresAuditLog_Record_ChainConcurrency`, this repo's
  first-ever `Benchmark*` function) with `SameChain`/`CrossChain`
  subbenchmarks using `b.RunParallel`. Measured
  (`-benchtime=2s`, Apple M4, local `make docker-up` Postgres):
  **SameChain 381,373 ns/op vs. CrossChain 174,909 ns/op — cross-chain
  writes are ~2.2x faster**, empirically confirming the lock no longer
  serializes unrelated chains against each other while same-chain writes
  remain fully serialized (`ConcurrentRecordStressMultiChain` from Stage 1
  re-run and still green, confirming correctness is unchanged).
- Full verification green: `go build ./...`, `go vet ./...`, `gofmt -l .`
  (clean), `golangci-lint run ./...` (0 issues), `bark check` (clean, no
  new files), `go test -race -timeout 5m .` (full suite), the full
  `TestAuditLog_Contract/Postgres` subtree re-run individually and green,
  `make coverage-check` (95.4%, unchanged from Stage 1 — this stage added
  one new small function fully exercised by the existing contract suite
  plus the benchmark itself, which coverage tooling doesn't count as a
  test).

## Stage 3 — `PostgresConfig.Pool` injection

Independent of chain-scoping; mirrors grnoti's `postgres.go` pattern
exactly (`PostgresConfig.Pool`, `connectPostgres`, `ownsPool bool`).

**Files touched:**
- **`postgres.go`** — `PostgresConfig` gains `Pool *pgxpool.Pool` (doc
  comment: "Exactly one of DSN or Pool must be set," mirroring grnoti's
  wording); `postgresAuditLog` gains `ownsPool bool`; `NewPostgresAuditLog`
  refactored around a new `connectPostgres`-style helper mirroring
  grnoti's shape (`(ctx, cfg, component string) (*pgxpool.Pool,
  *postgresdb.Queries, bool, error)`, validating DSN-xor-Pool via
  `(cfg.DSN == "") == (cfg.Pool == nil)`); `Close()` only calls
  `a.pool.Close()` when `a.ownsPool`.

**New/changed public API:**
```go
type PostgresConfig struct { DSN string; Pool *pgxpool.Pool; /* ... unchanged ... */ }
```

**Test-file impact:** `postgres_test.go` gains
`TestNewPostgresAuditLog_WithExternalPool` (construct via `cfg.Pool`,
confirm it works), `TestNewPostgresAuditLog_DSNXorPoolRequired` (both set
→ error, alongside the existing neither-set case), and — mirroring
grnoti's own test naming — `TestPostgresAuditLog_SharedPool_CloseDoesNotClosePool`
(assert `pool.Ping` still succeeds after `log.Close()` when the pool was
externally supplied).

**Dependencies:** ordered after Stages 1–2 (lower-friction to build on the
already-settled Stage-1 shape of `postgres.go` than the reverse), but
structurally independent — no chainID interaction.

**Note on scope:** grnoti's `PostgresConfig` also has a
`SkipSchemaEnsure bool` escape hatch — not requested here, not added
(decision #13).

**Verification:** `go vet ./...`, `make race`, `make coverage-check`.

### Stage 3 completion notes

Implemented as planned, with the `connectPostgres` helper taking the exact
grnoti-mirrored shape (`(ctx, cfg, component string) (*pgxpool.Pool,
*postgresdb.Queries, bool, error)`) despite graudit having only one
Postgres-backed caller (`NewPostgresAuditLog`, passing `"AuditLog"` as
`component`) — kept for ecosystem-convention consistency per the plan's
own wording ("mirrors grnoti's `postgres.go` pattern exactly"), not because
graudit needs the parameter today.

One deliberate simplification versus grnoti's own `connectPostgres`: no
per-branch `appLogger.Error(...)` calls inside the helper (grnoti's
doesn't have these either — its callers don't log connect failures at
all). graudit's `NewPostgresAuditLog` now logs once, generically, at the
call site (`appLogger.Error("graudit: connect failed", "error", err)`)
instead of the pre-Stage-3 code's three separately-worded log lines
("open failed" / "ping failed" / — schema errors were never logged
directly either way). No test asserted the old per-branch log wording, so
this was a safe consolidation, not a behavior change contract-wise.

`Close()` now guards `a.pool.Close()` behind `a.ownsPool`, matching
grnoti's `tokenstore.postgres.go` etc. exactly.

**Test-file impact vs. plan:** all three planned tests added
(`TestNewPostgresAuditLog_WithExternalPool`,
`TestNewPostgresAuditLog_DSNXorPoolRequired`,
`TestPostgresAuditLog_SharedPool_CloseDoesNotClosePool` — the last using a
second `postgresAuditLog` instance as the "sibling" sharing the pool,
since graudit, unlike grnoti, has only one Postgres-backed type to play
that role). One test added beyond the plan:
`TestNewPostgresAuditLog_ExternalPoolPingFails` (an already-closed pool
passed via `cfg.Pool`, failing `Ping` deterministically) — added to cover
`connectPostgres`'s Pool-branch Ping-failure line, the Pool-supplied
analogue of the pre-existing `TestNewPostgresAuditLog_BadDSN` covering the
same failure on the DSN-dialing path. The Pool-branch's
`applyPostgresSchema` failure line remains untested (no deterministic way
to fail schema application against a live, already-Pinged pool without
fault injection) — accepted per this repo's existing convention of a
small number of permanently-untested branches, same category as
`applyPostgresSchema`'s own pre-existing 81.8%-covered branches.

**Verification results:** `go build ./...`, `go vet ./...`, `gofmt -l .`
all clean. `golangci-lint run ./...`: 0 issues. `go test -race -timeout
5m .`: full suite green (8.1s). New/changed Stage 3 tests individually
re-run with `-race -v`: all pass. `make coverage-check`: **95.5%**
(up from 95.4% pre-Stage-3 — the new tests exercise more of
`connectPostgres` than the old inline code had coverage for). `bark
check`: clean.

## Stage 4 — Docs, CHANGELOG, version bump, example

Last, once the API surface from Stages 1–3 is final.

**Files touched:**
- **`example/example.go`** — demo two chains on one `AuditLog` instance
  (e.g. a tenant chain + a platform chain); demo the `Pool`-injection
  pattern from Stage 3.
- **`docs.go`** — update "Key Features"/"Getting Started" code snippets for
  the new `ChainID` field and signatures; new short section on multi-chain
  support.
- **`README.md`** — same updates as `docs.go`, plus the backends table and
  any Quickstart/Backends code blocks.
- **`CHANGELOG.md`** — new `## [0.4.0]` entry (decision #12) documenting:
  the `ChainID` additions (breaking), `ComputeHash`/`BuildChangeEvent`
  signature changes (breaking, exported), `QueryFilter.ChainID` becoming
  required (breaking behavior change), the new `ErrChainIDRequired`
  sentinel, the Postgres lock-scoping refinement, `PostgresConfig.Pool`
  (additive), and — explicitly, per decision #10 — the two manual
  operator migration steps for anyone with an existing pre-`chain_id`
  deployment (Postgres: no `ALTER TABLE` path, manual recreation only;
  Mongo: `db.graudit_entries.dropIndex("entryId_1")` required before
  upgrading).
- **`version.go`** — `Version` `"v0.3.0"` → `"v0.4.0"`.
- **`CLAUDE.md`** — update the Architecture section's per-file bullets
  (`audit.go`, `hash.go`, `postgres.go`, `mongo.go`) for the new chain
  concept; update the Commands section if Stage 2 adds a benchmark worth
  naming.

**Test-file impact:** none expected — documentation/metadata-only stage.

**Dependencies:** Stages 1–3 fully landed.

**Verification:** `go run ./example` succeeds end-to-end against the
memory backend; `bark check` reports no header debris on touched files.

## Stage 5 — Full validation pass

`make fmt` → `make vet` → `make lint` → `make race` → `make coverage-check`
(must stay ≥95%) → `make bench` (now has a real benchmark from Stage 2) →
`make docker-up` against a **freshly recreated** `graudit_test` Postgres DB
and Mongo DB (not just an already-iterated-on local instance, to catch any
residual `CREATE TABLE IF NOT EXISTS` staleness) → full live-backend
contract run → `go run ./example` → `bark check`.

## Critical files

- `audit.go` — `AuditLog` interface, `AuditEvent`, `QueryFilter`, `Validate()`
- `hash.go` — `ComputeHash` (chain-splice-prevention fix, decision #6)
- `postgres.go` — locking (`chainLockKey`), `Record`/`Verify`/`Query`,
  `PostgresConfig` (Stage 3)
- `mongo.go` — chain-state singleton keying, index definitions
- `internal/postgresdb/schema.sql` + `queries/audit.sql` — schema/query
  source of truth for the Postgres backend changes (regenerate via
  `sqlc generate`, never hand-edit the generated output)
- `contract_audit_test.go` — the shared behavioral suite every backend
  runs through; the three new isolation/stress scenarios are the plan's
  primary correctness evidence
