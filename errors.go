// File: errors.go

package graudit

import "errors"

// Sentinel errors for use with errors.Is. Backend implementations translate
// their own native errors (gorm.ErrRecordNotFound, mongo.ErrNoDocuments,
// ...) into these sentinels before wrapping with fmt.Errorf("...: %w", ...)
// — a backend-native error must never leak through the AuditLog interface
// unwrapped.
//
// There is deliberately no IsX(err error) bool helper: callers use
// errors.Is(err, graudit.ErrClosed) directly, consistent with every other
// gourdian repo's sentinel-error convention.
var (
	// ErrClosed indicates a method was called after Close.
	ErrClosed = errors.New("graudit: audit log is closed")

	// ErrInvalidEvent indicates an AuditEvent passed to Record is missing a
	// required field or has a Payload that cannot be JSON-marshaled.
	ErrInvalidEvent = errors.New("graudit: invalid audit event")

	// ErrEntryNotFound indicates a query for a specific entry found none.
	ErrEntryNotFound = errors.New("graudit: entry not found")

	// ErrChainCorrupted indicates a backend-level invariant violation it
	// cannot even attempt to answer (e.g. a missing Mongo chain-state
	// document, or a Postgres row with an EntryID gap) — distinct from
	// Verify() returning VerifyResult.Valid == false, which is the designed
	// tamper-detection path, not an error. ErrChainCorrupted means the
	// backend itself is in a state Verify() cannot even evaluate.
	ErrChainCorrupted = errors.New("graudit: chain state corrupted")

	// ErrBackendUnavailable indicates the backend could not be reached
	// (connection failure, timeout, transaction failure, etc.).
	ErrBackendUnavailable = errors.New("graudit: backend unavailable")

	// ErrReplicaSetRequired indicates the graudit/mongo backend was
	// constructed against a standalone (non-replica-set) MongoDB
	// deployment. Multi-document transactions — which the chain's
	// serialization strategy depends on for correctness, not just
	// performance — require a replica set, even a single-node one.
	ErrReplicaSetRequired = errors.New("graudit: mongo backend requires a replica-set deployment")
)
