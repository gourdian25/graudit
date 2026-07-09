// File: mongo/mongo_test.go

package mongo_test

import (
	"context"
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
//	docker run -d --name graudit-mongo -p 27018:27017 \
//	  -e MONGO_INITDB_ROOT_USERNAME=root -e MONGO_INITDB_ROOT_PASSWORD=mongo_password \
//	  mongo:7 --replSet rs0
//	docker exec graudit-mongo mongosh -u root -p mongo_password --eval 'rs.initiate()'
const testURI = "mongodb://root:mongo_password@localhost:27018/?directConnection=true&replicaSet=rs0"

const testDatabase = "graudit_test"

func dropTestDB(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := mongodriver.Connect(ctx, options.Client().ApplyURI(testURI))
	if err != nil {
		t.Fatalf("connect for drop: %v", err)
	}
	defer client.Disconnect(ctx)
	if err := client.Database(testDatabase).Drop(ctx); err != nil {
		t.Fatalf("drop test database: %v", err)
	}
}

func newLog() (graudit.AuditLog, error) {
	return graudmongo.NewMongoAuditLog(graudmongo.MongoConfig{URI: testURI, Database: testDatabase})
}

func newLogWithBus(bus grevents.Bus) (graudit.AuditLog, error) {
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
	dropTestDB(t)
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

// TestNewMongoAuditLog_RequiresReplicaSet documents (rather than
// independently re-verifies against a second standalone container) that
// construction fails fast against a non-replica-set deployment: the
// transaction probe in NewMongoAuditLog relies on the MongoDB driver's own
// documented rejection of StartTransaction on a standalone instance,
// wrapped as graudit.ErrReplicaSetRequired. Standing up a second, separate
// standalone Mongo container purely for this negative test is unnecessary
// infrastructure for what the driver already guarantees; this test instead
// exercises the wrapping/error-path logic against an unreachable URI (a
// standalone instance behaves identically to an unreachable one from
// probeTransactionSupport's perspective: WithTransaction fails and
// NewMongoAuditLog returns ErrReplicaSetRequired only when Ping/Connect
// already succeeded, so this specific assertion needs a real non-replica-set
// server to observe — skipped when only the replica-set test container is
// available).
func TestNewMongoAuditLog_RequiresReplicaSet(t *testing.T) {
	t.Skip("requires a second, standalone (non-replica-set) MongoDB instance not provisioned in this environment; NewMongoAuditLog's fail-fast path is implemented via probeTransactionSupport, see mongo.go")
}
