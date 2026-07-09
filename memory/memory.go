// File: memory/memory.go

// Package memory is graudit's test/dev-only backend. It stores entries in a
// single mutex-protected slice — the mutex is both the storage guard and
// the chain's serialization point — and does not coordinate state across
// processes or replicas. Never use this backend for anything you need to
// keep; use graudit/postgres or graudit/mongo for durable storage.
package memory

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gourdian25/graudit"
	"github.com/gourdian25/grevents"
)

// Option configures an AuditLog constructed by NewMemoryAuditLog.
type Option func(*AuditLog)

// WithLogger installs an optional graudit.Logger for diagnostic messages
// (grevents publish failures). Logging is always opt-in.
func WithLogger(l graudit.Logger) Option {
	return func(a *AuditLog) {
		a.logger = graudit.OrNop(l)
	}
}

// WithEventBus installs an optional grevents.Bus. When set, every
// successful Record publishes a graudit.TopicAuditRecorded event. A nil/
// unconfigured bus (the default) means Record simply doesn't publish —
// not an error.
func WithEventBus(bus grevents.Bus) Option {
	return func(a *AuditLog) {
		a.bus = bus
	}
}

// AuditLog is an in-memory implementation of graudit.AuditLog. It has zero
// external dependencies and does not coordinate state across processes or
// replicas — running it behind multiple instances of an application means
// each instance has its own independent, diverging chain, which is
// expected, not a bug. Use graudit/postgres or graudit/mongo for state that
// must be shared and durable.
type AuditLog struct {
	mu       sync.Mutex // guards entries + lastHash + lastID together: this IS the serialization point
	entries  []graudit.AuditEvent
	lastHash string
	lastID   graudit.EntryID

	logger graudit.Logger
	bus    grevents.Bus

	closed    atomic.Bool
	closeOnce sync.Once
}

var _ graudit.AuditLog = (*AuditLog)(nil)

// NewMemoryAuditLog constructs a ready-to-use in-memory AuditLog.
// Construction never fails — the error return exists only to match the
// signature every other backend's constructor uses.
//
// Parameters:
//   - opts: ...Option — WithLogger and/or WithEventBus; both optional
//
// Returns:
//   - graudit.AuditLog: ready to use immediately
//   - error: always nil
func NewMemoryAuditLog(opts ...Option) (graudit.AuditLog, error) {
	a := &AuditLog{
		logger: graudit.NopLogger(),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a, nil
}

func (a *AuditLog) Record(ctx context.Context, event graudit.AuditEvent) (graudit.EntryID, error) {
	if a.closed.Load() {
		return 0, graudit.ErrClosed
	}
	if err := event.Validate(); err != nil {
		return 0, fmt.Errorf("graudit/memory: record: %w", err)
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	a.mu.Lock()

	prevHash := graudit.GenesisPrevHash
	if a.lastID > 0 {
		prevHash = a.lastHash
	}
	nextID := a.lastID + 1

	hash, err := graudit.ComputeHash(nextID, event.ActorID, event.EntityType, event.EntityID, event.Action, event.Payload, event.Timestamp, prevHash)
	if err != nil {
		a.mu.Unlock()
		return 0, fmt.Errorf("graudit/memory: record: %w", err)
	}

	event.ID = nextID
	event.Hash = hash
	event.PrevHash = prevHash

	a.entries = append(a.entries, event)
	a.lastID = nextID
	a.lastHash = hash

	a.mu.Unlock()

	graudit.PublishRecorded(ctx, a.bus, a.logger, event)
	return nextID, nil
}

func (a *AuditLog) RecordChange(ctx context.Context, actorID, entityType, entityID string, before, after any) (graudit.EntryID, error) {
	event, err := graudit.BuildChangeEvent(actorID, entityType, entityID, before, after)
	if err != nil {
		return 0, fmt.Errorf("graudit/memory: record change: %w", err)
	}
	return a.Record(ctx, event)
}

func (a *AuditLog) Verify(ctx context.Context, from, to graudit.EntryID) (bool, graudit.VerifyResult, error) {
	if a.closed.Load() {
		return false, graudit.VerifyResult{}, graudit.ErrClosed
	}
	if from < 1 {
		from = 1
	}

	a.mu.Lock()
	entries := make([]graudit.AuditEvent, 0, len(a.entries))
	for _, e := range a.entries {
		if e.ID >= from && e.ID <= to {
			entries = append(entries, e)
		}
	}
	a.mu.Unlock()

	return verifyChain(entries)
}

// verifyChain applies the two required checks to entries (already ordered
// by ID, contiguous within the requested range): Check A recomputes each
// entry's hash from its own stored fields and compares against its stored
// Hash; Check B asserts each entry's stored PrevHash equals the previous
// entry's stored Hash. Both are needed — Check A alone would not detect a
// deleted entry or a rewritten PrevHash pointing at a tampered predecessor.
func verifyChain(entries []graudit.AuditEvent) (bool, graudit.VerifyResult, error) {
	var prevHash string
	for i, e := range entries {
		expectPrev := graudit.GenesisPrevHash
		if i > 0 {
			expectPrev = prevHash
		}
		if e.PrevHash != expectPrev {
			return false, graudit.VerifyResult{
				Valid: false, BrokenAt: e.ID, Expected: expectPrev, Actual: e.PrevHash,
			}, nil
		}

		recomputed, err := graudit.ComputeHash(e.ID, e.ActorID, e.EntityType, e.EntityID, e.Action, e.Payload, e.Timestamp, e.PrevHash)
		if err != nil {
			return false, graudit.VerifyResult{}, fmt.Errorf("graudit/memory: verify: %w", err)
		}
		if recomputed != e.Hash {
			return false, graudit.VerifyResult{
				Valid: false, BrokenAt: e.ID, Expected: recomputed, Actual: e.Hash,
			}, nil
		}

		prevHash = e.Hash
	}
	return true, graudit.VerifyResult{Valid: true}, nil
}

func (a *AuditLog) Query(ctx context.Context, filter graudit.QueryFilter) ([]graudit.AuditEvent, error) {
	if a.closed.Load() {
		return nil, graudit.ErrClosed
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	var out []graudit.AuditEvent
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

func (a *AuditLog) Close() error {
	a.closeOnce.Do(func() {
		a.closed.Store(true)
	})
	return nil
}
