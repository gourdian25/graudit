// File: events.go

package graudit

import (
	"context"

	"github.com/gourdian25/grevents"
)

// TopicAuditRecorded is the grevents topic every backend publishes to after
// a successful Record/RecordChange. Two segments, resource.pastTenseVerb,
// matching the real convention already established by grevents' own
// examples (role.assigned, order.placed, payment.failed, user.signup).
const TopicAuditRecorded = "audit.recorded"

// PublishRecorded builds and publishes a TopicAuditRecorded event for entry
// via bus. It is the single implementation every backend calls once after
// its own durable write commits — never called before the write is
// confirmed durable, and never allowed to fail the caller's Record: bus may
// be nil (no grevents.Bus configured, a silent no-op), and any error
// returned by bus.Publish is only logged via logger, never propagated. The
// durable write is graudit's guarantee; grevents delivery is a best-effort
// side channel on top of it.
//
// Parameters:
//   - ctx: context.Context — passed through to bus.Publish unchanged
//   - bus: grevents.Bus — may be nil
//   - logger: Logger — must be non-nil (backends call graudit.OrNop first)
//   - entry: AuditEvent — the fully-populated entry as durably written
//     (ID, Hash, PrevHash already set)
func PublishRecorded(ctx context.Context, bus grevents.Bus, logger Logger, entry AuditEvent) {
	if bus == nil {
		return
	}
	event := grevents.Event{
		Topic:   TopicAuditRecorded,
		Payload: entry,
		Metadata: map[string]string{
			"actor_id":    entry.ActorID,
			"entity_type": entry.EntityType,
			"entity_id":   entry.EntityID,
		},
	}
	if err := bus.Publish(ctx, event); err != nil {
		logger.Warn("graudit: publish failed", "topic", TopicAuditRecorded, "entry_id", entry.ID, "error", err)
	}
}
