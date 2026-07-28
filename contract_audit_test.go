// File: contract_audit_test.go

// White-box (package graudit, not graudit_test) specifically so the memory
// backend's tamper hook can reach memoryAuditLog's unexported fields
// directly — memory's entire state lives in-process with no separate
// storage to connect to independently, unlike the postgres/mongo tamper
// hooks, which connect via a raw client/driver call to the same known test
// DSN/URI instead.
//
// This was originally a separate, publicly-importable conformance package
// (graudit/conformance, imported sideways by each backend subpackage's own
// test file to avoid an import cycle in the old subpackage-per-backend
// layout) — folded into the root package's own tests once every backend
// lived in one flat package, matching the rest of the gourdian ecosystem's
// convention (see grnoti's own contract_*_test.go files).
package graudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// testChainID and testChainID2 are used throughout this suite: testChainID
// for every scenario that only needs one chain (the vast majority — chain
// scoping itself isn't what they're testing), testChainID2 only by the
// scenarios that specifically prove chain isolation. Named distinctly
// (not "chain-1"/"chain-2") so an accidental argument transposition
// between a chainID and an adjacent string parameter is obvious in a diff.
const (
	testChainID  = "test-chain-a"
	testChainID2 = "test-chain-b"
)

// TestAuditLog_Contract runs the full contract suite against all three
// backends. Postgres and Mongo skip gracefully if their live local service
// isn't reachable, matching the rest of the gourdian ecosystem's
// convention; the in-memory backend never skips, since it has no external
// dependency to be unavailable.
func TestAuditLog_Contract(t *testing.T) {
	t.Run("Memory", func(t *testing.T) {
		runAuditContract(t, newMemoryLog, newMemoryLogWithBus, withTamperHook(tamperMemoryEntry))
	})

	t.Run("Postgres", func(t *testing.T) {
		log, err := newPostgresLog()
		if err != nil {
			t.Skipf("PostgreSQL not available, skipping: %v", err)
		}
		_ = log.Close()
		runAuditContract(t, newPostgresLog, newPostgresLogWithBus, withTamperHook(tamperPostgresEntry))
	})

	t.Run("Mongo", func(t *testing.T) {
		log, err := newMongoLog()
		if err != nil {
			t.Skipf("MongoDB not available, skipping: %v", err)
		}
		_ = log.Close()
		runAuditContract(t, newMongoLog, newMongoLogWithBus, withTamperHook(tamperMongoEntry))
	})
}

// auditContractOption configures runAuditContract's behavior for scenarios
// that need backend-specific hooks.
type auditContractOption func(*auditContractConfig)

// tamperHookFunc mutates one entry's stored Payload directly, bypassing the
// AuditLog interface entirely — raw SQL for postgres, a raw driver call for
// mongo (both typically ignoring log and instead connecting independently
// to the same known test DSN/URI), or a white-box type-assertion back to
// the concrete type for memory (whose state is only reachable in-process).
// log is the exact instance testVerifyDetectsTamper is about to re-Verify,
// so backends that do need it (memory) have it available. chainID is
// required alongside entryID: EntryID alone no longer identifies an entry
// uniquely, since every chain independently restarts its own sequence at 1.
type tamperHookFunc func(t *testing.T, log AuditLog, chainID string, entryID EntryID)

type auditContractConfig struct {
	tamperHook tamperHookFunc
}

// withTamperHook supplies the backend-specific mechanism for
// testVerifyDetectsTamper to mutate one entry's stored Payload directly,
// simulating an attacker or a bug elsewhere touching storage directly. If
// omitted, testVerifyDetectsTamper is skipped with a clear message; every
// backend is expected to supply one, since this is one of the plan's named
// mandatory adversarial tests.
func withTamperHook(hook tamperHookFunc) auditContractOption {
	return func(cfg *auditContractConfig) { cfg.tamperHook = hook }
}

// newLogFunc constructs a fresh, empty AuditLog for one scenario.
type newLogFunc func() (AuditLog, error)

