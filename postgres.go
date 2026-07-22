// File: postgres.go

// PostgreSQL backend (postgresAuditLog) for graudit — production-eligible
// and durable. Uses pgx/v5 with sqlc-generated queries (see
// internal/postgresdb), the same pattern gourdiantoken/grnoti/grcache use
// for their own Postgres backends, replacing the GORM-based implementation
// this package originally shipped.
//
// The chain's single-writer serialization point is a pg_advisory_xact_lock
// held for the duration of the transaction that reads the current tail and
// inserts the new entry — not a SELECT ... FOR UPDATE on the latest row,
// which would need a separate sentinel row to lock in the empty-chain
// (genesis) case. EntryID is explicitly assigned by application code inside
// that locked transaction, never a BIGSERIAL column: a Postgres sequence
// advances even when its transaction rolls back, which would silently
// create an EntryID gap with no corresponding entry — breaking the
// "strictly increasing, no gaps" contract and making a legitimate
// rollback-gap indistinguishable from tampering. See docs/architecture.md.
package graudit

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gourdian25/graudit/internal/postgresdb"
	"github.com/gourdian25/grevents"
)

//go:embed internal/postgresdb/schema.sql
var postgresSchemaSQL string

// chainLockKey is the constant pg_advisory_xact_lock key for graudit's one
// global chain in v1 — there is no per-tenant/per-chain partitioning yet
// (see docs/architecture.md for the extension note). Scoped to a single
// transaction (released automatically on commit/rollback) — distinct in
// purpose and lifetime from grauditSchemaLockKey below, which guards schema
// application at connect time, not per-Record serialization.
const chainLockKey int64 = 892374651

// grauditSchemaLockKey is a fixed Postgres advisory-lock key used by
// applyPostgresSchema to serialize schema application across concurrent
// callers — e.g. multiple service replicas racing on first boot against a
// fresh database, where concurrent CREATE TABLE/INDEX IF NOT EXISTS
// statements are not fully race-free in Postgres. Distinct from
// gourdiantoken's (8_314_672_205), grnoti's (7_927_100_419), and grcache's
// (6_442_871_953) own lock keys, and from chainLockKey above, since these
// libraries may share a database.
const grauditSchemaLockKey int64 = 5_198_204_733

// PostgresConfig configures an AuditLog constructed by NewPostgresAuditLog.
type PostgresConfig struct {
	// DSN is a standard libpq/pgx connection string. Required.
	DSN string

	// MaxConns caps the pgxpool connection pool size. 0 means use pgxpool's
	// own default.
	MaxConns int32

	// MinConns is the minimum number of connections pgxpool keeps ready.
	// 0 means use pgxpool's own default.
	MinConns int32

	// MaxConnLifetime bounds how long a pooled connection may be reused
	// before being recycled. 0 means pgxpool's own default (unlimited).
	MaxConnLifetime time.Duration

	// Logger receives optional diagnostic messages (connection failures,
	// grevents publish failures, shutdown). A nil Logger disables logging.
	Logger Logger

	// EventBus, if set, receives one TopicAuditRecorded event per
	// successful Record/RecordChange. A nil EventBus (the default) means
	// Record simply doesn't publish — not an error.
	EventBus grevents.Bus
}

// postgresAuditLog is a PostgreSQL-backed implementation of AuditLog, using
// pgx/v5 and sqlc-generated queries.
type postgresAuditLog struct {
	pool   *pgxpool.Pool
	q      *postgresdb.Queries
	logger Logger
	bus    grevents.Bus

	closed    atomic.Bool
	closeOnce sync.Once
}

var _ AuditLog = (*postgresAuditLog)(nil)

// NewPostgresAuditLog opens a connection pool per cfg, applies the schema
// (table graudit_entries, serialized by a Postgres advisory lock so
// concurrent callers building an audit log against the same fresh database
// don't race on the DDL), and validates connectivity via Ping before
// returning.
//
// Parameters:
//   - cfg: PostgresConfig — DSN is required
//
// Returns:
//   - AuditLog: ready to use
//   - error: non-nil if DSN is empty or malformed, the connection/Ping
//     fails (wrapping ErrBackendUnavailable), or schema application fails
//
// Example:
//
//	log, err := graudit.NewPostgresAuditLog(graudit.PostgresConfig{
//		DSN: "host=localhost user=myuser password=mypass dbname=mydb port=5432 sslmode=disable",
//	})
func NewPostgresAuditLog(cfg PostgresConfig) (AuditLog, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("graudit/postgres: PostgresConfig.DSN is required")
	}
	appLogger := OrNop(cfg.Logger)

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("graudit/postgres: parse dsn: %w", ErrBackendUnavailable)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		poolCfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		appLogger.Errorf("graudit/postgres: open failed: %v", err)
		return nil, fmt.Errorf("graudit/postgres: open: %w", ErrBackendUnavailable)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		appLogger.Errorf("graudit/postgres: ping failed: %v", err)
		return nil, fmt.Errorf("graudit/postgres: ping: %w", ErrBackendUnavailable)
	}

	if err := applyPostgresSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("graudit/postgres: apply schema: %w", err)
	}

	appLogger.Infof("graudit/postgres: connected")
	return &postgresAuditLog{pool: pool, q: postgresdb.New(pool), logger: appLogger, bus: cfg.EventBus}, nil
}

