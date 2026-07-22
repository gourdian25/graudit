// File: audit.go

package graudit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// GenesisPrevHash is the well-known PrevHash value for the first entry in a
// chain — 64 zero characters, matching SHA-256's hex output length.
// Exported so every backend and Verify() agree on one value instead of each
// hardcoding its own; computed via strings.Repeat rather than a hand-typed
// literal, since a miscounted zero would be exactly the kind of subtle
// determinism bug this package exists to prevent.
var GenesisPrevHash = strings.Repeat("0", 64)

// EntryID is a chain position: strictly increasing starting at 1, never a
// UUID. Backends assign it explicitly (never via a database auto-increment
// sequence — see each backend's doc comment for why) so that "no gaps" is
// an invariant Verify() can rely on.
type EntryID uint64

// AuditLog is the primary interface all backends implement. Every backend
// file (memory.go, postgres.go, mongo.go) provides a constructor returning
// an AuditLog, so application code can depend on this interface alone and
// swap backends without changing call sites.
type AuditLog interface {
	// Record computes the entry's hash (its own fields plus the previous
	// entry's hash), appends it durably, and publishes a
	// TopicAuditRecorded event via the backend's configured grevents.Bus on
	// success (a nil/unconfigured bus, or a publish failure, never causes
	// Record itself to fail — the durable write is the source of truth,
	// grevents is a best-effort side channel; a publish failure is only
	// logged via the backend's configured Logger).
	//
	// Parameters:
	//   - ctx: context.Context
	//   - event: AuditEvent — ActorID, EntityType, EntityID, and Action are
	//     required; ID, Hash, PrevHash are set by Record and any input
	//     value is ignored; Timestamp defaults to time.Now() if zero
	//
	// Returns:
	//   - EntryID: the newly assigned, strictly-sequential chain position
	//   - error: wraps ErrInvalidEvent for a missing required field or a
	//     non-JSON-serializable Payload, ErrClosed if called after Close,
	//     ErrBackendUnavailable for a storage failure
	Record(ctx context.Context, event AuditEvent) (EntryID, error)

	// RecordChange is a convenience wrapper: computes a field-level diff
	// between before and after (see ChangeDiff), and calls Record with that
	// diff as the Payload and Action left to the caller's convention (a
	// small controlled vocabulary — "create"/"update"/"delete" — is
	// recommended but not enforced).
	//
	// Parameters:
	//   - ctx: context.Context
	//   - actorID, entityType, entityID: string — all required, same as AuditEvent
	//   - before, after: any — JSON-serializable snapshots of the entity's
	//     state; either may be nil (nil before means "creation", nil after
	//     means "deletion")
	//
	// Returns:
	//   - EntryID: see Record
	//   - error: see Record, plus any error from computing the diff itself
	//     (a before/after value that cannot be marshaled to a JSON object)
	RecordChange(ctx context.Context, actorID, entityType, entityID string, before, after any) (EntryID, error)

	// Verify recomputes the hash chain across [from, to] (inclusive) and
	// confirms integrity via two checks per entry: that each entry's stored
	// Hash matches one recomputed from its own stored fields, and that each
	// entry's stored PrevHash matches the immediately preceding entry's
	// stored Hash. ok=false (with a non-nil detail, no error) means
	// tampering or corruption was detected; err is reserved for genuine
	// operational failures (e.g. can't reach the backend).
	//
	// Parameters:
	//   - ctx: context.Context
	//   - from, to: EntryID — inclusive range; from must be >= 1
	//
	// Returns:
	//   - ok: bool — true if every entry in range passes both checks
	//   - detail: VerifyResult — see its own doc comment
	//   - err: error — non-nil only for an operational failure, never for
	//     detected tampering (that is reported via detail, with ok=false)
	Verify(ctx context.Context, from, to EntryID) (ok bool, detail VerifyResult, err error)

	// Query returns entries matching filter, ordered oldest-first.
	//
	// Parameters:
	//   - ctx: context.Context
	//   - filter: QueryFilter — zero value matches every entry, subject to
	//     Limit (0 means no limit)
	//
	// Returns:
	//   - []AuditEvent: entries matching filter, oldest-first
	//   - error: ErrClosed if called after Close, ErrBackendUnavailable for
	//     a storage failure
	Query(ctx context.Context, filter QueryFilter) ([]AuditEvent, error)

	// Close releases any underlying connections/resources. After Close,
	// every other method returns ErrClosed. Close is idempotent.
	Close() error
}

// AuditEvent is a single entry in the chain.
type AuditEvent struct {
	// ID is this entry's chain position. Set by Record; any input value is
	// ignored.
	ID EntryID

	// ActorID identifies who performed the action. Required. graudit does
	// not verify this beyond non-emptiness — it proves nothing about
	// authorship beyond what the caller asserts (see docs.go's precise
	// tamper-evidence claim).
	ActorID string

	// EntityType and EntityID together identify what was acted on (e.g.
	// EntityType "user", EntityID "42"). Both required.
	EntityType string
	EntityID   string

	// Action describes what happened, e.g. "create"/"update"/"delete".
	// Freeform but a small controlled vocabulary is recommended. Required.
	Action string

	// Payload is arbitrary JSON-serializable detail — typically a
	// ChangeDiff produced by RecordChange, or caller-supplied structured
	// data for a direct Record call. May be nil.
	Payload any

	// Timestamp is set by Record if left zero on input.
	Timestamp time.Time

	// Hash is set by Record; hex-encoded SHA-256.
	Hash string

	// PrevHash is set by Record; hex-encoded SHA-256 of the previous entry,
	// or GenesisPrevHash for entry #1.
	PrevHash string
}

// Validate checks that e has the minimum fields required to be recorded and
// that Payload can be JSON-marshaled at all. Every backend calls this once,
// at the top of Record, before doing anything durable — exported (not just
// an unexported helper) so that every backend (memory.go, postgres.go,
// mongo.go) applies identical validation despite each implementing Record
// independently.
func (e AuditEvent) Validate() error {
	switch {
	case e.ActorID == "":
		return fmt.Errorf("%w: ActorID is required", ErrInvalidEvent)
	case e.EntityType == "":
		return fmt.Errorf("%w: EntityType is required", ErrInvalidEvent)
	case e.EntityID == "":
		return fmt.Errorf("%w: EntityID is required", ErrInvalidEvent)
	case e.Action == "":
		return fmt.Errorf("%w: Action is required", ErrInvalidEvent)
	}
	if e.Payload != nil {
		if _, err := json.Marshal(e.Payload); err != nil {
			return fmt.Errorf("%w: Payload is not JSON-serializable: %v", ErrInvalidEvent, err)
		}
	}
	return nil
}

// QueryFilter selects entries for Query. The zero value matches every
// entry. Any combination of fields may be set together.
type QueryFilter struct {
	ActorID    string
	EntityType string
	EntityID   string
	From, To   time.Time
	Limit      int // 0 means no limit
}

// VerifyResult is the detailed outcome of Verify.
type VerifyResult struct {
	// Valid is true if every entry in the requested range passed both
	// integrity checks.
	Valid bool

	// BrokenAt is the lowest EntryID where a check failed; zero if Valid.
	BrokenAt EntryID

	// Expected and Actual report whichever hash values disagreed at
	// BrokenAt: for a per-entry integrity failure, Expected is the
	// recomputed hash and Actual is the entry's stored Hash; for a
	// chain-linkage failure, Expected is the previous entry's stored Hash
	// and Actual is this entry's stored PrevHash. Both empty if Valid.
	Expected string
	Actual   string
}