// newLogWithBusFunc constructs a fresh, empty AuditLog wired to bus — used
// only by the PublishOnRecord/PublishFailureDoesNotFailRecord scenarios,
// which need to inject a test-double grevents.Bus. Every backend supports
// this since EventBus/WithEventBus is a standard Config/Option field.
type newLogWithBusFunc func(bus *stubBus) (AuditLog, error)

// runAuditContract executes the full contract suite. TestAuditLog_Contract
// calls this once per backend with that backend's own constructors so the
// same behavioral assertions run identically against all three.
func runAuditContract(t *testing.T, newLog newLogFunc, newLogWithBus newLogWithBusFunc, opts ...auditContractOption) {
	t.Helper()

	cfg := &auditContractConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	t.Run("GenesisEntry", func(t *testing.T) { testGenesisEntry(t, newLog) })
	t.Run("SequentialEntryIDs", func(t *testing.T) { testSequentialEntryIDs(t, newLog) })
	t.Run("VerifyOnSingleEntry", func(t *testing.T) { testVerifyOnSingleEntry(t, newLog) })
	t.Run("ConcurrentRecordStress", func(t *testing.T) { testConcurrentRecordStress(t, newLog) })
	t.Run("ConcurrentRecordStressMultiChain", func(t *testing.T) { testConcurrentRecordStressMultiChain(t, newLog) })
	t.Run("HashDeterminism", func(t *testing.T) { testHashDeterminism(t, newLog) })
	t.Run("VerifyDetectsTamper", func(t *testing.T) { testVerifyDetectsTamper(t, newLog, cfg.tamperHook) })
	t.Run("RecordChangeDiff", func(t *testing.T) { testRecordChangeDiff(t, newLog) })
	t.Run("QueryFilters", func(t *testing.T) { testQueryFilters(t, newLog) })
	t.Run("ChainIsolation", func(t *testing.T) { testChainIsolation(t, newLog) })
	t.Run("ChainIsolationTamperContainment", func(t *testing.T) { testChainIsolationTamperContainment(t, newLog, cfg.tamperHook) })
	t.Run("PublishOnRecord", func(t *testing.T) { testPublishOnRecord(t, newLogWithBus) })
	t.Run("PublishFailureDoesNotFailRecord", func(t *testing.T) { testPublishFailureDoesNotFailRecord(t, newLogWithBus) })
	t.Run("PostClose", func(t *testing.T) { testPostClose(t, newLog) })
}

func testGenesisEntry(t *testing.T, newLog newLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	id, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if id != 1 {
		t.Fatalf("first Record returned EntryID %d, want 1", id)
	}

	entries, err := log.Query(ctx, QueryFilter{ChainID: testChainID})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Query returned %d entries, want 1", len(entries))
	}
	if entries[0].PrevHash != GenesisPrevHash {
		t.Fatalf("genesis entry PrevHash = %q, want GenesisPrevHash", entries[0].PrevHash)
	}
}

func testSequentialEntryIDs(t *testing.T, newLog newLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	const n = 20
	for i := 0; i < n; i++ {
		id, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "actor:1", EntityType: "widget", EntityID: fmt.Sprintf("w%d", i), Action: "create"})
		if err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
		if int(id) != i+1 {
			t.Fatalf("Record #%d returned EntryID %d, want %d", i, id, i+1)
		}
	}

	ok, detail, err := log.Verify(ctx, testChainID, 1, n)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Fatalf("Verify after %d sequential records: %+v", n, detail)
	}
}

func testVerifyOnSingleEntry(t *testing.T, newLog newLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	if _, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	ok, detail, err := log.Verify(ctx, testChainID, 1, 1)
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
func testConcurrentRecordStress(t *testing.T, newLog newLogFunc) {
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
	ids := make([]EntryID, total)
	errs := make([]error, total)
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				idx := w*perWorker + i
				id, err := log.Record(ctx, AuditEvent{
					ChainID: testChainID, ActorID: fmt.Sprintf("actor:%d", w), EntityType: "stress", EntityID: fmt.Sprintf("e%d", idx), Action: "create",
				})
				ids[idx] = id
				errs[idx] = err
			}
		}(w)
	}
	wg.Wait()

	seen := make(map[EntryID]bool, total)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
		if seen[ids[i]] {
			t.Fatalf("duplicate EntryID %d assigned", ids[i])
		}
		seen[ids[i]] = true
	}
	for id := EntryID(1); id <= EntryID(total); id++ {
		if !seen[id] {
			t.Fatalf("EntryID %d missing — chain has a gap", id)
		}
	}

	ok, detail, err := log.Verify(ctx, testChainID, 1, EntryID(total))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Fatalf("Verify after %d concurrent records: %+v", total, detail)
	}
}