// applyPostgresSchema applies the embedded schema against pool, serialized
// by a Postgres session-level advisory lock (grauditSchemaLockKey) so
// concurrent callers apply it one at a time instead of racing on catalog
// DDL. The lock/exec/unlock sequence runs on a single acquired connection,
// since advisory locks are session-scoped, and is unlocked explicitly
// before the connection is released back to the pool — a released pooled
// connection is reused, not reset, so the lock would otherwise leak onto
// whichever caller acquires that connection next.
func applyPostgresSchema(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", grauditSchemaLockKey); err != nil {
		return fmt.Errorf("acquire schema lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", grauditSchemaLockKey)
	}()

	if _, err := conn.Exec(ctx, postgresSchemaSQL); err != nil {
		return err
	}
	return nil
}

// pgTimestamptz converts t into a non-NULL pgtype.Timestamptz — every
// timestamp graudit/postgres stores (Timestamp on an entry) is required,
// unlike grcache's nullable expires_at, so Valid is always true here.
func pgTimestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// pgEntryID and toPgEntryID convert between EntryID (uint64) and the
// signed int64 the graudit_entries.entry_id column uses — Postgres has no
// unsigned integer type, so the sqlc-generated model stores it as int64.
// EntryID is a strictly-increasing chain position starting at 1 (see its
// own doc comment); realistic values never approach the ~9.2 quintillion
// boundary where this conversion could actually overflow, so both
// directions are safe despite gosec's G115 check being unable to prove
// that statically.
func pgEntryID(id int64) EntryID {
	return EntryID(id) //nolint:gosec // see pgEntryID's doc comment
}

func toPgEntryID(id EntryID) int64 {
	return int64(id) //nolint:gosec // see pgEntryID's doc comment
}

func postgresEntryToAuditEvent(row postgresdb.GrauditEntry) (AuditEvent, error) {
	payload, err := DecodeStoredPayload(row.Payload)
	if err != nil {
		return AuditEvent{}, err
	}
	return AuditEvent{
		ID: pgEntryID(row.EntryID), ActorID: row.ActorID, EntityType: row.EntityType, EntityID: row.EntityID,
		Action: row.Action, Payload: payload, Timestamp: row.Timestamp.Time.UTC(), Hash: row.Hash, PrevHash: row.PrevHash,
	}, nil
}

// Record implements AuditLog.Record; see the interface's doc comment for
// the full contract and the package doc comment for the
// pg_advisory_xact_lock-based serialization strategy.
func (a *postgresAuditLog) Record(ctx context.Context, event AuditEvent) (EntryID, error) {
	if a.closed.Load() {
		return 0, ErrClosed
	}
	if err := event.Validate(); err != nil {
		return 0, fmt.Errorf("graudit/postgres: record: %w", err)
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	payloadBytes, err := marshalPayload(event.Payload)
	if err != nil {
		return 0, fmt.Errorf("graudit/postgres: record: %w", err)
	}

	var recorded AuditEvent
	err = pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
		// Advisory lock scoped to this one transaction; released
		// automatically on commit/rollback. A single constant key: one
		// global chain in v1, no per-tenant sub-chains.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", chainLockKey); err != nil {
			return err
		}

		q := a.q.WithTx(tx)
		prevHash := GenesisPrevHash
		nextID := EntryID(1)
		last, err := q.GetLastEntry(ctx)
		switch {
		case err == nil:
			prevHash = last.Hash
			nextID = pgEntryID(last.EntryID) + 1
		case errors.Is(err, pgx.ErrNoRows):
			// genesis: no rows yet
		default:
			return err
		}

		hash, err := ComputeHash(nextID, event.ActorID, event.EntityType, event.EntityID, event.Action, event.Payload, event.Timestamp, prevHash)
		if err != nil {
			return err
		}

		if err := q.InsertEntry(ctx, postgresdb.InsertEntryParams{
			EntryID: toPgEntryID(nextID), ActorID: event.ActorID, EntityType: event.EntityType, EntityID: event.EntityID,
			Action: event.Action, Payload: payloadBytes, Timestamp: pgTimestamptz(event.Timestamp), Hash: hash, PrevHash: prevHash,
		}); err != nil {
			return err
		}

		recorded = event
		recorded.ID, recorded.Hash, recorded.PrevHash = nextID, hash, prevHash
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("graudit/postgres: record: %w", ErrBackendUnavailable)
	}

	PublishRecorded(ctx, a.bus, a.logger, recorded)
	return recorded.ID, nil
}

