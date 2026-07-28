# Architecture

This document records graudit's deliberate divergences from sibling repo
conventions and other decisions worth calling out explicitly — check here
before "fixing" something that looks inconsistent with `grcache` or
`gourdiantoken`.

## Package flattened into one, GORM replaced with pgx/v5 + sqlc

graudit originally split each backend into its own importable subpackage
(`graudit/memory`, `graudit/postgres`, `graudit/mongostore`), the same
`grcache` originally did, to keep unused client libraries out of a
consumer's build. That layout was reversed for consistency with the rest
of the gourdian ecosystem's flat-package convention — every backend's
constructor, Config/Option type, and concrete (unexported) implementation
now live directly in `package graudit` (`memory.go`, `postgres.go`,
`mongo.go`). Flattening incidentally resolved the `mongo` → `mongostore`
rename-for-collision issue from `v0.2.0`: as a file within one flat
package rather than a separate importable package, `mongo.go` no longer
collides with `go.mongodb.org/mongo-driver/mongo`'s own package name, so
the shorter, clearer name is safe again.

Each backend's own concrete `AuditLog` implementation is unexported and
backend-prefixed (`memoryAuditLog`, `postgresAuditLog`, `mongoAuditLog`) to
avoid colliding with the shared, exported `AuditLog` interface in
`audit.go` — before flattening, each subpackage could name its own struct
plain `AuditLog` since it lived in a different Go package.

The postgres backend's GORM implementation was replaced with `pgx/v5` and
sqlc-generated queries (see `internal/postgresdb`), matching
gourdiantoken's and grnoti's own Postgres backend pattern. This introduced
a *second* Postgres advisory lock, `grauditSchemaLockKey`
(`5_198_204_733`), used only to serialize schema application (`CREATE
TABLE/INDEX IF NOT EXISTS`) across concurrent callers at connect time —
deliberately distinct from `chainLockKey` (`892374651`), which continues
to serialize the actual chain-append transaction as it always has. The two
locks protect different things at different times and are never held
simultaneously, so there's no reason to unify them into one key.

## Multi-chain support: `ChainID` must be part of the hash preimage

graudit originally supported exactly one global chain per `AuditLog`
instance — `EntryID` was one strictly-increasing sequence with no
partitioning. This was extended so one `AuditLog` instance (one connection
pool) can serve any number of independent chains simultaneously (e.g. one
per tenant in a multi-tenant deployment, plus a separate chain for
platform-operator actions) — every `Record`/`RecordChange`/`Verify`/`Query`
call now takes a `ChainID`, and `EntryID` sequences/`PrevHash` linkage are
scoped per `ChainID` rather than globally. `ChainID` is mandatory and fail-
loud everywhere (`ErrChainIDRequired`) — an audit trail silently treating
an empty `ChainID` as an implicit default chain would risk one tenant's
entries landing in, or a query silently returning, another tenant's data.

The one design point that isn't optional: `ComputeHash` includes `chainID`
as the first field in its hash preimage, not just a storage/filter column.
Without it, an attacker with direct database access could rewrite one
entry's `chain_id` column — splicing it from one chain into another —
without invalidating its stored `Hash`, since `EntryID` sequences
independently restart at 1 in every chain and two entries from different
chains could otherwise share an identical `(EntryID, ActorID, EntityType,
EntityID, Action, Payload, Timestamp, PrevHash)` tuple. Baking `chainID`
into the preimage means reproducing an entry's `Hash` under a different
`chainID` requires breaking SHA-256, not editing a column.
`GenesisPrevHash` does not need to become chain-specific for the same
reason: since `chainID` is in every entry's preimage including entry #1's,
two chains' genesis entries sharing that one well-known `PrevHash` constant
still hash differently. This guarantee only holds if every backend's
`Verify` — not just `Query` — scopes its own row-fetch to one `chainID`
independently (they are separate code paths in every backend): postgres's
`ListEntriesInRange` and mongo's `Verify`-side `Find` both filter on
`chain_id`/`chainId`, matching `Query`'s own filtering.

