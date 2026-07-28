// File: example/example.go

// Command example is a runnable demonstration of graudit against the
// memory backend (no live services required, so `go run` works with no
// setup — grlog is a lightweight in-process logger, not an external
// service). See the commented block at the bottom for the postgres/mongo
// equivalents, including PostgresConfig.Pool injection.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"

	"github.com/gourdian25/grevents"
	"github.com/gourdian25/grlog"

	"github.com/gourdian25/graudit"
)

func main() {
	// slog.New(grlog.NewSlogHandler(...)) satisfies graudit.Logger with no
	// adapter — see logger_test.go's TestGrlogSatisfiesLoggerInterface for
	// the compile+run proof. Connection failures, grevents publish
	// failures, and shutdown all get logged through it; a nil Logger (the
	// default) means graudit logs nothing.
	logger := slog.New(grlog.NewSlogHandler(grlog.NewDefaultLogger()))

	// A bus closed before use, purely to make the logger visibly fire
	// below: PublishRecorded logs (via Warn) and swallows any publish
	// error rather than failing Record — the durable write is graudit's
	// guarantee, grevents delivery is a best-effort side channel on top of
	// it. A real application would pass a live, running Bus here instead.
	bus, _ := grevents.NewBus()
	_ = bus.Close()

	// One AuditLog instance (one connection pool in a networked backend)
	// serves any number of independent chains — the multi-tenant use case
	// this exists for: a schema-per-tenant SaaS backend uses one ChainID
	// per tenant, plus a separate chain for platform-operator actions
	// (tenant provisioning/suspension) that happen outside any tenant's
	// own schema. See docs/architecture.md's "Multi-chain support" section.
	auditLog, err := graudit.NewMemoryAuditLog(graudit.WithLogger(logger), graudit.WithEventBus(bus))
	if err != nil {
		log.Fatal(err)
	}
	defer auditLog.Close()

	ctx := context.Background()

	const (
		tenantChainID   = "tenant:acme"
		platformChainID = "platform:ops"
	)

	// Record a direct entry into the tenant's chain.
	tenantID1, err := auditLog.Record(ctx, graudit.AuditEvent{
		ChainID:    tenantChainID,
		ActorID:    "user:42",
		EntityType: "invoice",
		EntityID:   "inv_123",
		Action:     "create",
		Payload:    map[string]any{"amount": 100, "currency": "USD"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("recorded tenant entry", tenantID1)

	// RecordChange diffs a before/after pair automatically.
	tenantID2, err := auditLog.RecordChange(ctx, tenantChainID, "user:42", "invoice", "inv_123",
		map[string]any{"amount": 100, "status": "draft"},
		map[string]any{"amount": 100, "status": "sent"},
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("recorded tenant change entry", tenantID2)

	// The platform chain is entirely independent: its own EntryID sequence
	// starts at 1 too, despite sharing this same AuditLog instance and
	// underlying storage — chains never see each other's entries, and
	// ChainID is baked into each entry's Hash so one can't be spliced into
	// the other even with direct database access (see ComputeHash's doc
	// comment).
	platformID1, err := auditLog.Record(ctx, graudit.AuditEvent{
		ChainID:    platformChainID,
		ActorID:    "operator:jane",
		EntityType: "tenant",
		EntityID:   "acme",
		Action:     "provision",
		Payload:    map[string]any{"plan": "enterprise"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("recorded platform entry", platformID1)

	// Verify confirms each chain hasn't been tampered with — scoped
	// independently per chain, never across them.
	tenantOK, tenantDetail, err := auditLog.Verify(ctx, tenantChainID, 1, tenantID2)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("verify tenant chain (before tampering): ok=%v detail=%+v\n", tenantOK, tenantDetail)

	platformOK, platformDetail, err := auditLog.Verify(ctx, platformChainID, 1, platformID1)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("verify platform chain (before tampering): ok=%v detail=%+v\n", platformOK, platformDetail)

	// Query is likewise scoped by ChainID — required on every call, no
	// wildcard/query-all escape hatch, since a cross-tenant leak in an
	// audit trail is worse than the ergonomic cost of always specifying
	// one chain.
	tenantEntries, err := auditLog.Query(ctx, graudit.QueryFilter{ChainID: tenantChainID, EntityType: "invoice", EntityID: "inv_123"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("found %d entries for invoice inv_123 in the tenant chain\n", len(tenantEntries))

	// Postgres and Mongo backends are constructed with a Config instead of
	// options, but implement the exact same graudit.AuditLog interface —
	// both live directly in the graudit package, no subpackage import:
	//
	//	auditLog, err := graudit.NewPostgresAuditLog(graudit.PostgresConfig{
	//		DSN: "host=localhost user=myuser password=mypass dbname=mydb port=5432 sslmode=disable",
	//	})
	//
	//	auditLog, err := graudit.NewMongoAuditLog(graudit.MongoConfig{
	//		URI:      "mongodb://localhost:27017/?replicaSet=rs0",
	//		Database: "myapp",
	//	})
	//
	// At multi-tenant scale, a single process serving hundreds of tenant
	// chains wants one shared pgxpool.Pool, not one AuditLog dialing its
	// own dedicated connections — pass an already-open pool via
	// PostgresConfig.Pool instead of DSN (exactly one of the two is
	// required); graudit never closes a pool it didn't dial itself:
	//
	//	auditLog, err := graudit.NewPostgresAuditLog(graudit.PostgresConfig{
	//		Pool: sharedPool, // *pgxpool.Pool your application already owns
	//	})
	//
	// Wiring in grevents so other consumers can react to recorded entries:
	//
	//	import "github.com/gourdian25/grevents"
	//	bus, _ := grevents.NewBus()
	//	auditLog, err := graudit.NewPostgresAuditLog(graudit.PostgresConfig{
	//		DSN:      dsn,
	//		EventBus: bus,
	//	})
}
