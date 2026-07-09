// File: mongo/mongo.go

// Package mongo is graudit's second production-eligible durable backend. It
// uses go.mongodb.org/mongo-driver v1 (the same driver family grcache/mongo
// and gourdiantoken depend on) — the v1 module is upstream-deprecated in
// favor of go.mongodb.org/mongo-driver/v2, but migrating to that would be a
// breaking API rewrite out of scope for a routine dependency choice.
//
// The chain's single-writer serialization point is a multi-document ACID
// transaction (session.WithTransaction) covering a singleton chain-state
// document (graudit_entries_chain_state, {_id: "tail", lastEntryId,
// lastHash}) and the new entry's insert into a separate entries collection
// — kept separate so Query never has to filter out a non-entry document
// shape. This requires the target deployment to be a replica set
// (single-node is sufficient) unconditionally: unlike gourdiantoken's Mongo
// repository, there is no useTransactions bool escape hatch, because
// graudit's correctness (not just consistency-under-load) depends on the
// transaction. NewMongoAuditLog fails fast at construction — wrapping
// graudit.ErrReplicaSetRequired — against a standalone instance, rather
// than silently degrading to non-transactional writes that could corrupt
// the chain. See docs/architecture.md.
//
// Unlike grcache/mongo, there is no TTL index anywhere in this backend —
// audit entries are never expired.
package mongo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	"github.com/gourdian25/graudit"
	"github.com/gourdian25/grevents"
)

const (
	defaultCollection = "graudit_entries"
	chainStateID      = "tail"
)

// entryDocument is the BSON document shape for a single chain entry.
type entryDocument struct {
	EntryID    uint64    `bson:"entryId"`
	ActorID    string    `bson:"actorId"`
	EntityType string    `bson:"entityType"`
	EntityID   string    `bson:"entityId"`
	Action     string    `bson:"action"`
	Payload    []byte    `bson:"payload,omitempty"`
	Timestamp  time.Time `bson:"timestamp"`
	Hash       string    `bson:"hash"`
	PrevHash   string    `bson:"prevHash"`
}

func (d entryDocument) toAuditEvent() (graudit.AuditEvent, error) {
	payload, err := graudit.DecodeStoredPayload(d.Payload)
	if err != nil {
		return graudit.AuditEvent{}, err
	}
	return graudit.AuditEvent{
		ID: graudit.EntryID(d.EntryID), ActorID: d.ActorID, EntityType: d.EntityType, EntityID: d.EntityID,
		Action: d.Action, Payload: payload, Timestamp: d.Timestamp.UTC(), Hash: d.Hash, PrevHash: d.PrevHash,
	}, nil
}

// chainState is the singleton tail document tracking the chain's current
// end, stored in its own collection (<Collection>_chain_state).
type chainState struct {
	ID          string `bson:"_id"`
	LastEntryID uint64 `bson:"lastEntryId"`
	LastHash    string `bson:"lastHash"`
}

// MongoConfig configures an AuditLog constructed by NewMongoAuditLog.
type MongoConfig struct {
	// URI is the MongoDB connection string. Must point at a replica set
	// (single-node is sufficient) — see the package doc. Required.
	URI string

	// Database is the database name to use. Required.
	Database string

	// Collection is the entries collection name. Defaults to
	// "graudit_entries" if empty. The chain-state collection is always
	// named "<Collection>_chain_state".
	Collection string

	// Logger receives optional diagnostic messages. A nil Logger disables
	// logging.
	Logger graudit.Logger

	// EventBus, if set, receives one TopicAuditRecorded event per
	// successful Record/RecordChange. A nil EventBus (the default) means
	// Record simply doesn't publish — not an error.
	EventBus grevents.Bus
}

func (cfg MongoConfig) withDefaults() MongoConfig {
	if cfg.Collection == "" {
		cfg.Collection = defaultCollection
	}
	return cfg
}

