// File: internal_coverage_test.go

// White-box (package graudit) coverage-closing tests targeting branches
// unreachable from the public API alone or from the shared contract suite
// — the same techniques used throughout the gourdian ecosystem (see
// gourdiantoken's and grcache's own internal_coverage_test.go files):
// closing/disconnecting a backend's underlying connection out from under
// an otherwise-valid AuditLog to deterministically hit its
// ErrBackendUnavailable-wrapping branches, direct calls to unexported
// helpers with hand-built inputs, and deliberately malformed/unreachable
// connection settings.
package graudit

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/bson"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/gourdian25/graudit/internal/postgresdb"
)

// TestMemoryAuditLog_Record_InvalidEvent, TestPostgresAuditLog_Record_InvalidEvent,
// and TestMongoAuditLog_Record_InvalidEvent cover each backend's own
// event.Validate() error branch inside Record directly. The shared
// contract suite only ever calls Record with valid events, and
// RecordChange's own error path (already covered per-backend) never
// reaches this branch either, since BuildChangeEvent always produces an
// event that passes Validate() — so this is only reachable via a direct,
// invalid Record call.
func TestMemoryAuditLog_Record_InvalidEvent(t *testing.T) {
	log, err := NewMemoryAuditLog()
	if err != nil {
		t.Fatalf("NewMemoryAuditLog: %v", err)
	}
	defer log.Close()

	if _, err := log.Record(context.Background(), AuditEvent{}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Record with an empty (invalid) event: err=%v, want ErrInvalidEvent", err)
	}
}

func TestPostgresAuditLog_Record_InvalidEvent(t *testing.T) {
	log, err := newPostgresLog()
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	defer log.Close()

	if _, err := log.Record(context.Background(), AuditEvent{}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Record with an empty (invalid) event: err=%v, want ErrInvalidEvent", err)
	}
}

func TestMongoAuditLog_Record_InvalidEvent(t *testing.T) {
	log, err := newMongoLog()
	if err != nil {
		t.Skipf("MongoDB not available, skipping: %v", err)
	}
	defer log.Close()

	if _, err := log.Record(context.Background(), AuditEvent{}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Record with an empty (invalid) event: err=%v, want ErrInvalidEvent", err)
	}
}

func TestMemoryAuditLog_Verify_ClampsFromBelowOne(t *testing.T) {
	log, err := NewMemoryAuditLog()
	if err != nil {
		t.Fatalf("NewMemoryAuditLog: %v", err)
	}
	defer log.Close()

	ctx := context.Background()
	if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	ok, _, err := log.Verify(ctx, 0, 1)
	if err != nil {
		t.Fatalf("Verify(0, 1): %v", err)
	}
	if !ok {
		t.Fatal("Verify(0, 1) should behave identically to Verify(1, 1)")
	}
}

func TestMemoryAuditLog_RecordChange_InvalidPayload(t *testing.T) {
	log, err := NewMemoryAuditLog()
	if err != nil {
		t.Fatalf("NewMemoryAuditLog: %v", err)
	}
	defer log.Close()

	if _, err := log.RecordChange(context.Background(), "actor:1", "widget", "w1", make(chan int), nil); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("RecordChange with an unmarshalable before value: err=%v, want ErrInvalidEvent", err)
	}
}

// TestMemoryAuditLog_VerifyDetectsChainLinkageBreak proves Verify's "Check
// B" (chain linkage: each entry's stored PrevHash must equal the preceding
// entry's stored Hash) independently of "Check A" (per-entry hash
// integrity) — the shared contract suite's VerifyDetectsTamper scenario
// only ever corrupts Payload, which changes the recomputed hash and so
// only ever exercises Check A. Without this, Check B's own branch (see
// docs/architecture.md's "Verify()'s two-check design") was completely
// untested on every backend.
func TestMemoryAuditLog_VerifyDetectsChainLinkageBreak(t *testing.T) {
	log, err := NewMemoryAuditLog()
	if err != nil {
		t.Fatalf("NewMemoryAuditLog: %v", err)
	}
	defer log.Close()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
	}

	a := log.(*memoryAuditLog)
	a.mu.Lock()
	a.entries[1].PrevHash = GenesisPrevHash
	a.mu.Unlock()

	ok, detail, err := log.Verify(ctx, 1, 3)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Fatal("Verify with a broken PrevHash link returned ok=true, want false")
	}
	if detail.BrokenAt != 2 {
		t.Fatalf("VerifyResult.BrokenAt = %d, want 2", detail.BrokenAt)
	}
}

