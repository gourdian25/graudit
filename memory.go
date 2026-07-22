// File: memory.go

// In-memory backend (memoryAuditLog) for graudit — test/dev-only. It stores
// entries in a single mutex-protected slice — the mutex is both the storage
// guard and the chain's serialization point — and does not coordinate state
// across processes or replicas. Never use this backend for anything you
// need to keep; use graudit's postgres or mongo backend for durable
// storage.
package graudit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gourdian25/grevents"
)

// MemoryOption configures an AuditLog constructed by NewMemoryAuditLog.
type MemoryOption func(*memoryAuditLog)

// WithLogger installs an optional Logger for diagnostic messages (grevents
// publish failures). Logging is always opt-in.
func WithLogger(l Logger) MemoryOption {
	return func(a *memoryAuditLog) {
		a.logger = OrNop(l)
	}
}

// WithEventBus installs an optional grevents.Bus. When set, every
// successful Record publishes a TopicAuditRecorded event. A nil/
// unconfigured bus (the default) means Record simply doesn't publish —
// not an error.
func WithEventBus(bus grevents.Bus) MemoryOption {
	return func(a *memoryAuditLog) {
		a.bus = bus
	}
}

// memoryAuditLog is an in-memory implementation of AuditLog. It has zero
// external dependencies and does not coordinate state across processes or
// replicas — running it behind multiple instances of an application means
// each instance has its own independent, diverging chain, which is
// expected, not a bug. Use graudit's postgres or mongo backend for state
// that must be shared and durable.
type memoryAuditLog struct {
	mu       sync.Mutex // guards entries + lastHash + lastID together: this IS the serialization point
	entries  []AuditEvent
	lastHash string
	lastID   EntryID

	logger Logger
	bus    grevents.Bus

	closed    atomic.Bool
	closeOnce sync.Once
}

var _ AuditLog = (*memoryAuditLog)(nil)

// NewMemoryAuditLog constructs a ready-to-use in-memory AuditLog.
// Construction never fails — the error return exists only to match the
// signature every other backend's constructor uses.
//
// Parameters:
//   - opts: ...MemoryOption — WithLogger and/or WithEventBus; both optional
//
// Returns:
//   - AuditLog: ready to use immediately
//   - error: always nil
func NewMemoryAuditLog(opts ...MemoryOption) (AuditLog, error) {
	a := &memoryAuditLog{
		logger: NopLogger(),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a, nil
}

// Record implements AuditLog.Record; see the interface's doc comment for
// the full contract. Locking here doubles as the chain's single-writer
// serialization point — the mutex is held across the entire
// read-tail/compute-hash/append sequence.
func (a *memoryAuditLog) Record(ctx context.Context, event AuditEvent) (EntryID, error) {
	if a.closed.Load() {
		return 0, ErrClosed
	}
	if err := event.Validate(); err != nil {
		return 0, fmt.Errorf("graudit: record: %w", err)
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	a.mu.Lock()

	prevHash := GenesisPrevHash
	if a.lastID > 0 {
		prevHash = a.lastHash
	}
	nextID := a.lastID + 1

	hash, err := ComputeHash(nextID, event.ActorID, event.EntityType, event.EntityID, event.Action, event.Payload, event.Timestamp, prevHash)
	if err != nil {
		a.mu.Unlock()
		return 0, fmt.Errorf("graudit: record: %w", err)
	}

	event.ID = nextID
	event.Hash = hash
	event.PrevHash = prevHash

	a.entries = append(a.entries, event)
	a.lastID = nextID
	a.lastHash = hash

	a.mu.Unlock()

	PublishRecorded(ctx, a.bus, a.logger, event)
	return nextID, nil
}

// RecordChange implements AuditLog.RecordChange; see the interface's doc
// comment for the full contract.
func (a *memoryAuditLog) RecordChange(ctx context.Context, actorID, entityType, entityID string, before, after any) (EntryID, error) {
	event, err := BuildChangeEvent(actorID, entityType, entityID, before, after)
	if err != nil {
		return 0, fmt.Errorf("graudit: record change: %w", err)
	}
	return a.Record(ctx, event)
}

// Verify implements AuditLog.Verify; see the interface's doc comment for
// the full contract and verifyChain's doc comment for the two-check
// design.
func (a *memoryAuditLog) Verify(ctx context.Context, from, to EntryID) (bool, VerifyResult, error) {
	if a.closed.Load() {
		return false, VerifyResult{}, ErrClosed
	}
	if from < 1 {
		from = 1
	}

	a.mu.Lock()
	entries := make([]AuditEvent, 0, len(a.entries))
	for _, e := range a.entries {
		if e.ID >= from && e.ID <= to {
			entries = append(entries, e)
		}
	}
	a.mu.Unlock()

	return verifyMemoryChain(entries)
}

// verifyMemoryChain applies the two required checks to entries (already
// ordered by ID, contiguous within the requested range): Check A
// recomputes each entry's hash from its own stored fields and compares
// against its stored Hash; Check B asserts each entry's stored PrevHash
// equals the previous entry's stored Hash. Both are needed — Check A alone
// would not detect a deleted entry or a rewritten PrevHash pointing at a
// tampered predecessor.
func verifyMemoryChain(entries []AuditEvent) (bool, VerifyResult, error) {
	var prevHash string
	for i, e := range entries {
		expectPrev := GenesisPrevHash
		if i > 0 {
			expectPrev = prevHash
		}
		if e.PrevHash != expectPrev {
			return false, VerifyResult{
				Valid: false, BrokenAt: e.ID, Expected: expectPrev, Actual: e.PrevHash,
			}, nil
		}

		recomputed, err := ComputeHash(e.ID, e.ActorID, e.EntityType, e.EntityID, e.Action, e.Payload, e.Timestamp, e.PrevHash)
		if err != nil {
			return false, VerifyResult{}, fmt.Errorf("graudit: verify: %w", err)
		}
		if recomputed != e.Hash {
			return false, VerifyResult{
				Valid: false, BrokenAt: e.ID, Expected: recomputed, Actual: e.Hash,
			}, nil
		}

		prevHash = e.Hash
	}
	return true, VerifyResult{Valid: true}, nil
}

// Query implements AuditLog.Query; see the interface's doc comment for the
// full contract. This is a linear scan over the in-memory slice —
// acceptable specifically because this is the test/dev-only backend, never
// the production one.
func (a *memoryAuditLog) Query(ctx context.Context, filter QueryFilter) ([]AuditEvent, error) {
	if a.closed.Load() {
		return nil, ErrClosed
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	var out []AuditEvent
	for _, e := range a.entries {
		if filter.ActorID != "" && e.ActorID != filter.ActorID {
			continue
		}
		if filter.EntityType != "" && e.EntityType != filter.EntityType {
			continue
		}
		if filter.EntityID != "" && e.EntityID != filter.EntityID {
			continue
		}
		if !filter.From.IsZero() && e.Timestamp.Before(filter.From) {
			continue
		}
		if !filter.To.IsZero() && e.Timestamp.After(filter.To) {
			continue
		}
		out = append(out, e)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

// Close implements AuditLog.Close; idempotent via sync.Once.
func (a *memoryAuditLog) Close() error {
	a.closeOnce.Do(func() {
		a.closed.Store(true)
	})
	return nil
}
