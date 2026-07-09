// File: mongo/mongo_test.go

package mongo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	mongodriver "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/gourdian25/graudit"
	"github.com/gourdian25/graudit/conformance"
	graudmongo "github.com/gourdian25/graudit/mongo"
	"github.com/gourdian25/grevents"
)

// testURI must point at a replica set (single-node is sufficient) — see
// the package doc comment: NewMongoAuditLog fails fast against a
// standalone instance. Uses the same host/port as grcache's/gourdiantoken's
// own local Mongo test setup, with a distinct database name (graudit_test)
// so all three repos' tests can run against the same local instance
// without colliding.
//
// Start a matching local container with:
//
//	docker run -d --name graudit-mongo -p 27018:27017 mongo:7 --replSet rs0
//	docker exec graudit-mongo mongosh --eval 'rs.initiate()'
const testURI = "mongodb://localhost:27018/?directConnection=true&replicaSet=rs0"

const testDatabase = "graudit_test"

// drop clears the test database so the next NewMongoAuditLog call sees an
// empty chain. Every conformance scenario calls newLog() expecting a
// fresh, empty AuditLog (true by construction for the memory backend, but
// the mongo backend reconnects to the same persistent database on every
// call unless explicitly cleared first) — this must run before *every*
// newLog()/newLogWithBus() call, not just once at the top of
// TestConformance, or later scenarios see documents left behind by earlier
// ones and fail with ID/hash mismatches that look like corruption but are
// actually just test-isolation bugs.
func drop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := mongodriver.Connect(ctx, options.Client().ApplyURI(testURI))
	if err != nil {
		return // surfaced properly by NewMongoAuditLog's own connect/ping right after
	}
	defer client.Disconnect(ctx)
	_ = client.Database(testDatabase).Drop(ctx)
}

func newLog() (graudit.AuditLog, error) {
	drop()
	return graudmongo.NewMongoAuditLog(graudmongo.MongoConfig{URI: testURI, Database: testDatabase})
}

func newLogWithBus(bus grevents.Bus) (graudit.AuditLog, error) {
	drop()
	return graudmongo.NewMongoAuditLog(graudmongo.MongoConfig{URI: testURI, Database: testDatabase, EventBus: bus})
}

// tamperEntry bypasses the AuditLog interface entirely via a raw driver
// update against the same test database, simulating an attacker or a bug
// elsewhere touching the collection directly.
func tamperEntry(t *testing.T, log graudit.AuditLog, entryID graudit.EntryID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := mongodriver.Connect(ctx, options.Client().ApplyURI(testURI))
	if err != nil {
		t.Fatalf("connect for tamper: %v", err)
	}
	defer client.Disconnect(ctx)

	coll := client.Database(testDatabase).Collection("graudit_entries")
	res, err := coll.UpdateOne(ctx, bson.M{"entryId": uint64(entryID)}, bson.M{"$set": bson.M{"payload": []byte(`{"tampered":true}`)}})
	if err != nil {
		t.Fatalf("tamper UpdateOne: %v", err)
	}
	if res.ModifiedCount != 1 {
		t.Fatalf("tamper UpdateOne modified %d docs, want 1", res.ModifiedCount)
	}
}

func TestConformance(t *testing.T) {
	conformance.Run(t, newLog, newLogWithBus, conformance.WithTamperHook(tamperEntry))
}

func TestNewMongoAuditLog_MissingURI(t *testing.T) {
	if _, err := graudmongo.NewMongoAuditLog(graudmongo.MongoConfig{Database: "x"}); err == nil {
		t.Fatal("expected an error for a missing URI, got nil")
	}
}

func TestNewMongoAuditLog_MissingDatabase(t *testing.T) {
	if _, err := graudmongo.NewMongoAuditLog(graudmongo.MongoConfig{URI: testURI}); err == nil {
		t.Fatal("expected an error for a missing Database, got nil")
	}
}

// standaloneURI must point at a plain, non-replica-set MongoDB instance —
// deliberately distinct from testURI. A pre-release audit of this exact
// test (previously skipped, see git history) found a real bug this way:
// probeTransactionSupport's original no-op transaction body never sent an
// actual transaction-start command to the server, so it silently
// "succeeded" even against a standalone deployment — MongoDB only rejects
// a transaction with "Transaction numbers are only allowed on a replica
// set member or mongos" once a real operation is attempted inside one, not
// on StartTransaction/CommitTransaction alone. Do not re-skip this test;
// it is the only thing that would have caught that bug.
//
//	docker run -d --name graudit-mongo-standalone -p 27019:27017 mongo:7
const standaloneURI = "mongodb://localhost:27019/?directConnection=true"

func TestNewMongoAuditLog_RequiresReplicaSet(t *testing.T) {
	log, err := graudmongo.NewMongoAuditLog(graudmongo.MongoConfig{URI: standaloneURI, Database: "graudit_standalone_test"})
	if err == nil {
		_ = log.Close()
		t.Fatal("expected NewMongoAuditLog to fail against a standalone (non-replica-set) instance, got nil error")
	}
	if !errors.Is(err, graudit.ErrReplicaSetRequired) {
		t.Fatalf("error = %v, want wrapping graudit.ErrReplicaSetRequired", err)
	}
}

func TestNewMongoAuditLog_CustomCollectionAndLogger(t *testing.T) {
	drop()
	log, err := graudmongo.NewMongoAuditLog(graudmongo.MongoConfig{
		URI: testURI, Database: testDatabase, Collection: "custom_entries", Logger: &recordingLogger{},
	})
	if err != nil {
		t.Fatalf("NewMongoAuditLog with custom collection: %v", err)
	}
	defer log.Close()

	if _, err := log.Record(context.Background(), graudit.AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
		t.Fatalf("Record against custom collection: %v", err)
	}
}

func TestRecordChange_InvalidPayload(t *testing.T) {
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	if _, err := log.RecordChange(context.Background(), "actor:1", "widget", "w1", make(chan int), nil); !errors.Is(err, graudit.ErrInvalidEvent) {
		t.Fatalf("RecordChange with an unmarshalable before value: err=%v, want ErrInvalidEvent", err)
	}
}

func TestQuery_InvalidStoredPayload(t *testing.T) {
	log, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer log.Close()

	ctx := context.Background()
	if _, err := log.Record(ctx, graudit.AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	client, err := mongodriver.Connect(ctx, options.Client().ApplyURI(testURI))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Disconnect(ctx)
	coll := client.Database(testDatabase).Collection("graudit_entries")
	if _, err := coll.UpdateOne(ctx, bson.M{"entryId": uint64(1)}, bson.M{"$set": bson.M{"payload": []byte(`not-valid-json`)}}); err != nil {
		t.Fatalf("corrupt payload: %v", err)
	}

	if _, err := log.Query(ctx, graudit.QueryFilter{}); err == nil {
		t.Fatal("expected Query to surface a decode error for a corrupted payload, got nil")
	}
	if _, _, err := log.Verify(ctx, 1, 1); err == nil {
		t.Fatal("expected Verify to surface a decode error for a corrupted payload, got nil")
	}
}

type recordingLogger struct{}

func (*recordingLogger) Infof(string, ...interface{})  {}
func (*recordingLogger) Warnf(string, ...interface{})  {}
func (*recordingLogger) Errorf(string, ...interface{}) {}