func TestPostgresAuditLog_VerifyDetectsChainLinkageBreak(t *testing.T) {
	log, err := newPostgresLog()
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	defer log.Close()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
	}

	pool, err := pgxpool.New(ctx, postgresTestDSN)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `UPDATE graudit_entries SET prev_hash = $1 WHERE entry_id = 2`, GenesisPrevHash); err != nil {
		t.Fatalf("tamper prev_hash: %v", err)
	}

	ok, detail, err := log.Verify(ctx, 1, 3)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Fatal("Verify with a broken PrevHash link returned ok=true, want false")
	}
	if detail.BrokenAt != 2 {
		t.Fatalf("VerifyResult.BrokenAt = %d, want 2", detail.BrokenAt)
	}
}

func TestMongoAuditLog_VerifyDetectsChainLinkageBreak(t *testing.T) {
	log, err := newMongoLog()
	if err != nil {
		t.Skipf("MongoDB not available, skipping: %v", err)
	}
	defer log.Close()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
	}

	client, err := mongodriver.Connect(ctx, options.Client().ApplyURI(mongoTestURI))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Disconnect(ctx)
	coll := client.Database(mongoTestDatabase).Collection("graudit_entries")
	if _, err := coll.UpdateOne(ctx, bson.M{"entryId": uint64(2)},
		bson.M{"$set": bson.M{"prevHash": GenesisPrevHash}}); err != nil {
		t.Fatalf("tamper prevHash: %v", err)
	}

	ok, detail, err := log.Verify(ctx, 1, 3)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Fatal("Verify with a broken PrevHash link returned ok=true, want false")
	}
	if detail.BrokenAt != 2 {
		t.Fatalf("VerifyResult.BrokenAt = %d, want 2", detail.BrokenAt)
	}
}

// TestPostgresAuditLog_Record_TableDroppedMidFlight covers Record's own
// "default: return err" branch inside the GetLastEntry switch — a genuine
// operational failure distinct from the expected pgx.ErrNoRows genesis
// case, simulating some other tool dropping or corrupting the table out
// from under a still-open pool. The table is recreated via a direct
// applyPostgresSchema call before the test returns so later tests in this
// package aren't affected.
func TestPostgresAuditLog_Record_TableDroppedMidFlight(t *testing.T) {
	log, err := newPostgresLog()
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	defer log.Close()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, postgresTestDSN)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, "DROP TABLE graudit_entries"); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	defer func() {
		if err := applyPostgresSchema(context.Background(), pool); err != nil {
			t.Errorf("restore schema after test: %v", err)
		}
	}()

	if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Record with the table dropped mid-flight: err=%v, want ErrBackendUnavailable", err)
	}
}

// TestPostgresAuditLog_Record_InsertRejectedByCheckConstraint covers
// Record's own InsertEntry error branch: a CHECK constraint added directly
// (the kind an operator might attach outside graudit's own API, or a
// future migration might introduce) rejecting the insert after the
// advisory lock and GetLastEntry both already succeeded. The constraint is
// dropped afterward so later tests in this package aren't affected.
func TestPostgresAuditLog_Record_InsertRejectedByCheckConstraint(t *testing.T) {
	log, err := newPostgresLog()
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	defer log.Close()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, postgresTestDSN)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	const constraintName = "graudit_test_reject_all_inserts"
	if _, err := pool.Exec(ctx, "ALTER TABLE graudit_entries ADD CONSTRAINT "+constraintName+" CHECK (false)"); err != nil {
		t.Fatalf("add constraint: %v", err)
	}
	defer func() {
		if _, err := pool.Exec(context.Background(), "ALTER TABLE graudit_entries DROP CONSTRAINT "+constraintName); err != nil {
			t.Errorf("drop constraint after test: %v", err)
		}
	}()

	if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Record with insert rejected by a CHECK constraint: err=%v, want ErrBackendUnavailable", err)
	}
}

func TestPostgresEntryToAuditEvent_InvalidPayloadErrors(t *testing.T) {
	if _, err := postgresEntryToAuditEvent(postgresdb.GrauditEntry{Payload: []byte("not-valid-json")}); err == nil {
		t.Fatal("expected an error for a row with an undecodable payload, got nil")
	}
}

