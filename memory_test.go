// File: memory_test.go

package graudit

import (
	"context"
	"testing"
)

func newMemoryLog() (AuditLog, error) {
	return NewMemoryAuditLog()
}

func newMemoryLogWithBus(bus *stubBus) (AuditLog, error) {
	return NewMemoryAuditLog(WithEventBus(bus))
}

func tamperMemoryEntry(t *testing.T, log AuditLog, chainID string, entryID EntryID) {
	t.Helper()
	a, ok := log.(*memoryAuditLog)
	if !ok {
		t.Fatalf("tamperMemoryEntry: log is %T, want *memoryAuditLog", log)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.entries {
		if a.entries[i].ChainID == chainID && a.entries[i].ID == entryID {
			a.entries[i].Payload = map[string]any{"tampered": true}
			return
		}
	}
	t.Fatalf("tamperMemoryEntry: entry (chain %q, id %d) not found", chainID, entryID)
}

// rehashMemoryEntry overwrites one entry's stored Hash (withLinkHooks).
func rehashMemoryEntry(t *testing.T, log AuditLog, chainID string, entryID EntryID) {
	t.Helper()
	a := log.(*memoryAuditLog)
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.entries {
		if a.entries[i].ChainID == chainID && a.entries[i].ID == entryID {
			a.entries[i].Hash = tamperedHash
			return
		}
	}
	t.Fatalf("rehashMemoryEntry: entry (chain %q, id %d) not found", chainID, entryID)
}

// deleteMemoryEntry removes one entry (withLinkHooks).
func deleteMemoryEntry(t *testing.T, log AuditLog, chainID string, entryID EntryID) {
	t.Helper()
	a := log.(*memoryAuditLog)
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.entries {
		if a.entries[i].ChainID == chainID && a.entries[i].ID == entryID {
			a.entries = append(a.entries[:i], a.entries[i+1:]...)
			return
		}
	}
	t.Fatalf("deleteMemoryEntry: entry (chain %q, id %d) not found", chainID, entryID)
}

func TestNewMemoryAuditLog_NeverFails(t *testing.T) {
	if _, err := NewMemoryAuditLog(); err != nil {
		t.Fatalf("NewMemoryAuditLog: %v, want nil", err)
	}
}

func TestWithLogger(t *testing.T) {
	rl := &recordingLogger{}
	a, err := NewMemoryAuditLog(WithLogger(rl))
	if err != nil {
		t.Fatalf("NewMemoryAuditLog: %v", err)
	}
	defer a.Close()
	if concrete := a.(*memoryAuditLog); concrete.logger != rl {
		t.Fatal("WithLogger did not install the given logger")
	}
}

// An entry whose stored fields were edited by moving bytes between two of
// them (here actor and entity type) must fail Verify: the hash preimage
// length-prefixes every field (v0.8.0). Before, this edit kept the hash.
func TestMemoryVerify_DetectsBytesMovedBetweenFields(t *testing.T) {
	ctx := context.Background()
	l, err := NewMemoryAuditLog()
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"alice", "bob"} {
		if _, err := l.Record(ctx, AuditEvent{ChainID: "platform", ActorID: actor, EntityType: "user", EntityID: "u1", Action: "user.updated"}); err != nil {
			t.Fatal(err)
		}
	}
	m := l.(*memoryAuditLog)
	m.mu.Lock()
	m.entries[0].ActorID, m.entries[0].EntityType = "aliceu", "ser"
	m.mu.Unlock()

	ok, res, err := l.Verify(ctx, "platform", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ok || res.BrokenAt != 1 {
		t.Errorf("Verify after moving bytes from entity type to actor: ok=%v broken at %d; want false at 1", ok, res.BrokenAt)
	}
}