// AuditLog is a MongoDB-backed implementation of graudit.AuditLog.
type AuditLog struct {
	client     *mongo.Client
	entries    *mongo.Collection
	chainColl  *mongo.Collection
	logger     graudit.Logger
	bus        grevents.Bus

	closed    atomic.Bool
	closeOnce sync.Once
}

var _ graudit.AuditLog = (*AuditLog)(nil)

// NewMongoAuditLog connects to cfg.URI, validates connectivity with Ping,
// probes that the deployment supports multi-document transactions (failing
// fast, wrapping graudit.ErrReplicaSetRequired, if it does not), ensures
// indexes exist, and returns a ready-to-use AuditLog.
//
// Parameters:
//   - cfg: MongoConfig — URI and Database are required
//
// Returns:
//   - graudit.AuditLog: ready to use
//   - error: non-nil if URI/Database is empty, the connection/Ping fails
//     (wrapping graudit.ErrBackendUnavailable), the deployment is not a
//     replica set (wrapping graudit.ErrReplicaSetRequired), or index
//     creation fails
func NewMongoAuditLog(cfg MongoConfig) (graudit.AuditLog, error) {
	if cfg.URI == "" {
		return nil, fmt.Errorf("graudit/mongo: MongoConfig.URI is required")
	}
	if cfg.Database == "" {
		return nil, fmt.Errorf("graudit/mongo: MongoConfig.Database is required")
	}
	cfg = cfg.withDefaults()
	appLogger := graudit.OrNop(cfg.Logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.URI))
	if err != nil {
		appLogger.Errorf("graudit/mongo: connect failed: %v", err)
		return nil, fmt.Errorf("graudit/mongo: connect: %w", graudit.ErrBackendUnavailable)
	}

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		_ = client.Disconnect(ctx)
		appLogger.Errorf("graudit/mongo: ping failed: %v", err)
		return nil, fmt.Errorf("graudit/mongo: ping: %w", graudit.ErrBackendUnavailable)
	}

	if err := probeTransactionSupport(ctx, client); err != nil {
		_ = client.Disconnect(ctx)
		appLogger.Errorf("graudit/mongo: transaction probe failed: %v", err)
		return nil, fmt.Errorf("graudit/mongo: %w", graudit.ErrReplicaSetRequired)
	}

	db := client.Database(cfg.Database)
	entries := db.Collection(cfg.Collection)
	chainColl := db.Collection(cfg.Collection + "_chain_state")

	if err := ensureIndexes(ctx, entries); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("graudit/mongo: ensure indexes: %w", err)
	}

	appLogger.Infof("graudit/mongo: connected to database %q collection %q", cfg.Database, cfg.Collection)
	return &AuditLog{client: client, entries: entries, chainColl: chainColl, logger: appLogger, bus: cfg.EventBus}, nil
}

// probeTransactionSupport runs a no-op multi-document transaction to
// confirm the deployment supports them — this is documented driver
// behavior for detecting a standalone (non-replica-set) instance, which
// rejects StartTransaction with a clear server-side error.
func probeTransactionSupport(ctx context.Context, client *mongo.Client) error {
	session, err := client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (interface{}, error) {
		return nil, nil
	})
	return err
}

func ensureIndexes(ctx context.Context, entries *mongo.Collection) error {
	models := []mongo.IndexModel{
		{Keys: bson.D{{Key: "entryId", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "actorId", Value: 1}}},
		{Keys: bson.D{{Key: "entityType", Value: 1}, {Key: "entityId", Value: 1}}},
		{Keys: bson.D{{Key: "timestamp", Value: 1}}},
	}
	_, err := entries.Indexes().CreateMany(ctx, models)
	return err
}

