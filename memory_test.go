// File: memory_test.go

package graudit

import "testing"

func newMemoryLog() (AuditLog, error) {
	return NewMemoryAuditLog()
}

func newMemoryLogWithBus(bus *stubBus) (AuditLog, error) {
	return NewMemoryAuditLog(WithEventBus(bus))
}

func tamperMemoryEntry(t *testing.T, log AuditLog, entryID EntryID) {
	t.Helper()
	a, ok := log.(*memoryAuditLog)
	if !ok {
		t.Fatalf("tamperMemoryEntry: log is %T, want *memoryAuditLog", log)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.entries {
		if a.entries[i].ID == entryID {
			a.entries[i].Payload = map[string]any{"tampered": true}
			return
		}
	}
	t.Fatalf("tamperMemoryEntry: entry %d not found", entryID)
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
