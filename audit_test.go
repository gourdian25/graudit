// File: audit_test.go

package graudit

import (
	"errors"
	"testing"
)

func TestAuditEvent_Validate_MissingFields(t *testing.T) {
	cases := []struct {
		name  string
		event AuditEvent
	}{
		{"missing ActorID", AuditEvent{EntityType: "t", EntityID: "1", Action: "create"}},
		{"missing EntityType", AuditEvent{ActorID: "a", EntityID: "1", Action: "create"}},
		{"missing EntityID", AuditEvent{ActorID: "a", EntityType: "t", Action: "create"}},
		{"missing Action", AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.event.Validate()
			if !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("expected ErrInvalidEvent, got %v", err)
			}
		})
	}
}

func TestAuditEvent_Validate_NonSerializablePayload(t *testing.T) {
	event := AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create", Payload: make(chan int)}
	if err := event.Validate(); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("expected ErrInvalidEvent for unserializable payload, got %v", err)
	}
}

func TestAuditEvent_Validate_OK(t *testing.T) {
	event := AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create", Payload: map[string]any{"x": 1}}
	if err := event.Validate(); err != nil {
		t.Fatalf("expected valid event to pass, got %v", err)
	}
}

func TestAuditEvent_Validate_NilPayloadOK(t *testing.T) {
	event := AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}
	if err := event.Validate(); err != nil {
		t.Fatalf("expected nil payload to be valid, got %v", err)
	}
}

func TestLogger_NopAndOrNop(t *testing.T) {
	nop := NopLogger()
	// Must not panic.
	nop.Infof("x")
	nop.Warnf("x")
	nop.Errorf("x")

	if OrNop(nil) == nil {
		t.Fatal("OrNop(nil) must never return nil")
	}

	custom := &recordingLogger{}
	if got := OrNop(custom); got != custom {
		t.Fatal("OrNop must return the given non-nil logger unchanged")
	}
}

type recordingLogger struct {
	infos, warns, errs []string
}

func (l *recordingLogger) Infof(format string, args ...interface{})  { l.infos = append(l.infos, format) }
func (l *recordingLogger) Warnf(format string, args ...interface{})  { l.warns = append(l.warns, format) }
func (l *recordingLogger) Errorf(format string, args ...interface{}) { l.errs = append(l.errs, format) }

func TestSentinelErrors_AreDistinct(t *testing.T) {
	sentinels := []error{ErrClosed, ErrInvalidEvent, ErrEntryNotFound, ErrChainCorrupted, ErrBackendUnavailable, ErrReplicaSetRequired}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Fatalf("sentinel errors must be distinct: %v matched %v", a, b)
			}
		}
	}
}
