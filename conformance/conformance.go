// File: conformance/conformance.go

// Package conformance is a shared behavioral test suite run against every
// graudit backend via the common graudit.AuditLog interface. It enforces
// identical behavior across backends for every scenario it covers (see Run)
// and is the primary test artifact for each backend package, which
// supplies its own constructors to Run and adds backend-specific tests
// (e.g. connection-failure handling, replica-set requirements) separately.
//
// This package imports only the root graudit package, never a backend
// subpackage — each backend's own test file imports conformance sideways,
// which is what avoids an import cycle in the subpackage-per-backend
// layout.
package conformance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gourdian25/graudit"
	"github.com/gourdian25/grevents"
)

// RunOption configures Run's behavior for scenarios that need
// backend-specific hooks.
type RunOption func(*runConfig)

// TamperHook mutates one entry's stored Payload directly, bypassing the
// AuditLog interface entirely — raw SQL for postgres, a raw driver call for
// mongo (both typically ignoring log and instead connecting independently
// to the same known test DSN/URI), or a white-box type-assertion back to
// the concrete type for memory (whose state is only reachable in-process).
// log is the exact instance VerifyDetectsTamper is about to re-Verify, so
// backends that do need it (memory) have it available.
type TamperHook func(t *testing.T, log graudit.AuditLog, entryID graudit.EntryID)

type runConfig struct {
	tamperHook TamperHook
}

// WithTamperHook supplies the backend-specific mechanism for
// VerifyDetectsTamper to mutate one entry's stored Payload directly,
// simulating an attacker or a bug elsewhere touching storage directly. If
// omitted, VerifyDetectsTamper is skipped with a clear message; every
// backend is expected to supply one, since this is one of the plan's named
// mandatory adversarial tests.
func WithTamperHook(hook TamperHook) RunOption {
	return func(cfg *runConfig) { cfg.tamperHook = hook }
}

// NewLogFunc constructs a fresh, empty AuditLog for one scenario.
type NewLogFunc func() (graudit.AuditLog, error)

// NewLogWithBusFunc constructs a fresh, empty AuditLog wired to bus — used
// only by the PublishOnRecord/PublishFailureDoesNotFailRecord scenarios,
// which need to inject a test-double grevents.Bus. Every backend supports
// this since EventBus/WithEventBus is a standard Config/Option field.
type NewLogWithBusFunc func(bus grevents.Bus) (graudit.AuditLog, error)

// Run executes the full conformance suite. Every backend's own test file
// calls this with its own constructors so the same behavioral assertions
// run identically against all three backends.
//
// Example:
//
//	func TestConformance(t *testing.T) {
//		conformance.Run(t,
//			func() (graudit.AuditLog, error) { return memory.NewMemoryAuditLog() },
//			func(bus grevents.Bus) (graudit.AuditLog, error) { return memory.NewMemoryAuditLog(memory.WithEventBus(bus)) },
//		)
//	}
func Run(t *testing.T, newLog NewLogFunc, newLogWithBus NewLogWithBusFunc, opts ...RunOption) {
	t.Helper()

	cfg := &runConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	t.Run("GenesisEntry", func(t *testing.T) { testGenesisEntry(t, newLog) })
	t.Run("SequentialEntryIDs", func(t *testing.T) { testSequentialEntryIDs(t, newLog) })
	t.Run("VerifyOnSingleEntry", func(t *testing.T) { testVerifyOnSingleEntry(t, newLog) })
	t.Run("ConcurrentRecordStress", func(t *testing.T) { testConcurrentRecordStress(t, newLog) })
	t.Run("HashDeterminism", func(t *testing.T) { testHashDeterminism(t, newLog) })
	t.Run("VerifyDetectsTamper", func(t *testing.T) { testVerifyDetectsTamper(t, newLog, cfg.tamperHook) })
	t.Run("RecordChangeDiff", func(t *testing.T) { testRecordChangeDiff(t, newLog) })
	t.Run("QueryFilters", func(t *testing.T) { testQueryFilters(t, newLog) })
	t.Run("PublishOnRecord", func(t *testing.T) { testPublishOnRecord(t, newLogWithBus) })
	t.Run("PublishFailureDoesNotFailRecord", func(t *testing.T) { testPublishFailureDoesNotFailRecord(t, newLogWithBus) })
	t.Run("PostClose", func(t *testing.T) { testPostClose(t, newLog) })
}