No migration tooling exists for upgrading a pre-multi-chain deployment
(confirmed no such deployment needed to be preserved when this was added),
matching this repo's existing precedent of shipping schema-affecting
breaking changes pre-1.0
without migration tooling (the `v0.3.0` package-flatten/GORM-removal
release did the same). Postgres has no `ALTER TABLE` path — `CREATE TABLE
IF NOT EXISTS` no-ops against an existing, differently-shaped table, so an
existing deployment needs manual recreation. Mongo's legacy unique index
`{entryId:1}` must be manually dropped
(`db.graudit_entries.dropIndex("entryId_1")`) before upgrading an existing
deployment — left in place alongside the new compound `{chainId:1,
entryId:1}` index, it would incorrectly reject every second chain's
`entryId=1`. `ensureAuditIndexes` deliberately does not auto-detect and
drop this index itself: an unconditional drop would error against a fresh
deployment where the index never existed.

## Postgres advisory-lock chain scoping

`chainLockKey` originally serialized `Record` across the entire database
unconditionally — correct for a single global chain, but unnecessarily
serializes unrelated chains' writes against each other once multiple
chains share one database. Since `GetLastEntry`/`InsertEntry`/
`ListEntriesInRange` are all `chain_id`-filtered (see above), a single
global lock is strictly stronger than required — over-serialization is a
throughput cost, never a correctness gap — so this was addressed in two
steps rather than one: multi-chain support itself shipped first with the
lock left global (still correct, not yet scale-optimal), then narrowed in
a separate follow-up via Postgres's two-`int32`-argument
`pg_advisory_xact_lock(key1, key2)` overload: `key1` is the `chainLockKey`
constant (declared as `int32` directly, since that's its only use now —
the value itself, `892374651`, is unchanged and always fit in an `int32`),
`key2` is `chainLockSubKey(chainID)`, an FNV-1a 32-bit hash (`hash/fnv` —
deliberately not `hash/maphash`, which is randomly reseeded per process and
would hash the same `chainID` differently across connections, breaking the
lock's whole purpose: two writers on the same chain must agree on which key
they're contending for). A hash collision between two different `chainID`s
only costs extra (harmless) serialization, never corruption, since the SQL
`chain_id` filter remains authoritative regardless of lock granularity — an
accepted, low-severity trade-off given `chainID` values are
tenant-provisioning-assigned, not raw adversarial end-user input, the same
"document, don't guard" convention already used for `probeDocID` and the
schema-lock-key collision comments elsewhere in this file. `chainLockKey`
itself stays a separate constant rather than folding into the hash, purely
so this library's locks remain trivially distinguishable (by their fixed
`key1`) from an unrelated advisory lock another tool might take against the
same database.

The mongo backend needed no equivalent follow-up: keying each chain's
chain-state singleton document's `_id` directly by the real `chainID`
string means concurrent `Record` calls on different chains touch disjoint
documents and never trigger `session.WithTransaction`'s conflict-retry
against each other — "no artificial serialization graudit's own design
imposes," not a throughput guarantee beyond normal single-primary write
ordering, but requiring no lock-scoping work analogous to postgres's.

## No `SERIAL`/`BIGSERIAL` for `EntryID` in the postgres backend

`EntryID` is explicitly assigned by application code inside the same
`pg_advisory_xact_lock`-held transaction that inserts the row, never a
Postgres `SERIAL`/`BIGSERIAL` column. A sequence advances even when its
transaction rolls back, which would silently create a gap in `EntryID` with
no corresponding entry — breaking the "strictly increasing, no gaps"
contract `EntryID`'s own doc comment states, and making a legitimate
rollback-gap indistinguishable from tampering. That ambiguity is exactly
what a hash chain exists to eliminate, so it can't be tolerated here even
though it would be a completely normal, unremarkable choice in most other
schemas. This applies per chain, not globally, since multi-chain support
was added: the primary key is `(chain_id, entry_id)`, and `EntryID`'s "no
gaps" contract is scoped to one `chain_id` at a time.

