// File: mongo_test.go

package graudit

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// mongoTestURI must point at a replica set (single-node is sufficient) —
// see the package doc comment: NewMongoAuditLog fails fast against a
// standalone instance. This is the workspace-wide standardized,
// authenticated single-node replica-set container (root/mongo_password on
// 27018) also used by grcache/gourdiantoken, with a distinct database name
// (graudit_test) so all repos' tests can run against the same local
// instance without colliding. directConnection=true is required because
// the single-node replica set member is registered under its own
// container-internal hostname, which only resolves inside Docker's
// network; without it, the driver's topology discovery gets stuck trying
// to reach that unresolvable hostname.
const mongoTestURI = "mongodb://root:mongo_password@localhost:27018/?directConnection=true"

const mongoTestDatabase = "graudit_test"

// dropMongoTestDB clears the test database so the next NewMongoAuditLog
// call sees an empty chain. newMongoLog and newMongoLogWithBus call this on
// *every* invocation, not just once at the top of a test — the mongo
// backend reconnects to the same persistent database on every call unless
// explicitly cleared first (unlike the memory backend, which is fresh by
// construction), and later contract scenarios would otherwise see
// documents left behind by earlier ones and fail with ID/hash mismatches
// that look like corruption but are actually just test-isolation bugs.
// Errors are ignored (surfaced properly by the subsequent
// NewMongoAuditLog call's own connect/ping instead) so this is safe to
// call even when Mongo isn't reachable at all.
func dropMongoTestDB() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := mongodriver.Connect(ctx, options.Client().ApplyURI(mongoTestURI))
	if err != nil {
		return
	}
	defer client.Disconnect(ctx)
	_ = client.Database(mongoTestDatabase).Drop(ctx)
}

func newMongoLog() (AuditLog, error) {
	dropMongoTestDB()
	return NewMongoAuditLog(MongoConfig{URI: mongoTestURI, Database: mongoTestDatabase})
}

func newMongoLogWithBus(bus *stubBus) (AuditLog, error) {
	dropMongoTestDB()
	return NewMongoAuditLog(MongoConfig{URI: mongoTestURI, Database: mongoTestDatabase, EventBus: bus})
}

// tamperMongoEntry bypasses the AuditLog interface entirely via a raw
// driver update against the same test database, simulating an attacker or
// a bug elsewhere touching the collection directly.
func tamperMongoEntry(t *testing.T, log AuditLog, entryID EntryID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := mongodriver.Connect(ctx, options.Client().ApplyURI(mongoTestURI))
	if err != nil {
		t.Fatalf("connect for tamper: %v", err)
	}
	defer client.Disconnect(ctx)

	coll := client.Database(mongoTestDatabase).Collection("graudit_entries")
	res, err := coll.UpdateOne(ctx, bson.M{"entryId": uint64(entryID)}, bson.M{"$set": bson.M{"payload": []byte(`{"tampered":true}`)}})
	if err != nil {
		t.Fatalf("tamper UpdateOne: %v", err)
	}
	if res.ModifiedCount != 1 {
		t.Fatalf("tamper UpdateOne modified %d docs, want 1", res.ModifiedCount)
	}
}

func TestNewMongoAuditLog_MissingURI(t *testing.T) {
	if _, err := NewMongoAuditLog(MongoConfig{Database: "x"}); err == nil {
		t.Fatal("expected an error for a missing URI, got nil")
	}
}

func TestNewMongoAuditLog_MissingDatabase(t *testing.T) {
	if _, err := NewMongoAuditLog(MongoConfig{URI: mongoTestURI}); err == nil {
		t.Fatal("expected an error for a missing Database, got nil")
	}
}

// mongoStandaloneURI must point at a plain, non-replica-set MongoDB
// instance — deliberately distinct from mongoTestURI, and deliberately
// no-auth (a second, separate standalone-only container reserved for this
// test — see CLAUDE.md). A pre-release audit of this exact test (see git
// history) found a real bug this way: probeTransactionSupport's original
// no-op transaction body never sent an actual transaction-start command to
// the server, so it silently "succeeded" even against a standalone
// deployment — MongoDB only rejects a transaction with "Transaction
// numbers are only allowed on a replica set member or mongos" once a real
// operation is attempted inside one, not on
// StartTransaction/CommitTransaction alone. Do not re-skip this test; it is
// the only thing that would have caught that bug.
const mongoStandaloneURI = "mongodb://localhost:27019/?directConnection=true"

func TestNewMongoAuditLog_RequiresReplicaSet(t *testing.T) {
	log, err := NewMongoAuditLog(MongoConfig{URI: mongoStandaloneURI, Database: "graudit_standalone_test"})
	if err == nil {
		_ = log.Close()
		t.Fatal("expected NewMongoAuditLog to fail against a standalone (non-replica-set) instance, got nil error")
	}
	if !errors.Is(err, ErrReplicaSetRequired) {
		t.Fatalf("error = %v, want wrapping ErrReplicaSetRequired", err)
	}
}

func TestNewMongoAuditLog_CustomCollectionAndLogger(t *testing.T) {
	if probe, err := newMongoLog(); err != nil {
		t.Skipf("MongoDB not available, skipping: %v", err)
	} else {
		_ = probe.Close()
	}
	log, err := NewMongoAuditLog(MongoConfig{
		URI: mongoTestURI, Database: mongoTestDatabase, Collection: "custom_entries", Logger: &recordingLogger{},
	})
	if err != nil {
		t.Fatalf("NewMongoAuditLog with custom collection: %v", err)
	}
	defer log.Close()

	if _, err := log.Record(context.Background(), AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
		t.Fatalf("Record against custom collection: %v", err)
	}
}

func TestMongoAuditLog_RecordChange_InvalidPayload(t *testing.T) {
	log, err := newMongoLog()
	if err != nil {
		t.Skipf("MongoDB not available, skipping: %v", err)
	}
	defer log.Close()

	if _, err := log.RecordChange(context.Background(), "actor:1", "widget", "w1", make(chan int), nil); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("RecordChange with an unmarshalable before value: err=%v, want ErrInvalidEvent", err)
	}
}

func TestMongoAuditLog_Query_InvalidStoredPayload(t *testing.T) {
	log, err := newMongoLog()
	if err != nil {
		t.Skipf("MongoDB not available, skipping: %v", err)
	}
	defer log.Close()

	ctx := context.Background()
	if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	client, err := mongodriver.Connect(ctx, options.Client().ApplyURI(mongoTestURI))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Disconnect(ctx)
	coll := client.Database(mongoTestDatabase).Collection("graudit_entries")
	if _, err := coll.UpdateOne(ctx, bson.M{"entryId": uint64(1)}, bson.M{"$set": bson.M{"payload": []byte(`not-valid-json`)}}); err != nil {
		t.Fatalf("corrupt payload: %v", err)
	}

	if _, err := log.Query(ctx, QueryFilter{}); err == nil {
		t.Fatal("expected Query to surface a decode error for a corrupted payload, got nil")
	}
	if _, _, err := log.Verify(ctx, 1, 1); err == nil {
		t.Fatal("expected Verify to surface a decode error for a corrupted payload, got nil")
	}
}