func testGenesisEntry(t *testing.T, newLog NewLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	id, err := log.Record(ctx, graudit.AuditEvent{ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if id != 1 {
		t.Fatalf("first Record returned EntryID %d, want 1", id)
	}

	entries, err := log.Query(ctx, graudit.QueryFilter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Query returned %d entries, want 1", len(entries))
	}
	if entries[0].PrevHash != graudit.GenesisPrevHash {
		t.Fatalf("genesis entry PrevHash = %q, want GenesisPrevHash", entries[0].PrevHash)
	}
}

func testSequentialEntryIDs(t *testing.T, newLog NewLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	const n = 20
	for i := 0; i < n; i++ {
		id, err := log.Record(ctx, graudit.AuditEvent{ActorID: "actor:1", EntityType: "widget", EntityID: fmt.Sprintf("w%d", i), Action: "create"})
		if err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
		if int(id) != i+1 {
			t.Fatalf("Record #%d returned EntryID %d, want %d", i, id, i+1)
		}
	}

	ok, detail, err := log.Verify(ctx, 1, n)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Fatalf("Verify after %d sequential records: %+v", n, detail)
	}
}

func testVerifyOnSingleEntry(t *testing.T, newLog NewLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	if _, err := log.Record(ctx, graudit.AuditEvent{ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	ok, detail, err := log.Verify(ctx, 1, 1)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Fatalf("Verify on a length-1 chain: %+v", detail)
	}
}

// testConcurrentRecordStress is the single most important scenario in the
// suite: it directly proves each backend's serialization strategy (mutex /
// advisory lock / Mongo transaction) actually works under real concurrency,
// not just that it compiles.
func testConcurrentRecordStress(t *testing.T, newLog NewLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	const workers = 10
	const perWorker = 10
	total := workers * perWorker

	var wg sync.WaitGroup
	ids := make([]graudit.EntryID, total)
	errs := make([]error, total)
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				idx := w*perWorker + i
				id, err := log.Record(ctx, graudit.AuditEvent{
					ActorID: fmt.Sprintf("actor:%d", w), EntityType: "stress", EntityID: fmt.Sprintf("e%d", idx), Action: "create",
				})
				ids[idx] = id
				errs[idx] = err
			}
		}(w)
	}
	wg.Wait()

	seen := make(map[graudit.EntryID]bool, total)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
		if seen[ids[i]] {
			t.Fatalf("duplicate EntryID %d assigned", ids[i])
		}
		seen[ids[i]] = true
	}
	for id := graudit.EntryID(1); id <= graudit.EntryID(total); id++ {
		if !seen[id] {
			t.Fatalf("EntryID %d missing — chain has a gap", id)
		}
	}

	ok, detail, err := log.Verify(ctx, 1, graudit.EntryID(total))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Fatalf("Verify after %d concurrent records: %+v", total, detail)
	}
}