// testConcurrentRecordStressMultiChain interleaves concurrent Record calls
// across two independent chains on the same AuditLog instance, asserting
// each chain's own EntryID sequence is gap-free/duplicate-free and Verify
// passes per chain. This is the correctness counterpart to the
// architectural claims in docs/architecture.md (postgres's interim global
// advisory lock still serializes correctly across chains; mongo's
// per-chain chain-state documents never contend with each other) — pure
// correctness assertions, no timing/throughput assertions, so it can't be
// flaky in CI regardless of which backend's concurrency strategy is faster.
func testConcurrentRecordStressMultiChain(t *testing.T, newLog newLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	const workersPerChain = 8
	const perWorker = 8
	totalPerChain := workersPerChain * perWorker
	chains := []string{testChainID, testChainID2}

	var wg sync.WaitGroup
	ids := make(map[string][]EntryID, len(chains))
	errs := make(map[string][]error, len(chains))
	var mu sync.Mutex
	for _, chainID := range chains {
		ids[chainID] = make([]EntryID, totalPerChain)
		errs[chainID] = make([]error, totalPerChain)
	}

	wg.Add(workersPerChain * len(chains))
	for _, chainID := range chains {
		chainID := chainID
		for w := 0; w < workersPerChain; w++ {
			go func(w int) {
				defer wg.Done()
				for i := 0; i < perWorker; i++ {
					idx := w*perWorker + i
					id, err := log.Record(ctx, AuditEvent{
						ChainID: chainID, ActorID: fmt.Sprintf("actor:%d", w), EntityType: "stress", EntityID: fmt.Sprintf("e%d", idx), Action: "create",
					})
					mu.Lock()
					ids[chainID][idx] = id
					errs[chainID][idx] = err
					mu.Unlock()
				}
			}(w)
		}
	}
	wg.Wait()

	for _, chainID := range chains {
		seen := make(map[EntryID]bool, totalPerChain)
		for i, err := range errs[chainID] {
			if err != nil {
				t.Fatalf("chain %q Record #%d: %v", chainID, i, err)
			}
			if seen[ids[chainID][i]] {
				t.Fatalf("chain %q: duplicate EntryID %d assigned", chainID, ids[chainID][i])
			}
			seen[ids[chainID][i]] = true
		}
		for id := EntryID(1); id <= EntryID(totalPerChain); id++ {
			if !seen[id] {
				t.Fatalf("chain %q: EntryID %d missing — chain has a gap", chainID, id)
			}
		}

		ok, detail, err := log.Verify(ctx, chainID, 1, EntryID(totalPerChain))
		if err != nil {
			t.Fatalf("chain %q Verify: %v", chainID, err)
		}
		if !ok {
			t.Fatalf("chain %q Verify after %d concurrent records: %+v", chainID, totalPerChain, detail)
		}
	}
}