## The mongo backend requires a replica set unconditionally

Unlike `gourdiantoken`'s `NewMongoTokenRepository(db *mongo.Database,
useTransactions bool)`, which makes transactions an opt-in escape hatch,
graudit's `NewMongoAuditLog` (`mongo.go`) has no such flag. Each chain's
correctness — not just its behavior under concurrent load — depends on the
multi-document transaction that atomically updates both the new entry and
that chain's own chain-state document (one per `ChainID`, keyed by `_id` =
the real `chainID` string — see "Multi-chain support" above). Running
without a transaction would let two concurrent `Record()` calls on the same
chain interleave in a way that corrupts it, so construction fails fast
(wrapping `ErrReplicaSetRequired`) against a standalone deployment rather
than silently degrading to a weaker mode.

**The fail-fast probe must perform a real operation, not a no-op.** A
pre-v0.1.0 audit found that `probeTransactionSupport`'s original
implementation ran a transaction whose body did nothing
(`return nil, nil`) — the MongoDB driver never sends an actual
transaction-start command to the server until a real operation is
attempted inside the transaction, so the no-op probe silently reported
"transactions supported" even against a genuinely standalone deployment.
Confirmed empirically: MongoDB only rejects a transaction with
`"Transaction numbers are only allowed on a replica set member or
mongos"` once a real read/write is attempted inside it. The fix inserts
and deletes a throwaway document (under a reserved `_id`,
`__graudit_transaction_probe__`, distinct from any real chain-state
document a caller's own `chainID` could ever produce) inside the probe
transaction, so the check is real but nothing persists either way. See
`mongo_test.go`'s `TestNewMongoAuditLog_RequiresReplicaSet` — this
test must never be reverted to `t.Skip`, since it is the only thing that
catches a regression of this exact bug.

## No TTL index in the mongo backend

grcache's own Mongo backend uses a TTL index (`expireAfterSeconds: 0`) as
its native expiry mechanism — caches are supposed to evict entries.
graudit's mongo backend has no TTL index anywhere: audit entries are never
expired. If a future retention/archival feature is added (see the plan
doc's roadmap), it belongs in its own explicit sweep mechanism, not a
database TTL index, since silent expiry is the opposite of what an audit
trail should ever do.

## `grevents.Bus` is injected via Config/Option, not part of `AuditLog`

graudit is the first repo in the ecosystem with a genuine *functional*
dependency on another gourdian repo: `Record()` publishes an event via
grevents on success. Rather than adding `Bus` methods to `AuditLog` itself,
every backend accepts an optional `EventBus grevents.Bus` (postgres/mongo
`Config` field; memory `WithEventBus` option), following the same
nil-safe, structurally-optional pattern as `Logger`. This keeps `AuditLog`
itself free of a grevents import and keeps publishing entirely orthogonal
to storage — a caller that never configures a bus gets identical behavior
to one that does, minus the publish.

## `Verify()`'s two-check design

It's tempting to implement `Verify()` as "recompute each entry's hash from
its own fields plus the previous entry's *recomputed* hash, and compare
against the stored hash." That single check does not correctly localize a
tampered entry: once entry N is tampered, every entry after N would also
fail to match (since their own PrevHash references N's *original* stored
hash, not the still-matching recomputed one), making every later entry
falsely appear "broken" too, instead of pinpointing entry N specifically.

The actual implementation (identical across `postgres` and `mongo`) runs
two independent checks per entry:

- **Check A (per-entry integrity)**: recompute the entry's hash from its
  own stored fields and its own stored `PrevHash`, compare against its
  stored `Hash`.
- **Check B (chain linkage)**: assert the entry's stored `PrevHash` equals
  the immediately preceding entry's stored `Hash`.

`VerifyResult.BrokenAt` is the lowest `EntryID` where either check fails —
this correctly identifies the first tampered/broken entry regardless of
how many entries follow it in the requested range.

## Payload storage and hash determinism across backends

`AuditEvent.Payload` is stored as JSON bytes (Postgres `jsonb`, a raw BSON
`[]byte` field in Mongo). When recomputing a hash for `Verify()` or
returning entries from `Query()`, backends must decode that JSON via
`graudit.DecodeStoredPayload` — never a plain `json.Unmarshal` into `any` —
because plain unmarshaling decodes every number as `float64`, and a
subsequent canonical re-marshal of that `float64` can reformat a large
integer (e.g. via exponential notation), producing a hash that no longer
matches the one computed at write time. `DecodeStoredPayload` uses
`json.Decoder.UseNumber()` specifically to avoid this; see `hash_test.go`'s
`TestDecodeStoredPayload_RoundTripPreservesHashForLargeInts` for the
regression test.

## Constructor shape: `Config` struct for networked backends, `Option`s for memory

Matches `grcache`'s own split: `NewPostgresAuditLog`/`NewMongoAuditLog`
each take a single `<Backend>Config` struct
(`New<Backend>AuditLog(cfg Config) (AuditLog, error)`); `NewMemoryAuditLog`
takes functional options (`NewMemoryAuditLog(opts ...MemoryOption)`,
renamed from the pre-flatten `Option` to avoid an overly generic
root-package-level name now that all three backends share one package),
since it has no connection details to configure.

`PostgresConfig` additionally accepts exactly one of `DSN` (dial a new
`pgxpool.Pool`) or `Pool` (reuse an already-open one the caller owns),
validated via `(cfg.DSN == "") == (cfg.Pool == nil)` in the shared
`connectPostgres` helper — mirroring grnoti's own
`PostgresConfig.Pool`/`connectPostgres`/`ownsPool` pattern exactly, added
specifically for multi-chain deployments where a single process serves
many tenant chains and shouldn't open one dedicated connection pool per
`AuditLog` instance to do it. `postgresAuditLog.ownsPool` records whether
`connectPostgres` dialed the pool itself (`true`, DSN path) or received it
externally (`false`, `Pool` path) — `Close()` only closes the pool in the
former case, since a pool the caller supplied may still be in use
elsewhere. Mongo and memory have no equivalent injection point (not
requested).

## No Redis/memcached backend

The original plan doc's own reasoning for excluding cache-backed storage
("an audit trail in a cache that can evict entries is a contradiction")
still holds for Redis/memcached specifically — those are genuinely cache
technologies with eviction as a first-class feature. MongoDB was added to
the backend list by explicit user decision despite grcache's own Mongo
backend also being a cache, because graudit's mongo backend deliberately
configures no TTL index and no eviction path (see above) — it uses
MongoDB purely as a durable document store, sidestepping the contradiction
that disqualified Redis/memcached.

## `Verify()`'s Check B has its own dedicated regression test

The shared contract suite's `VerifyDetectsTamper` scenario (in
`contract_audit_test.go`) only ever corrupts a stored entry's `Payload`
via each backend's `tamperHookFunc` — which changes the entry's
*recomputed* hash and so only ever exercises Check A (per-entry hash
integrity). Check B (chain linkage: each entry's stored `PrevHash` must
equal the immediately preceding entry's stored `Hash`) was, until an
internal coverage audit caught it, completely untested on every backend.
Each backend now has its own
`Test<Backend>AuditLog_VerifyDetectsChainLinkageBreak` test in
`internal_coverage_test.go`, directly corrupting a stored entry's
`PrevHash` (bypassing `Payload` entirely) and asserting `Verify` reports
the break at the correct `EntryID`.