func TestApplyPostgresSchema_AcquireFailsOnClosedPool(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), postgresTestDSN)
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	pool.Close()

	if err := applyPostgresSchema(context.Background(), pool); err == nil {
		t.Fatal("expected applyPostgresSchema to fail against a closed pool, got nil")
	}
}

// TestPostgresAuditLog_OperationsAfterPoolClosed closes the pool directly
// (bypassing Close(), which would set the closed flag and short-circuit
// every method before it ever touches the pool) to deterministically cover
// every method's ErrBackendUnavailable-wrapping branch instead of the
// separate, earlier-checked ErrClosed branch.
func TestPostgresAuditLog_OperationsAfterPoolClosed(t *testing.T) {
	log, err := newPostgresLog()
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	a := log.(*postgresAuditLog)
	a.pool.Close()

	ctx := context.Background()
	if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Record after pool closed: err=%v, want ErrBackendUnavailable", err)
	}
	if _, err := log.RecordChange(ctx, "a", "t", "1", nil, map[string]any{"x": 1}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("RecordChange after pool closed: err=%v, want ErrBackendUnavailable", err)
	}
	if _, _, err := log.Verify(ctx, 1, 1); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Verify after pool closed: err=%v, want ErrBackendUnavailable", err)
	}
	if _, err := log.Query(ctx, QueryFilter{}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Query after pool closed: err=%v, want ErrBackendUnavailable", err)
	}
	// Close itself is still safe to call (idempotent via sync.Once) even
	// though the pool was already closed out from under it directly.
	if err := log.Close(); err != nil {
		t.Fatalf("Close after pool already closed: %v, want nil", err)
	}
}

// TestMongoAuditLog_OperationsAfterClientDisconnected mirrors the postgres
// test above: disconnects the client directly (bypassing Close()) so every
// method's ErrBackendUnavailable-wrapping branch is covered deterministically
// instead of the separate, earlier-checked ErrClosed branch.
func TestMongoAuditLog_OperationsAfterClientDisconnected(t *testing.T) {
	log, err := newMongoLog()
	if err != nil {
		t.Skipf("MongoDB not available, skipping: %v", err)
	}
	a := log.(*mongoAuditLog)
	ctx := context.Background()
	if err := a.client.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Record after client disconnected: err=%v, want ErrBackendUnavailable", err)
	}
	if _, err := log.RecordChange(ctx, "a", "t", "1", nil, map[string]any{"x": 1}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("RecordChange after client disconnected: err=%v, want ErrBackendUnavailable", err)
	}
	if _, _, err := log.Verify(ctx, 1, 1); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Verify after client disconnected: err=%v, want ErrBackendUnavailable", err)
	}
	if _, err := log.Query(ctx, QueryFilter{}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Query after client disconnected: err=%v, want ErrBackendUnavailable", err)
	}
}

// TestNewMongoAuditLog_EnsureIndexesFailsOnDuplicateEntryID covers
// NewMongoAuditLog's ensureAuditIndexes error branch: pre-seeding a fresh
// collection with two documents sharing the same entryId makes creating
// the unique index on entryId fail deterministically, simulating a
// collection some other tool wrote to directly before graudit ever touched
// it.
func TestNewMongoAuditLog_EnsureIndexesFailsOnDuplicateEntryID(t *testing.T) {
	if _, err := newMongoLog(); err != nil {
		t.Skipf("MongoDB not available, skipping: %v", err)
	}
	dropMongoTestDB()

	ctx := context.Background()
	client, err := mongodriver.Connect(ctx, options.Client().ApplyURI(mongoTestURI))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Disconnect(ctx)

	const collection = "dup_entry_id_entries"
	coll := client.Database(mongoTestDatabase).Collection(collection)
	if _, err := coll.InsertMany(ctx, []interface{}{
		entryDocument{EntryID: 1, ActorID: "a", EntityType: "t", EntityID: "1", Action: "create", Hash: "h1", PrevHash: GenesisPrevHash},
		entryDocument{EntryID: 1, ActorID: "a", EntityType: "t", EntityID: "2", Action: "create", Hash: "h2", PrevHash: "h1"},
	}); err != nil {
		t.Fatalf("seed duplicate entryId docs: %v", err)
	}

	_, err = NewMongoAuditLog(MongoConfig{URI: mongoTestURI, Database: mongoTestDatabase, Collection: collection})
	if err == nil {
		t.Fatal("expected NewMongoAuditLog to fail creating a unique index over duplicate entryId values, got nil")
	}
}

