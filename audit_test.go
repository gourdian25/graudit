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
		{"missing ChainID", AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}},
		{"missing ActorID", AuditEvent{ChainID: "c1", EntityType: "t", EntityID: "1", Action: "create"}},
		{"missing EntityType", AuditEvent{ChainID: "c1", ActorID: "a", EntityID: "1", Action: "create"}},
		{"missing EntityID", AuditEvent{ChainID: "c1", ActorID: "a", EntityType: "t", Action: "create"}},
		{"missing Action", AuditEvent{ChainID: "c1", ActorID: "a", EntityType: "t", EntityID: "1"}},
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

// TestAuditEvent_Validate_MissingChainID_WrapsBothSentinels confirms an
// empty ChainID dual-wraps ErrInvalidEvent and ErrChainIDRequired together
// (decision #5 in docs/plan/multi-chain-support-plan.md): errors.Is must
// match either sentinel regardless of which method ultimately caught it.
func TestAuditEvent_Validate_MissingChainID_WrapsBothSentinels(t *testing.T) {
	event := AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}
	err := event.Validate()
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("expected err to wrap ErrInvalidEvent, got %v", err)
	}
	if !errors.Is(err, ErrChainIDRequired) {
		t.Fatalf("expected err to also wrap ErrChainIDRequired, got %v", err)
	}
}

func TestAuditEvent_Validate_NonSerializablePayload(t *testing.T) {
	event := AuditEvent{ChainID: "c1", ActorID: "a", EntityType: "t", EntityID: "1", Action: "create", Payload: make(chan int)}
	if err := event.Validate(); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("expected ErrInvalidEvent for unserializable payload, got %v", err)
	}
}

func TestAuditEvent_Validate_OK(t *testing.T) {
	event := AuditEvent{ChainID: "c1", ActorID: "a", EntityType: "t", EntityID: "1", Action: "create", Payload: map[string]any{"x": 1}}
	if err := event.Validate(); err != nil {
		t.Fatalf("expected valid event to pass, got %v", err)
	}
}

func TestAuditEvent_Validate_NilPayloadOK(t *testing.T) {
	event := AuditEvent{ChainID: "c1", ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}
	if err := event.Validate(); err != nil {
		t.Fatalf("expected nil payload to be valid, got %v", err)
	}
}

func TestLogger_NopAndOrNop(t *testing.T) {
	nop := NopLogger()
	// Must not panic.
	nop.Debug("x")
	nop.Info("x")
	nop.Warn("x")
	nop.Error("x")

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

func (l *recordingLogger) Debug(msg string, args ...any) { l.infos = append(l.infos, msg) }
func (l *recordingLogger) Info(msg string, args ...any)  { l.infos = append(l.infos, msg) }
func (l *recordingLogger) Warn(msg string, args ...any)  { l.warns = append(l.warns, msg) }
func (l *recordingLogger) Error(msg string, args ...any) { l.errs = append(l.errs, msg) }

func TestSentinelErrors_AreDistinct(t *testing.T) {
	sentinels := []error{ErrClosed, ErrInvalidEvent, ErrEntryNotFound, ErrChainCorrupted, ErrBackendUnavailable, ErrReplicaSetRequired, ErrChainIDRequired}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Fatalf("sentinel errors must be distinct: %v matched %v", a, b)
			}
		}
	}
}