// Record implements graudit.AuditLog.Record; see the interface's doc
// comment for the full contract and the package doc comment for the
// transaction-based serialization strategy.
func (a *AuditLog) Record(ctx context.Context, event graudit.AuditEvent) (graudit.EntryID, error) {
	if a.closed.Load() {
		return 0, graudit.ErrClosed
	}
	if err := event.Validate(); err != nil {
		return 0, fmt.Errorf("graudit/mongo: record: %w", err)
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	payloadBytes, err := marshalPayload(event.Payload)
	if err != nil {
		return 0, fmt.Errorf("graudit/mongo: record: %w", err)
	}

	session, err := a.client.StartSession()
	if err != nil {
		return 0, fmt.Errorf("graudit/mongo: record: %w", graudit.ErrBackendUnavailable)
	}
	defer session.EndSession(ctx)

	var recorded graudit.AuditEvent
	// session.WithTransaction already retries internally on
	// TransientTransactionError/UnknownTransactionCommitResult — do not add
	// a second, manual outer retry loop around it; that would be redundant.
	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (interface{}, error) {
		var tail chainState
		tailErr := a.chainColl.FindOne(sc, bson.M{"_id": chainStateID}).Decode(&tail)
		prevHash := graudit.GenesisPrevHash
		nextID := graudit.EntryID(1)
		switch {
		case tailErr == nil:
			prevHash, nextID = tail.LastHash, graudit.EntryID(tail.LastEntryID)+1
		case errors.Is(tailErr, mongo.ErrNoDocuments):
			// genesis: no tail doc yet
		default:
			return nil, tailErr
		}

		hash, err := graudit.ComputeHash(nextID, event.ActorID, event.EntityType, event.EntityID, event.Action, event.Payload, event.Timestamp, prevHash)
		if err != nil {
			return nil, err
		}

		doc := entryDocument{
			EntryID: uint64(nextID), ActorID: event.ActorID, EntityType: event.EntityType, EntityID: event.EntityID,
			Action: event.Action, Payload: payloadBytes, Timestamp: event.Timestamp, Hash: hash, PrevHash: prevHash,
		}
		if _, err := a.entries.InsertOne(sc, doc); err != nil {
			return nil, err
		}

		if _, err := a.chainColl.ReplaceOne(sc, bson.M{"_id": chainStateID},
			chainState{ID: chainStateID, LastEntryID: uint64(nextID), LastHash: hash},
			options.Replace().SetUpsert(true)); err != nil {
			return nil, err
		}

		recorded = event
		recorded.ID, recorded.Hash, recorded.PrevHash = nextID, hash, prevHash
		return nil, nil
	})
	if err != nil {
		return 0, fmt.Errorf("graudit/mongo: record: %w", graudit.ErrBackendUnavailable)
	}

	graudit.PublishRecorded(ctx, a.bus, a.logger, recorded)
	return recorded.ID, nil
}

// RecordChange implements graudit.AuditLog.RecordChange; see the
// interface's doc comment for the full contract.
func (a *AuditLog) RecordChange(ctx context.Context, actorID, entityType, entityID string, before, after any) (graudit.EntryID, error) {
	event, err := graudit.BuildChangeEvent(actorID, entityType, entityID, before, after)
	if err != nil {
		return 0, fmt.Errorf("graudit/mongo: record change: %w", err)
	}
	return a.Record(ctx, event)
}

