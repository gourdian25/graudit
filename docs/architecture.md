# Architecture

This document records graudit's deliberate divergences from sibling repo
conventions and other decisions worth calling out explicitly — check here
before "fixing" something that looks inconsistent with `grcache` or
`gourdiantoken`.

## No `SERIAL`/`BIGSERIAL` for `EntryID` in `graudit/postgres`

`EntryID` is explicitly assigned by application code inside the same
`pg_advisory_xact_lock`-held transaction that inserts the row, never a
Postgres `SERIAL`/`BIGSERIAL` column. A sequence advances even when its
transaction rolls back, which would silently create a gap in `EntryID` with
no corresponding entry — breaking the "strictly increasing, no gaps"
contract `EntryID`'s own doc comment states, and making a legitimate
rollback-gap indistinguishable from tampering. That ambiguity is exactly
what a hash chain exists to eliminate, so it can't be tolerated here even
though it would be a completely normal, unremarkable choice in most other
schemas.

## `graudit/mongo` requires a replica set unconditionally

Unlike `gourdiantoken`'s `NewMongoTokenRepository(db *mongo.Database,
useTransactions bool)`, which makes transactions an opt-in escape hatch,
`graudit/mongo`'s `NewMongoAuditLog` has no such flag. The chain's
correctness — not just its behavior under concurrent load — depends on the
multi-document transaction that atomically updates both the new entry and
the chain-state tail document. Running without a transaction would let two
concurrent `Record()` calls interleave in a way that corrupts the chain, so
construction fails fast (wrapping `ErrReplicaSetRequired`) against a
standalone deployment rather than silently degrading to a weaker mode.

## No TTL index in `graudit/mongo`

`grcache/mongo` uses a TTL index (`expireAfterSeconds: 0`) as its native
expiry mechanism — caches are supposed to evict entries. `graudit/mongo`
has no TTL index anywhere: audit entries are never expired. If a future
retention/archival feature is added (see the plan doc's roadmap), it
belongs in its own explicit sweep mechanism, not a database TTL index,
since silent expiry is the opposite of what an audit trail should ever do.

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

Matches `grcache`'s actual split: `graudit/postgres` and `graudit/mongo`
each take a single `<Backend>Config` struct (`New<Backend>AuditLog(cfg
Config) (graudit.AuditLog, error)`); `graudit/memory` takes functional
options (`NewMemoryAuditLog(opts ...Option)`), since it has no connection
details to configure.

## No Redis/memcached backend

The original plan doc's own reasoning for excluding cache-backed storage
("an audit trail in a cache that can evict entries is a contradiction")
still holds for Redis/memcached specifically — those are genuinely cache
technologies with eviction as a first-class feature. MongoDB was added to
the backend list by explicit user decision despite `grcache/mongo` also
being a cache backend, because `graudit/mongo` deliberately configures no
TTL index and no eviction path (see above) — it uses MongoDB purely as a
durable document store, sidestepping the contradiction that disqualified
Redis/memcached.
