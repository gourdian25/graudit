// File: example/example.go

// Command example is a runnable demonstration of graudit against the
// memory backend (no live services required, so `go run` works with no
// setup — grlog is a lightweight in-process logger, not an external
// service). See the commented block at the bottom for the postgres/mongo
// equivalents.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/gourdian25/grevents"
	"github.com/gourdian25/grlog"

	"github.com/gourdian25/graudit"
	"github.com/gourdian25/graudit/memory"
)

func main() {
	// *grlog.Logger satisfies graudit.Logger with no adapter — see
	// logger_test.go's TestGrlogSatisfiesLoggerInterface for the
	// compile+run proof. Connection failures, grevents publish failures,
	// and shutdown all get logged through it; a nil Logger (the default)
	// means graudit logs nothing.
	logger := grlog.NewDefaultLogger()

	// A bus closed before use, purely to make the logger visibly fire
	// below: PublishRecorded logs (via Warnf) and swallows any publish
	// error rather than failing Record — the durable write is graudit's
	// guarantee, grevents delivery is a best-effort side channel on top of
	// it. A real application would pass a live, running Bus here instead.
	bus, _ := grevents.NewBus()
	_ = bus.Close()

	auditLog, err := memory.NewMemoryAuditLog(memory.WithLogger(logger), memory.WithEventBus(bus))
	if err != nil {
		log.Fatal(err)
	}
	defer auditLog.Close()

	ctx := context.Background()

	// Record a direct entry.
	id1, err := auditLog.Record(ctx, graudit.AuditEvent{
		ActorID:    "user:42",
		EntityType: "invoice",
		EntityID:   "inv_123",
		Action:     "create",
		Payload:    map[string]any{"amount": 100, "currency": "USD"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("recorded entry", id1)

	// RecordChange diffs a before/after pair automatically.
	id2, err := auditLog.RecordChange(ctx, "user:42", "invoice", "inv_123",
		map[string]any{"amount": 100, "status": "draft"},
		map[string]any{"amount": 100, "status": "sent"},
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("recorded change entry", id2)

	// Verify confirms the chain hasn't been tampered with.
	ok, detail, err := auditLog.Verify(ctx, 1, id2)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("verify (before tampering): ok=%v detail=%+v\n", ok, detail)

	// Query entries for this entity.
	entries, err := auditLog.Query(ctx, graudit.QueryFilter{EntityType: "invoice", EntityID: "inv_123"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("found %d entries for invoice inv_123\n", len(entries))

	// Postgres and Mongo backends are constructed with a Config instead of
	// options, but implement the exact same graudit.AuditLog interface:
	//
	//	import "github.com/gourdian25/graudit/postgres"
	//	auditLog, err := postgres.NewPostgresAuditLog(postgres.PostgresConfig{
	//		DSN: "host=localhost user=myuser password=mypass dbname=mydb port=5432 sslmode=disable",
	//	})
	//
	//	import "github.com/gourdian25/graudit/mongostore"
	//	auditLog, err := mongostore.NewMongoAuditLog(mongostore.MongoConfig{
	//		URI:      "mongodb://localhost:27017/?replicaSet=rs0",
	//		Database: "myapp",
	//	})
	//
	// Wiring in grevents so other consumers can react to recorded entries:
	//
	//	import "github.com/gourdian25/grevents"
	//	bus, _ := grevents.NewBus()
	//	auditLog, err := postgres.NewPostgresAuditLog(postgres.PostgresConfig{
	//		DSN:      dsn,
	//		EventBus: bus,
	//	})
}