// testHashDeterminism records logically-identical entries (same ChainID,
// ActorID, EntityType, EntityID, Action, and a fixed Timestamp so
// time.Now() jitter can't interfere) on two fresh, independent AuditLog
// instances — one with its payload map keys in one insertion order, the
// other reordered — and confirms the stored Hash is identical. Run
// end-to-end through Record and Query (not just the pure unit test in
// hash_test.go) to catch a backend accidentally re-serializing the payload
// differently before hashing or on read-back.
func testHashDeterminism(t *testing.T, newLog newLogFunc) {
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

		if _, err := log.Record(ctx, AuditEvent{
			ChainID: testChainID, ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create", Payload: payload, Timestamp: ts,
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
		entries, err := log.Query(ctx, QueryFilter{ChainID: testChainID})
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

func testVerifyDetectsTamper(t *testing.T, newLog newLogFunc, tamperHook tamperHookFunc) {
	t.Helper()
	if tamperHook == nil {
		t.Skip("no withTamperHook supplied for this backend")
	}

	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	const n = 5
	var tamperedID EntryID
	for i := 0; i < n; i++ {
		id, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "actor:1", EntityType: "widget", EntityID: fmt.Sprintf("w%d", i), Action: "create"})
		if err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
		if i == 2 {
			tamperedID = id
		}
	}

	if ok, _, err := log.Verify(ctx, testChainID, 1, n); err != nil || !ok {
		t.Fatalf("Verify before tampering: ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	tamperHook(t, log, testChainID, tamperedID)

	ok, detail, err := log.Verify(ctx, testChainID, 1, n)
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

func testRecordChangeDiff(t *testing.T, newLog newLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	before := map[string]any{"name": "alice", "age": 30}
	after := map[string]any{"name": "alice", "age": 31}

	id, err := log.RecordChange(ctx, testChainID, "actor:1", "person", "p1", before, after)
	if err != nil {
		t.Fatalf("RecordChange: %v", err)
	}

	entries, err := log.Query(ctx, QueryFilter{ChainID: testChainID})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != id {
		t.Fatalf("Query returned %+v, want one entry with ID %d", entries, id)
	}

	// Payload's concrete Go type depends on the backend: memory preserves
	// the original ChangeDiff value in-process, while postgres/mongo
	// round-trip it through JSON storage and hand back a generic
	// map[string]any (DecodeStoredPayload has no way to know the original
	// concrete type was ChangeDiff — only the JSON shape survives). Both
	// are correct, expected backend-specific behavior, not a bug, so this
	// assertion normalizes through JSON rather than asserting a concrete Go
	// type.
	raw, err := json.Marshal(entries[0].Payload)
	if err != nil {
		t.Fatalf("marshal Payload for inspection: %v", err)
	}
	var diff map[string]json.RawMessage
	if err := json.Unmarshal(raw, &diff); err != nil {
		t.Fatalf("Payload did not marshal to a JSON object: %v (raw: %s)", err, raw)
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

func testQueryFilters(t *testing.T, newLog newLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	// Explicit, well-separated timestamps (not time.Now()) so the From/To
	// range assertions below are deterministic even under a backend whose
	// storage only preserves millisecond precision (the mongo backend) —
	// three real time.Now() calls in a tight loop can land in the same
	// millisecond and make the range filters indistinguishable.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create", Timestamp: base}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "actor:2", EntityType: "widget", EntityID: "w2", Action: "create", Timestamp: base.Add(time.Minute)}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "actor:1", EntityType: "gadget", EntityID: "g1", Action: "create", Timestamp: base.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	byActor, err := log.Query(ctx, QueryFilter{ChainID: testChainID, ActorID: "actor:1"})
	if err != nil {
		t.Fatalf("Query(ActorID): %v", err)
	}
	if len(byActor) != 2 {
		t.Fatalf("Query(ActorID=actor:1) returned %d entries, want 2", len(byActor))
	}

	byEntity, err := log.Query(ctx, QueryFilter{ChainID: testChainID, EntityType: "widget", EntityID: "w2"})
	if err != nil {
		t.Fatalf("Query(EntityType/EntityID): %v", err)
	}
	if len(byEntity) != 1 {
		t.Fatalf("Query(EntityType=widget,EntityID=w2) returned %d entries, want 1", len(byEntity))
	}

	limited, err := log.Query(ctx, QueryFilter{ChainID: testChainID, Limit: 1})
	if err != nil {
		t.Fatalf("Query(Limit): %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("Query(Limit=1) returned %d entries, want 1", len(limited))
	}

	all, err := log.Query(ctx, QueryFilter{ChainID: testChainID})
	if err != nil {
		t.Fatalf("Query(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("Query(all) returned %d entries, want 3", len(all))
	}
	mid := all[1].Timestamp

	byFrom, err := log.Query(ctx, QueryFilter{ChainID: testChainID, From: mid})
	if err != nil {
		t.Fatalf("Query(From): %v", err)
	}
	if len(byFrom) != 2 {
		t.Fatalf("Query(From=entry[1].Timestamp) returned %d entries, want 2", len(byFrom))
	}

	byTo, err := log.Query(ctx, QueryFilter{ChainID: testChainID, To: mid})
	if err != nil {
		t.Fatalf("Query(To): %v", err)
	}
	if len(byTo) != 2 {
		t.Fatalf("Query(To=entry[1].Timestamp) returned %d entries, want 2", len(byTo))
	}

	byRange, err := log.Query(ctx, QueryFilter{ChainID: testChainID, From: mid, To: mid})
	if err != nil {
		t.Fatalf("Query(From,To): %v", err)
	}
	if len(byRange) != 1 {
		t.Fatalf("Query(From=To=entry[1].Timestamp) returned %d entries, want 1", len(byRange))
	}
}

// testChainIsolation records into two independent chains on the same
// AuditLog instance and confirms: each chain's EntryID sequence
// independently starts at 1 (they collide numerically), and Verify/Query
// scoped to one chain never see the other's entries.
func testChainIsolation(t *testing.T, newLog newLogFunc) {
	t.Helper()
	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	idA1, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create"})
	if err != nil {
		t.Fatalf("Record chain A #1: %v", err)
	}
	idB1, err := log.Record(ctx, AuditEvent{ChainID: testChainID2, ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create"})
	if err != nil {
		t.Fatalf("Record chain B #1: %v", err)
	}
	idA2, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "actor:1", EntityType: "widget", EntityID: "w2", Action: "create"})
	if err != nil {
		t.Fatalf("Record chain A #2: %v", err)
	}

	if idA1 != 1 || idB1 != 1 {
		t.Fatalf("expected both chains' first entry to be EntryID 1 independently, got chain A=%d chain B=%d", idA1, idB1)
	}
	if idA2 != 2 {
		t.Fatalf("expected chain A's second entry to be EntryID 2, got %d", idA2)
	}

	entriesA, err := log.Query(ctx, QueryFilter{ChainID: testChainID})
	if err != nil {
		t.Fatalf("Query chain A: %v", err)
	}
	if len(entriesA) != 2 {
		t.Fatalf("Query(chain A) returned %d entries, want 2", len(entriesA))
	}
	entriesB, err := log.Query(ctx, QueryFilter{ChainID: testChainID2})
	if err != nil {
		t.Fatalf("Query chain B: %v", err)
	}
	if len(entriesB) != 1 {
		t.Fatalf("Query(chain B) returned %d entries, want 1", len(entriesB))
	}

	okA, detailA, err := log.Verify(ctx, testChainID, 1, idA2)
	if err != nil {
		t.Fatalf("Verify chain A: %v", err)
	}
	if !okA {
		t.Fatalf("Verify(chain A): %+v", detailA)
	}
	okB, detailB, err := log.Verify(ctx, testChainID2, 1, idB1)
	if err != nil {
		t.Fatalf("Verify chain B: %v", err)
	}
	if !okB {
		t.Fatalf("Verify(chain B): %+v", detailB)
	}
}

// testChainIsolationTamperContainment tampers an entry in chain A and
// confirms Verify(chain B, ...) still reports ok=true — tampering in one
// tenant's chain must never cascade into another tenant's Verify result.
// This is the single most important new scenario in this suite: it's the
// direct proof of the feature's actual purpose (tenant isolation), the
// same role ConcurrentRecordStress/VerifyDetectsTamper play for the
// single-chain design.
func testChainIsolationTamperContainment(t *testing.T, newLog newLogFunc, tamperHook tamperHookFunc) {
	t.Helper()
	if tamperHook == nil {
		t.Skip("no withTamperHook supplied for this backend")
	}

	ctx := context.Background()
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	var tamperedID EntryID
	const n = 3
	for i := 0; i < n; i++ {
		id, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "actor:1", EntityType: "widget", EntityID: fmt.Sprintf("wa%d", i), Action: "create"})
		if err != nil {
			t.Fatalf("Record chain A #%d: %v", i, err)
		}
		if i == 1 {
			tamperedID = id
		}
	}
	var lastB EntryID
	for i := 0; i < n; i++ {
		id, err := log.Record(ctx, AuditEvent{ChainID: testChainID2, ActorID: "actor:1", EntityType: "widget", EntityID: fmt.Sprintf("wb%d", i), Action: "create"})
		if err != nil {
			t.Fatalf("Record chain B #%d: %v", i, err)
		}
		lastB = id
	}

	tamperHook(t, log, testChainID, tamperedID)

	okA, _, err := log.Verify(ctx, testChainID, 1, n)
	if err != nil {
		t.Fatalf("Verify chain A after tampering: %v", err)
	}
	if okA {
		t.Fatal("Verify(chain A) after tampering chain A returned ok=true, want false")
	}

	okB, detailB, err := log.Verify(ctx, testChainID2, 1, lastB)
	if err != nil {
		t.Fatalf("Verify chain B after tampering chain A: %v", err)
	}
	if !okB {
		t.Fatalf("Verify(chain B) after tampering only chain A returned ok=false, want true (tampering must not cascade across chains): %+v", detailB)
	}
}

func testPublishOnRecord(t *testing.T, newLogWithBus newLogWithBusFunc) {
	t.Helper()
	ctx := context.Background()
	bus := &stubBus{}
	log, err := newLogWithBus(bus)
	if err != nil {
		t.Fatalf("newLogWithBus: %v", err)
	}
	defer log.Close()

	id, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	if got := bus.count(); got != 1 {
		t.Fatalf("expected exactly 1 Publish call, got %d", got)
	}
	event := bus.last()
	if event.Topic != TopicAuditRecorded {
		t.Fatalf("published Topic = %q, want %q", event.Topic, TopicAuditRecorded)
	}
	payload, ok := event.Payload.(AuditEvent)
	if !ok || payload.ID != id {
		t.Fatalf("published Payload = %+v, want AuditEvent with ID %d", event.Payload, id)
	}
}

func testPublishFailureDoesNotFailRecord(t *testing.T, newLogWithBus newLogWithBusFunc) {
	t.Helper()
	ctx := context.Background()
	bus := &stubBus{publishErr: errors.New("bus unavailable")}
	log, err := newLogWithBus(bus)
	if err != nil {
		t.Fatalf("newLogWithBus: %v", err)
	}
	defer log.Close()

	id, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Action: "create"})
	if err != nil {
		t.Fatalf("Record with a failing bus: %v, want nil (publish failures must not fail Record)", err)
	}
	if id == 0 {
		t.Fatal("Record returned a zero EntryID despite a nil error")
	}

	entries, err := log.Query(ctx, QueryFilter{ChainID: testChainID})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != id {
		t.Fatalf("entry not durably queryable after a publish failure: %+v", entries)
	}
}

// testPostClose checks every AuditLog method's behavior after Close,
// including RecordChange — a real gap in the pre-flatten conformance suite,
// which only ever exercised Record/Verify/Query, leaving RecordChange's
// post-Close error wrapping (which flows through Record's own ErrClosed
// check) unverified end-to-end.
func testPostClose(t *testing.T, newLog newLogFunc) {
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

	if _, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Record after Close error = %v, want ErrClosed", err)
	}
	if _, err := log.RecordChange(ctx, testChainID, "a", "t", "1", nil, map[string]any{"x": 1}); !errors.Is(err, ErrClosed) {
		t.Fatalf("RecordChange after Close error = %v, want ErrClosed", err)
	}
	if _, _, err := log.Verify(ctx, testChainID, 1, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("Verify after Close error = %v, want ErrClosed", err)
	}
	if _, err := log.Query(ctx, QueryFilter{ChainID: testChainID}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Query after Close error = %v, want ErrClosed", err)
	}
}