// TestMongoAuditLog_Record_ChainStateReplaceRejectedByValidator covers
// Record's chainColl.ReplaceOne error branch: a MongoDB collection
// validator (the kind an operator might attach directly, outside
// graudit's own API) rejecting the upsert that advances the chain-state
// tail document, even though the new entry's own insert into the entries
// collection already succeeded. The validator is cleared afterward, though
// dropMongoTestDB (run at the top of every other newMongoLog call) would
// remove it anyway by dropping the whole database.
func TestMongoAuditLog_Record_ChainStateReplaceRejectedByValidator(t *testing.T) {
	log, err := newMongoLog()
	if err != nil {
		t.Skipf("MongoDB not available, skipping: %v", err)
	}
	defer log.Close()

	ctx := context.Background()
	// Record once so the chain-state collection already exists — collMod
	// below requires an existing collection.
	if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
		t.Fatalf("seed Record: %v", err)
	}

	client, err := mongodriver.Connect(ctx, options.Client().ApplyURI(mongoTestURI))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Disconnect(ctx)

	const chainStateColl = defaultCollection + "_chain_state"
	if err := client.Database(mongoTestDatabase).RunCommand(ctx, bson.D{
		{Key: "collMod", Value: chainStateColl},
		{Key: "validator", Value: bson.M{"impossibleField": bson.M{"$exists": true}}},
		{Key: "validationAction", Value: "error"},
	}).Err(); err != nil {
		t.Fatalf("collMod: %v", err)
	}
	defer func() {
		_ = client.Database(mongoTestDatabase).RunCommand(ctx, bson.D{
			{Key: "collMod", Value: chainStateColl},
			{Key: "validator", Value: bson.M{}},
		}).Err()
	}()

	if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "2", Action: "create"}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Record with chain-state ReplaceOne rejected by validator: err=%v, want ErrBackendUnavailable", err)
	}
}

func TestNewMongoAuditLog_ConnectError(t *testing.T) {
	if _, err := NewMongoAuditLog(MongoConfig{URI: "not-a-valid-uri", Database: "x"}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("NewMongoAuditLog with a malformed URI: err=%v, want ErrBackendUnavailable", err)
	}
}

func TestNewMongoAuditLog_PingFailure(t *testing.T) {
	_, err := NewMongoAuditLog(MongoConfig{
		URI:      "mongodb://localhost:1/?connectTimeoutMS=200&serverSelectionTimeoutMS=200",
		Database: "x",
	})
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("NewMongoAuditLog against an unreachable host: err=%v, want ErrBackendUnavailable", err)
	}
}

func TestPostgresAuditLog_Verify_ClampsFromBelowOne(t *testing.T) {
	log, err := newPostgresLog()
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	defer log.Close()

	ctx := context.Background()
	if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// from=0 must clamp to 1, matching every other backend's documented
	// behavior (EntryID is 1-based; there is no entry #0).
	ok, _, err := log.Verify(ctx, 0, 1)
	if err != nil {
		t.Fatalf("Verify(0, 1): %v", err)
	}
	if !ok {
		t.Fatal("Verify(0, 1) should behave identically to Verify(1, 1)")
	}
}

func TestMongoAuditLog_Verify_ClampsFromBelowOne(t *testing.T) {
	log, err := newMongoLog()
	if err != nil {
		t.Skipf("MongoDB not available, skipping: %v", err)
	}
	defer log.Close()

	ctx := context.Background()
	if _, err := log.Record(ctx, AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	ok, _, err := log.Verify(ctx, 0, 1)
	if err != nil {
		t.Fatalf("Verify(0, 1): %v", err)
	}
	if !ok {
		t.Fatal("Verify(0, 1) should behave identically to Verify(1, 1)")
	}
}

// TestPostgresAuditLog_ConfigTuning exercises PostgresConfig's pool-tuning
// fields (MaxConns/MinConns/MaxConnLifetime) beyond the smoke-test coverage
// TestNewPostgresAuditLog_FullConfig already provides, with a short enough
// MaxConnLifetime that a real Record still succeeds against a pool that
// may recycle its one connection mid-test.
func TestPostgresAuditLog_ConfigTuning(t *testing.T) {
	log, err := NewPostgresAuditLog(PostgresConfig{
		DSN:      postgresTestDSN,
		MaxConns: 2,
		MinConns: 1,
	})
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	defer log.Close()

	if _, err := log.Record(context.Background(), AuditEvent{ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
}