// Verify applies the same two-check design as graudit/postgres: Check A
// recomputes each entry's hash from its own stored fields and compares
// against its stored Hash; Check B asserts each entry's stored PrevHash
// equals the immediately preceding entry's stored Hash.
func (a *AuditLog) Verify(ctx context.Context, from, to graudit.EntryID) (bool, graudit.VerifyResult, error) {
	if a.closed.Load() {
		return false, graudit.VerifyResult{}, graudit.ErrClosed
	}
	if from < 1 {
		from = 1
	}

	cursor, err := a.entries.Find(ctx,
		bson.M{"entryId": bson.M{"$gte": uint64(from), "$lte": uint64(to)}},
		options.Find().SetSort(bson.D{{Key: "entryId", Value: 1}}))
	if err != nil {
		return false, graudit.VerifyResult{}, fmt.Errorf("graudit/mongo: verify: %w", graudit.ErrBackendUnavailable)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var docs []entryDocument
	if err := cursor.All(ctx, &docs); err != nil {
		return false, graudit.VerifyResult{}, fmt.Errorf("graudit/mongo: verify: %w", graudit.ErrBackendUnavailable)
	}

	var prevHash string
	for i, doc := range docs {
		expectPrev := graudit.GenesisPrevHash
		if i > 0 {
			expectPrev = prevHash
		}
		if doc.PrevHash != expectPrev {
			return false, graudit.VerifyResult{
				Valid: false, BrokenAt: graudit.EntryID(doc.EntryID), Expected: expectPrev, Actual: doc.PrevHash,
			}, nil
		}

		payload, err := graudit.DecodeStoredPayload(doc.Payload)
		if err != nil {
			return false, graudit.VerifyResult{}, fmt.Errorf("graudit/mongo: verify: %w", err)
		}
		recomputed, err := graudit.ComputeHash(graudit.EntryID(doc.EntryID), doc.ActorID, doc.EntityType, doc.EntityID, doc.Action, payload, doc.Timestamp, doc.PrevHash)
		if err != nil {
			return false, graudit.VerifyResult{}, fmt.Errorf("graudit/mongo: verify: %w", err)
		}
		if recomputed != doc.Hash {
			return false, graudit.VerifyResult{
				Valid: false, BrokenAt: graudit.EntryID(doc.EntryID), Expected: recomputed, Actual: doc.Hash,
			}, nil
		}

		prevHash = doc.Hash
	}
	return true, graudit.VerifyResult{Valid: true}, nil
}

// Query implements graudit.AuditLog.Query; see the interface's doc comment
// for the full contract.
func (a *AuditLog) Query(ctx context.Context, filter graudit.QueryFilter) ([]graudit.AuditEvent, error) {
	if a.closed.Load() {
		return nil, graudit.ErrClosed
	}

	q := bson.M{}
	if filter.ActorID != "" {
		q["actorId"] = filter.ActorID
	}
	if filter.EntityType != "" {
		q["entityType"] = filter.EntityType
	}
	if filter.EntityID != "" {
		q["entityId"] = filter.EntityID
	}
	if !filter.From.IsZero() || !filter.To.IsZero() {
		ts := bson.M{}
		if !filter.From.IsZero() {
			ts["$gte"] = filter.From
		}
		if !filter.To.IsZero() {
			ts["$lte"] = filter.To
		}
		q["timestamp"] = ts
	}

	opts := options.Find().SetSort(bson.D{{Key: "entryId", Value: 1}})
	if filter.Limit > 0 {
		opts.SetLimit(int64(filter.Limit))
	}

	cursor, err := a.entries.Find(ctx, q, opts)
	if err != nil {
		return nil, fmt.Errorf("graudit/mongo: query: %w", graudit.ErrBackendUnavailable)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var docs []entryDocument
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("graudit/mongo: query: %w", graudit.ErrBackendUnavailable)
	}

	out := make([]graudit.AuditEvent, 0, len(docs))
	for _, doc := range docs {
		event, err := doc.toAuditEvent()
		if err != nil {
			return nil, fmt.Errorf("graudit/mongo: query: %w", err)
		}
		out = append(out, event)
	}
	return out, nil
}

// Close implements graudit.AuditLog.Close; idempotent via sync.Once.
func (a *AuditLog) Close() error {
	var err error
	a.closeOnce.Do(func() {
		a.closed.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = a.client.Disconnect(ctx)
		a.logger.Infof("graudit/mongo: audit log closed")
	})
	return err
}

func marshalPayload(payload any) ([]byte, error) {
	if payload == nil {
		return nil, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: payload is not JSON-serializable: %v", graudit.ErrInvalidEvent, err)
	}
	return raw, nil
}
