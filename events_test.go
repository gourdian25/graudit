// File: events_test.go

package graudit

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gourdian25/grevents"
)

// stubBus is a minimal grevents.Bus test double recording every Publish
// call, used to verify PublishRecorded's topic/payload shape and its
// failure-does-not-propagate contract without depending on a real Bus.
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
func (b *stubBus) Use(mw grevents.Middleware)                       {}
func (b *stubBus) Stats(ctx context.Context) (grevents.Stats, error) { return grevents.Stats{}, nil }
func (b *stubBus) Close() error                                     { return nil }

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

func TestPublishRecorded_NilBusIsNoOp(t *testing.T) {
	// Must not panic with a nil bus.
	PublishRecorded(context.Background(), nil, NopLogger(), AuditEvent{ID: 1})
}

func TestPublishRecorded_PublishesExpectedTopicAndPayload(t *testing.T) {
	bus := &stubBus{}
	entry := AuditEvent{ID: 1, ActorID: "actor:1", EntityType: "widget", EntityID: "w1", Hash: "h"}

	PublishRecorded(context.Background(), bus, NopLogger(), entry)

	if len(bus.published) != 1 {
		t.Fatalf("expected exactly 1 Publish call, got %d", len(bus.published))
	}
	got := bus.published[0]
	if got.Topic != TopicAuditRecorded {
		t.Fatalf("Topic = %q, want %q", got.Topic, TopicAuditRecorded)
	}
	payload, ok := got.Payload.(AuditEvent)
	if !ok {
		t.Fatalf("Payload type = %T, want AuditEvent", got.Payload)
	}
	if payload.ID != entry.ID || payload.Hash != entry.Hash {
		t.Fatalf("Payload = %+v, want %+v", payload, entry)
	}
	if got.Metadata["actor_id"] != "actor:1" || got.Metadata["entity_type"] != "widget" || got.Metadata["entity_id"] != "w1" {
		t.Fatalf("unexpected Metadata: %+v", got.Metadata)
	}
}

func TestPublishRecorded_FailureIsLoggedNotPropagated(t *testing.T) {
	bus := &stubBus{publishErr: errors.New("bus unavailable")}
	logger := &recordingLogger{}

	// PublishRecorded has no return value — the test asserts it doesn't
	// panic and that the failure was logged via Warnf, matching the
	// documented "log, never fail the caller" contract.
	PublishRecorded(context.Background(), bus, logger, AuditEvent{ID: 1})

	if len(logger.warns) != 1 {
		t.Fatalf("expected exactly 1 Warnf call recording the publish failure, got %d", len(logger.warns))
	}
}

func TestTopicAuditRecorded_IsTwoSegments(t *testing.T) {
	// Matches the confirmed real grevents convention (role.assigned,
	// order.placed, payment.failed, user.signup): exactly two
	// dot-separated segments, not a namespaced/three-segment name.
	segments := 1
	for _, c := range TopicAuditRecorded {
		if c == '.' {
			segments++
		}
	}
	if segments != 2 {
		t.Fatalf("TopicAuditRecorded = %q, expected exactly 2 dot-separated segments", TopicAuditRecorded)
	}
}