// RecordChange implements AuditLog.RecordChange; see the interface's doc
// comment for the full contract.
func (a *postgresAuditLog) RecordChange(ctx context.Context, actorID, entityType, entityID string, before, after any) (EntryID, error) {
	event, err := BuildChangeEvent(actorID, entityType, entityID, before, after)
	if err != nil {
		return 0, fmt.Errorf("graudit/postgres: record change: %w", err)
	}
	return a.Record(ctx, event)
}

// Verify recomputes the hash chain across [from, to] via two checks per
// entry: Check A recomputes each entry's hash from its own stored fields
// and compares against its stored Hash; Check B asserts each entry's stored
// PrevHash equals the immediately preceding entry's stored Hash. Both are
// required — Check A alone would not detect a deleted row or a rewritten
// PrevHash pointing at a tampered predecessor.
func (a *postgresAuditLog) Verify(ctx context.Context, from, to EntryID) (bool, VerifyResult, error) {
	if a.closed.Load() {
		return false, VerifyResult{}, ErrClosed
	}
	if from < 1 {
		from = 1
	}

	rows, err := a.q.ListEntriesInRange(ctx, postgresdb.ListEntriesInRangeParams{FromID: toPgEntryID(from), ToID: toPgEntryID(to)})
	if err != nil {
		return false, VerifyResult{}, fmt.Errorf("graudit/postgres: verify: %w", ErrBackendUnavailable)
	}

	var prevHash string
	for i, row := range rows {
		expectPrev := GenesisPrevHash
		if i > 0 {
			expectPrev = prevHash
		}
		if row.PrevHash != expectPrev {
			return false, VerifyResult{
				Valid: false, BrokenAt: pgEntryID(row.EntryID), Expected: expectPrev, Actual: row.PrevHash,
			}, nil
		}

		payload, err := DecodeStoredPayload(row.Payload)
		if err != nil {
			return false, VerifyResult{}, fmt.Errorf("graudit/postgres: verify: %w", err)
		}
		recomputed, err := ComputeHash(pgEntryID(row.EntryID), row.ActorID, row.EntityType, row.EntityID, row.Action, payload, row.Timestamp.Time, row.PrevHash)
		if err != nil {
			return false, VerifyResult{}, fmt.Errorf("graudit/postgres: verify: %w", err)
		}
		if recomputed != row.Hash {
			return false, VerifyResult{
				Valid: false, BrokenAt: pgEntryID(row.EntryID), Expected: recomputed, Actual: row.Hash,
			}, nil
		}

		prevHash = row.Hash
	}
	return true, VerifyResult{Valid: true}, nil
}

// Query implements AuditLog.Query; see the interface's doc comment for the
// full contract.
func (a *postgresAuditLog) Query(ctx context.Context, filter QueryFilter) ([]AuditEvent, error) {
	if a.closed.Load() {
		return nil, ErrClosed
	}

	var params postgresdb.QueryEntriesParams
	if filter.ActorID != "" {
		params.ActorID = pgtype.Text{String: filter.ActorID, Valid: true}
	}
	if filter.EntityType != "" {
		params.EntityType = pgtype.Text{String: filter.EntityType, Valid: true}
	}
	if filter.EntityID != "" {
		params.EntityID = pgtype.Text{String: filter.EntityID, Valid: true}
	}
	if !filter.From.IsZero() {
		params.FromTs = pgTimestamptz(filter.From)
	}
	if !filter.To.IsZero() {
		params.ToTs = pgTimestamptz(filter.To)
	}
	if filter.Limit > 0 {
		params.Lim = pgtype.Int8{Int64: int64(filter.Limit), Valid: true}
	}

	rows, err := a.q.QueryEntries(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("graudit/postgres: query: %w", ErrBackendUnavailable)
	}

	out := make([]AuditEvent, 0, len(rows))
	for _, row := range rows {
		event, err := postgresEntryToAuditEvent(row)
		if err != nil {
			return nil, fmt.Errorf("graudit/postgres: query: %w", err)
		}
		out = append(out, event)
	}
	return out, nil
}

// Close implements AuditLog.Close; idempotent via sync.Once.
func (a *postgresAuditLog) Close() error {
	a.closeOnce.Do(func() {
		a.closed.Store(true)
		a.pool.Close()
		a.logger.Infof("graudit/postgres: audit log closed")
	})
	return nil
}
