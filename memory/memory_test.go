// File: memory/memory_test.go

// White-box (package memory, not memory_test) specifically so
// tamperEntry can reach AuditLog's unexported fields directly — memory's
// entire state lives in-process with no separate storage to connect to
// independently, unlike postgres/mongo whose tamper hooks connect via a
// raw client to the same known test DSN/URI instead.
package memory

import (
	"testing"

	"github.com/gourdian25/graudit"
	"github.com/gourdian25/graudit/conformance"
	"github.com/gourdian25/grevents"
)

func newLog() (graudit.AuditLog, error) {
	return NewMemoryAuditLog()
}

func newLogWithBus(bus grevents.Bus) (graudit.AuditLog, error) {
	return NewMemoryAuditLog(WithEventBus(bus))
}

func tamperEntry(t *testing.T, log graudit.AuditLog, entryID graudit.EntryID) {
	t.Helper()
	a, ok := log.(*AuditLog)
	if !ok {
		t.Fatalf("tamperEntry: log is %T, want *AuditLog", log)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.entries {
		if a.entries[i].ID == entryID {
			a.entries[i].Payload = map[string]any{"tampered": true}
			return
		}
	}
	t.Fatalf("tamperEntry: entry %d not found", entryID)
}

func TestConformance(t *testing.T) {
	conformance.Run(t, newLog, newLogWithBus, conformance.WithTamperHook(tamperEntry))
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
	if concrete := a.(*AuditLog); concrete.logger != rl {
		t.Fatal("WithLogger did not install the given logger")
	}
}

type recordingLogger struct{}

func (recordingLogger) Infof(string, ...interface{})  {}
func (recordingLogger) Warnf(string, ...interface{})  {}
func (recordingLogger) Errorf(string, ...interface{}) {}
