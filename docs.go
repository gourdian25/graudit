// File: docs.go

// Package graudit provides an append-only, tamper-evident audit trail for
// the gourdian ecosystem.
//
// Overview:
//
// graudit answers "what changed, who did it, and can we prove the record
// hasn't been altered" — a different question from what grlog answers
// ("what happened during this request"). It is a library component that
// could contribute to SOC2/HIPAA/PCI readiness, not a compliance
// certification product itself.
//
// The Precise Claim (read this before trusting Verify()):
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
// Key Features:
//   - A single AuditLog interface: Record, RecordChange, Verify, Query,
//     Close — implemented identically by all three backends
//   - Hash-chained entries: each entry's Hash covers its own fields plus
//     the previous entry's Hash (see ComputeHash)
//   - Verify(from, to) recomputes the chain across a range and reports the
//     first broken link, if any, via VerifyResult
//   - RecordChange diffs a before/after pair and stores the diff as the
//     entry's Payload (see ChangeDiff)
//   - Publishes one grevents event (TopicAuditRecorded) per successful
//     write — graudit only ever publishes, never subscribes
//   - Sentinel errors (ErrClosed, ErrInvalidEvent, ErrBackendUnavailable,
//     ...) usable with errors.Is
//   - Optional diagnostic logging via any logger satisfying the small
//     Logger interface — grlog.Logger works with no adapter
//
// Getting Started:
//
//	import (
//		"context"
//		"log"
//
//		"github.com/gourdian25/graudit/memory"
//	)
//
//	func main() {
//		log_, err := memory.NewMemoryAuditLog()
//		if err != nil {
//			log.Fatal(err)
//		}
//		defer log_.Close()
//
//		ctx := context.Background()
//		id, err := log_.Record(ctx, graudit.AuditEvent{
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
// Backends:
//
// Each backend lives in its own importable subpackage so that consumers who
// only need one backend don't pull in the other backends' client libraries.
//
//  1. In-memory (graudit/memory) — test/dev only, never for anything you
//     need to keep. Single-process; a sync.Mutex is both the storage guard
//     and the chain's serialization point.
//
//     log_, err := memory.NewMemoryAuditLog()
//
//  2. PostgreSQL (graudit/postgres) — production-eligible, durable. Uses
//     GORM. Chain serialization is a pg_advisory_xact_lock held for the
//     duration of the transaction that reads the tail and inserts the new
//     entry; EntryID is explicitly assigned inside that transaction, never
//     a SERIAL/BIGSERIAL column (a sequence advances even on rollback,
//     which would silently create an EntryID gap — see docs/architecture.md).
//
//     log_, err := postgres.NewPostgresAuditLog(postgres.PostgresConfig{DSN: dsn})
//
//  3. MongoDB (graudit/mongo) — production-eligible, durable. Uses
//     go.mongodb.org/mongo-driver v1. Chain serialization is a
//     multi-document ACID transaction (session.WithTransaction) covering a
//     singleton chain-state document and the new entry's insert. Requires
//     the target deployment to be a replica set (even single-node)
//     unconditionally — construction fails fast, wrapping
//     ErrReplicaSetRequired, against a standalone instance, rather than
//     silently degrading to non-transactional writes that could corrupt
//     the chain.
//
//     log_, err := mongo.NewMongoAuditLog(mongo.MongoConfig{URI: uri, Database: "myapp"})
//
// grevents Integration:
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
//	log_, err := postgres.NewPostgresAuditLog(postgres.PostgresConfig{
//		DSN:      dsn,
//		EventBus: bus,
//	})
//
// Error Handling:
//
//	id, err := log_.Record(ctx, event)
//	if errors.Is(err, graudit.ErrInvalidEvent) {
//		// caller bug: missing required field or bad Payload
//	} else if err != nil {
//		// backend unavailable or another real failure
//	}
//
// Verify() Semantics:
//
//	ok, detail, err := log_.Verify(ctx, 1, latestID)
//	if err != nil {
//		// operational failure — could not even attempt verification
//	}
//	if !ok {
//		log.Printf("tampering detected at entry %d: expected %s, got %s",
//			detail.BrokenAt, detail.Expected, detail.Actual)
//	}
//
// Testing:
//
// graudit's own tests run against real local Postgres/MongoDB instances —
// no mocks — mirroring the ecosystem's testing philosophy. The Mongo
// backend's tests additionally require the instance to be configured as a
// (single-node is sufficient) replica set. See CLAUDE.md/README.md for
// exact connection settings and docker commands. A shared conformance
// package runs one behavioral suite (hash-chain integrity, tamper
// detection, concurrent Record ordering) against all three backends
// through the AuditLog interface.
//
// Out of Scope (v1):
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