// testHashDeterminism records logically-identical entries (same ActorID,
// EntityType, EntityID, Action, and a fixed Timestamp so time.Now() jitter
// can't interfere) on two fresh, independent AuditLog instances — one with
// its payload map keys in one insertion order, the other reordered — and
// confirms the stored Hash is identical. Run end-to-end through Record and
// Query (not just the pure unit test in the root package's hash_test.go)
// to catch a backend accidentally re-serializing the payload differently
// before hashing or on read-back (e.g. GORM/driver JSON reordering).
func testHashDeterminism(t *testing.T, newLog NewLogFunc) {
	t.Helper()
	ctx := context.Background()
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	record := func(payload map[string]any) string {
		t.Helper()
		log, err := newLog()
		if err != nil {
			t.Fatalf("newLog: %v", err)
		}
		defer log.Close()

		if _, err := log.Record(ctx, graudit.AuditEvent{
			ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create", Payload: payload, Timestamp: ts,
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
		entries, err := log.Query(ctx, graudit.QueryFilter{})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("Query returned %d entries, want 1", len(entries))
		}
		return entries[0].Hash
	}

	hashA := record(map[string]any{"x": 1, "y": 2, "z": "three"})
	hashB := record(map[string]any{"z": "three", "x": 1, "y": 2})

	if hashA != hashB {
		t.Fatalf("expected identical hashes for logically-identical payloads with different key order, got %q vs %q", hashA, hashB)
	}
}

func testVerifyDetectsTamper(t *testing.T, newLog NewLogFunc, tamperHook TamperHook) {
	t.Helper()
	if tamperHook == nil {
		t.Skip("no WithTamperHook supplied for this backend")
	}

	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	const n = 5
	var tamperedID graudit.EntryID
	for i := 0; i < n; i++ {
		id, err := log.Record(ctx, graudit.AuditEvent{ActorID: "actor:1", EntityType: "widget", EntityID: fmt.Sprintf("w%d", i), Action: "create"})
		if err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
		if i == 2 {
			tamperedID = id
		}
	}

	if ok, _, err := log.Verify(ctx, 1, n); err != nil || !ok {
		t.Fatalf("Verify before tampering: ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	tamperHook(t, log, tamperedID)

	ok, detail, err := log.Verify(ctx, 1, n)
	if err != nil {
		t.Fatalf("Verify after tampering: %v", err)
	}
	if ok {
		t.Fatal("Verify after tampering returned ok=true, want false")
	}
	if detail.BrokenAt != tamperedID {
		t.Fatalf("VerifyResult.BrokenAt = %d, want %d", detail.BrokenAt, tamperedID)
	}
}

func testRecordChangeDiff(t *testing.T, newLog NewLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	before := map[string]any{"name": "alice", "age": 30}
	after := map[string]any{"name": "alice", "age": 31}

	id, err := log.RecordChange(ctx, "actor:1", "person", "p1", before, after)
	if err != nil {
		t.Fatalf("RecordChange: %v", err)
	}

	entries, err := log.Query(ctx, graudit.QueryFilter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != id {
		t.Fatalf("Query returned %+v, want one entry with ID %d", entries, id)
	}

	diff, ok := entries[0].Payload.(graudit.ChangeDiff)
	if !ok {
		t.Fatalf("Payload type = %T, want graudit.ChangeDiff", entries[0].Payload)
	}
	if _, ok := diff["name"]; ok {
		t.Fatal("unchanged field \"name\" should not appear in the diff")
	}
	if _, ok := diff["age"]; !ok {
		t.Fatal("expected \"age\" in the diff")
	}
	if entries[0].Action != "update" {
		t.Fatalf("Action = %q, want %q", entries[0].Action, "update")
	}
}

func testQueryFilters(t *testing.T, newLog NewLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	if _, err := log.Record(ctx, graudit.AuditEvent{ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := log.Record(ctx, graudit.AuditEvent{ActorID: "actor:2", EntityType: "widget", EntityID: "w2", Action: "create"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := log.Record(ctx, graudit.AuditEvent{ActorID: "actor:1", EntityType: "gadget", EntityID: "g1", Action: "create"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	byActor, err := log.Query(ctx, graudit.QueryFilter{ActorID: "actor:1"})
	if err != nil {
		t.Fatalf("Query(ActorID): %v", err)
	}
	if len(byActor) != 2 {
		t.Fatalf("Query(ActorID=actor:1) returned %d entries, want 2", len(byActor))
	}

	byEntity, err := log.Query(ctx, graudit.QueryFilter{EntityType: "widget", EntityID: "w2"})
	if err != nil {
		t.Fatalf("Query(EntityType/EntityID): %v", err)
	}
	if len(byEntity) != 1 {
		t.Fatalf("Query(EntityType=widget,EntityID=w2) returned %d entries, want 1", len(byEntity))
	}

	limited, err := log.Query(ctx, graudit.QueryFilter{Limit: 1})
	if err != nil {
		t.Fatalf("Query(Limit): %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("Query(Limit=1) returned %d entries, want 1", len(limited))
	}
}

// stubBus is a minimal grevents.Bus test double for PublishOnRecord and
// PublishFailureDoesNotFailRecord — kept unexported here rather than
// reimplemented per backend.
type stubBus struct {
	mu         sync.Mutex
	published  []grevents.Event
	publishErr error
}

func (b *stubBus) Publish(ctx context.Context, event grevents.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.published = append(b.published, event)
	return b.publishErr
}
func (b *stubBus) Subscribe(topic string, handler grevents.HandlerFunc, opts ...grevents.SubscribeOption) (grevents.Unsubscribe, error) {
	return func() {}, nil
}
func (b *stubBus) Use(mw grevents.Middleware)                        {}
func (b *stubBus) Stats(ctx context.Context) (grevents.Stats, error) { return grevents.Stats{}, nil }
func (b *stubBus) Close() error                                      { return nil }

func (b *stubBus) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.published)
}

func (b *stubBus) last() grevents.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.published[len(b.published)-1]
}

var _ grevents.Bus = (*stubBus)(nil)

func testPublishOnRecord(t *testing.T, newLogWithBus NewLogWithBusFunc) {
	t.Helper()
	ctx := context.Background()
	bus := &stubBus{}
	log, err := newLogWithBus(bus)
	if err != nil {
		t.Fatalf("newLogWithBus: %v", err)
	}
	defer log.Close()

	id, err := log.Record(ctx, graudit.AuditEvent{ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	if got := bus.count(); got != 1 {
		t.Fatalf("expected exactly 1 Publish call, got %d", got)
	}
	event := bus.last()
	if event.Topic != graudit.TopicAuditRecorded {
		t.Fatalf("published Topic = %q, want %q", event.Topic, graudit.TopicAuditRecorded)
	}
	payload, ok := event.Payload.(graudit.AuditEvent)
	if !ok || payload.ID != id {
		t.Fatalf("published Payload = %+v, want AuditEvent with ID %d", event.Payload, id)
	}
}

func testPublishFailureDoesNotFailRecord(t *testing.T, newLogWithBus NewLogWithBusFunc) {
	t.Helper()
	ctx := context.Background()
	bus := &stubBus{publishErr: errors.New("bus unavailable")}
	log, err := newLogWithBus(bus)
	if err != nil {
		t.Fatalf("newLogWithBus: %v", err)
	}
	defer log.Close()

	id, err := log.Record(ctx, graudit.AuditEvent{ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create"})
	if err != nil {
		t.Fatalf("Record with a failing bus: %v, want nil (publish failures must not fail Record)", err)
	}
	if id == 0 {
		t.Fatal("Record returned a zero EntryID despite a nil error")
	}

	entries, err := log.Query(ctx, graudit.QueryFilter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != id {
		t.Fatalf("entry not durably queryable after a publish failure: %+v", entries)
	}
}

func testPostClose(t *testing.T, newLog NewLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}

	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("second Close: %v, want nil (idempotent)", err)
	}

	if _, err := log.Record(ctx, graudit.AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); !errors.Is(err, graudit.ErrClosed) {
		t.Fatalf("Record after Close error = %v, want ErrClosed", err)
	}
	if _, _, err := log.Verify(ctx, 1, 1); !errors.Is(err, graudit.ErrClosed) {
		t.Fatalf("Verify after Close error = %v, want ErrClosed", err)
	}
	if _, err := log.Query(ctx, graudit.QueryFilter{}); !errors.Is(err, graudit.ErrClosed) {
		t.Fatalf("Query after Close error = %v, want ErrClosed", err)
	}
}
