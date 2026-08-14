// File: docs.go

// Package graudit provides an append-only, tamper-evident audit trail for
// the gourdian ecosystem.
//
// # Overview
//
// graudit answers "what changed, who did it, and can we prove the record
// hasn't been altered" — a different question from what grlog answers
// ("what happened during this request"). It is a library component that
// could contribute to SOC2/HIPAA/PCI readiness, not a compliance
// certification product itself.
//
// # The Precise Claim (read this before trusting Verify())
//
// Hash-chaining proves internal consistency: nothing in the recorded range
// was altered or removed without Verify() detecting it. It does NOT prove
// who wrote a given entry beyond whatever ActorID the caller supplied, and
// it does NOT protect against a privileged attacker with direct database
// access regenerating the entire chain from scratch — a hash chain only
// detects partial tampering, not wholesale regeneration by someone who
// controls the storage. Do not let "tamper-evident" read as
// "cryptographically un-forgeable" anywhere this package is described.
//
// # Key Features
//
//   - A single AuditLog interface: Record, RecordChange, Verify, Query,
//     GetEntry, LatestEntryID, Close — implemented identically by all
//     three backends
//   - GetEntry(chainID, id) fetches one entry by its position via a
//     direct indexed/keyed lookup — O(1) on every backend, never a
//     Query-and-scan
//   - LatestEntryID(chainID) resolves a chain's current tail EntryID —
//     also the position Verify's to parameter resolves to via its
//     zero-value sentinel (see below)
//   - Multi-chain: every call is scoped by a required ChainID, so one
//     AuditLog instance (one connection pool) can serve any number of
//     independent hash chains — e.g. one per tenant in a multi-tenant
//     deployment, plus a separate chain for platform-operator actions
//   - Hash-chained entries: each entry's Hash covers its own fields
//     (including ChainID, so an entry can't be spliced from one chain into
//     another) plus the previous entry's Hash (see ComputeHash)
//   - Verify(chainID, from, to) recomputes one chain across a range and
//     reports the first broken link, if any, via VerifyResult
//   - RecordChange diffs a before/after pair and stores the diff as the
//     entry's Payload (see ChangeDiff)
//   - Publishes one grevents event (TopicAuditRecorded) per successful
//     write — graudit only ever publishes, never subscribes
//   - Sentinel errors (ErrClosed, ErrInvalidEvent, ErrBackendUnavailable,
//     ...) usable with errors.Is
//   - Optional diagnostic logging via any logger satisfying the small
//     Logger interface — *grlog.Logger satisfies it with no adapter
//     (proven at compile+run time by logger_test.go's
//     TestGrlogSatisfiesLoggerInterface, mirroring grcache's own
//     logger_test.go)
//
// # Getting Started
//
//	import (
//		"context"
//		"log"
//
//		"github.com/gourdian25/graudit"
//	)
//
//	func main() {
//		log_, err := graudit.NewMemoryAuditLog()
//		if err != nil {
//			log.Fatal(err)
//		}
//		defer log_.Close()
//
//		ctx := context.Background()
//		id, err := log_.Record(ctx, graudit.AuditEvent{
//			ChainID:    "tenant:acme",
//			ActorID:    "user:42",
//			EntityType: "invoice",
//			EntityID:   "inv_123",
//			Action:     "create",
//		})
//		if err != nil {
//			log.Fatal(err)
//		}
//		log.Println("recorded entry", id)
//	}
//
// # Multi-chain Support
//
// ChainID scopes every Record/RecordChange/Verify/Query call to one
// independent hash chain — EntryID sequences and PrevHash linkage are
// tracked per ChainID, not globally, so one AuditLog instance backed by one
// connection pool can serve any number of chains simultaneously (e.g. one
// per tenant in a multi-tenant deployment). ChainID is mandatory
// everywhere and there is no wildcard/query-all escape hatch: an empty
// ChainID fails loud with ErrChainIDRequired rather than silently matching
// every chain, since a cross-tenant leak in an audit trail is worse than
// the ergonomic cost of always specifying one. See
// docs/architecture.md's "Multi-chain support" section for why ChainID
// must be part of the hash preimage, not just a storage/filter column.
//
//	log_.Record(ctx, graudit.AuditEvent{ChainID: "tenant:acme", ...})
//	log_.Record(ctx, graudit.AuditEvent{ChainID: "platform:ops", ...})
//	ok, detail, err := log_.Verify(ctx, "tenant:acme", 1, 0) // to=0: verify through the latest entry
//
// # Backends
//
// graudit is a flat, single package — every backend's constructor and
// Config/Option type live directly in the graudit package (no subpackages
// to import selectively). This trades away per-backend dependency
// isolation for consistency with the rest of the gourdian ecosystem's
// flat-package convention; see docs/architecture.md for the full rationale.
//
// The first entry in every chain (EntryID 1) has PrevHash set to the
// exported GenesisPrevHash constant (64 zero characters, matching SHA-256's
// hex output length) rather than an empty string — Verify treats this as
// the documented base case, not a special "entry #0."
//
//  1. In-memory (NewMemoryAuditLog) — test/dev only, never for anything
//     you need to keep. Single-process; a sync.Mutex is both the storage
//     guard and the chain's serialization point.
//
//     log_, err := graudit.NewMemoryAuditLog()
//
//  2. PostgreSQL (NewPostgresAuditLog) — production-eligible, durable. Uses
//     pgx/v5 with sqlc-generated queries (no ORM). Chain serialization is a
//     pg_advisory_xact_lock scoped to (a fixed namespace, a per-ChainID
//     hash) and held for the duration of the transaction that reads the
//     tail and inserts the new entry, so unrelated chains' writes don't
//     serialize against each other; EntryID is explicitly assigned inside
//     that transaction, never a BIGSERIAL column (a sequence advances even
//     on rollback, which would silently create an EntryID gap — see
//     docs/architecture.md). PostgresConfig accepts either DSN (dials its
//     own pool) or an already-open Pool (shares one your application
//     already owns, e.g. across hundreds of tenant chains) — exactly one
//     of the two is required. Construction never applies schema itself —
//     apply PostgresSchemaSQL() through your own migration tool first, see
//     docs/architecture.md's "NewPostgresAuditLog never applies its own
//     schema" section.
//
//     log_, err := graudit.NewPostgresAuditLog(graudit.PostgresConfig{DSN: dsn})
//     log_, err := graudit.NewPostgresAuditLog(graudit.PostgresConfig{Pool: sharedPool})
//
//  3. MongoDB (NewMongoAuditLog) — production-eligible, durable. Uses
//     go.mongodb.org/mongo-driver v1. Chain serialization is a
//     multi-document ACID transaction (session.WithTransaction) covering a
//     singleton chain-state document and the new entry's insert. Requires
//     the target deployment to be a replica set (even single-node)
//     unconditionally — construction fails fast, wrapping
//     ErrReplicaSetRequired, against a standalone instance, rather than
//     silently degrading to non-transactional writes that could corrupt
//     the chain.
//
//     log_, err := graudit.NewMongoAuditLog(graudit.MongoConfig{URI: uri, Database: "myapp"})
//
// # grevents Integration
//
// Every backend accepts an optional grevents.Bus (via its Config/Option),
// defaulting to nil — no bus configured means Record simply doesn't
// publish, which is not an error. When configured, every successful Record
// publishes one TopicAuditRecorded event carrying the full recorded
// AuditEvent as Payload. A publish failure is logged, never propagated as a
// Record error.
//
//	import "github.com/gourdian25/grevents"
//
//	bus, _ := grevents.NewBus()
//	log_, err := graudit.NewPostgresAuditLog(graudit.PostgresConfig{
//		DSN:      dsn,
//		EventBus: bus,
//	})
//
// # Error Handling
//
//	id, err := log_.Record(ctx, event)
//	if errors.Is(err, graudit.ErrInvalidEvent) {
//		// caller bug: missing required field or bad Payload
//	} else if err != nil {
//		// backend unavailable or another real failure
//	}
//
// # Verify() Semantics
//
//	ok, detail, err := log_.Verify(ctx, chainID, 1, 0) // to=0: through the latest entry
//	if err != nil {
//		// operational failure — could not even attempt verification
//	}
//	if detail.Empty {
//		// chainID has no entries recorded yet — not a tampering signal
//	} else if !ok {
//		log.Printf("tampering detected at entry %d: expected %s, got %s",
//			detail.BrokenAt, detail.Expected, detail.Actual)
//	}
//
// to's zero value is a sentinel meaning "verify through chainID's current
// latest entry" (the same position LatestEntryID returns) rather than
// matching zero rows — a caller wanting a specific historical sub-range
// still passes an explicit non-zero to. VerifyResult.Empty is set only
// when chainID genuinely has no entries at all, distinguishing that case
// from a real full-chain pass (both otherwise report ok=true).
//
// # Testing
//
// graudit's own tests run against real local Postgres/MongoDB instances —
// no mocks — mirroring the ecosystem's testing philosophy. The Mongo
// backend's tests additionally require the instance to be configured as a
// (single-node is sufficient) replica set. See CLAUDE.md/README.md for
// exact connection settings and docker commands. A shared contract test
// suite (contract_audit_test.go, run via TestAuditLog_Contract's
// per-backend subtests) runs one behavioral suite (hash-chain integrity,
// tamper detection, concurrent Record ordering) against all three backends
// through the AuditLog interface — folded from a standalone conformance
// package into the root package's own tests for ecosystem consistency.
//
// # Out of Scope (v1)
//
// Full SOC2/HIPAA/PCI/ISO certification tooling, real-time alerting (a
// consumer's job, reacting to TopicAuditRecorded), auto-instrumentation
// (every entry comes from an explicit Record/RecordChange call, never
// automatic interception), cryptographic per-entry signatures (true
// non-repudiation — see the precise claim above), and archival/retention
// sweeping of old entries.
//
// License: MIT
// Repository: https://github.com/gourdian25/graudit
package graudit
