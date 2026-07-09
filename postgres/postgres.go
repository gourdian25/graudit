// File: postgres/postgres.go

// Package postgres is graudit's primary production-eligible durable
// backend. It uses GORM (tracked to grcache's already-proven version) and
// mirrors GORM conventions already established there (explicit
// TableName(), Config.withDefaults(), sync.Once-guarded Close()).
//
// The chain's single-writer serialization point is a pg_advisory_xact_lock
// held for the duration of the transaction that reads the current tail and
// inserts the new entry — not a SELECT ... FOR UPDATE on the latest row,
// which would need a separate sentinel row to lock in the empty-chain
// (genesis) case. EntryID is explicitly assigned by application code inside
// that locked transaction, never a SERIAL/BIGSERIAL column: a Postgres
// sequence advances even when its transaction rolls back, which would
// silently create an EntryID gap with no corresponding entry — breaking
// the "strictly increasing, no gaps" contract and making a legitimate
// rollback-gap indistinguishable from tampering. See docs/architecture.md.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/gourdian25/graudit"
	"github.com/gourdian25/grevents"
)

// chainLockKey is the constant pg_advisory_xact_lock key for graudit's one
// global chain in v1 — there is no per-tenant/per-chain partitioning yet
// (see docs/architecture.md for the extension note).
const chainLockKey = 892374651

// auditEntry is the GORM model for a single chain entry.
type auditEntry struct {
	EntryID    uint64 `gorm:"primaryKey"` // explicitly assigned inside the locked transaction — never SERIAL/BIGSERIAL, see package doc
	ActorID    string `gorm:"index:idx_graudit_actor;type:varchar(255);not null"`
	EntityType string `gorm:"index:idx_graudit_entity,composite:entity;type:varchar(255);not null"`
	EntityID   string `gorm:"index:idx_graudit_entity,composite:entity;type:varchar(255);not null"`
	Action     string `gorm:"type:varchar(64);not null"`
	Payload    []byte `gorm:"type:jsonb"` // JSON-encoded; decoded via graudit.DecodeStoredPayload before recomputing a hash
	Timestamp  time.Time `gorm:"index:idx_graudit_timestamp;not null"`
	Hash       string    `gorm:"type:char(64);not null;uniqueIndex"`
	PrevHash   string    `gorm:"type:char(64);not null"`
}

func (auditEntry) TableName() string { return "graudit_entries" }

func (e auditEntry) toAuditEvent() (graudit.AuditEvent, error) {
	payload, err := graudit.DecodeStoredPayload(e.Payload)
	if err != nil {
		return graudit.AuditEvent{}, err
	}
	return graudit.AuditEvent{
		ID: graudit.EntryID(e.EntryID), ActorID: e.ActorID, EntityType: e.EntityType, EntityID: e.EntityID,
		Action: e.Action, Payload: payload, Timestamp: e.Timestamp.UTC(), Hash: e.Hash, PrevHash: e.PrevHash,
	}, nil
}

// PostgresConfig configures an AuditLog constructed by NewPostgresAuditLog.
type PostgresConfig struct {
	// DSN is a standard libpq connection string. Required.
	DSN string

	// MaxOpenConns caps the connection pool size. 0 means use database/sql's
	// own default (unlimited).
	MaxOpenConns int

	// MaxIdleConns caps idle connections retained in the pool. 0 means use
	// database/sql's own default (2).
	MaxIdleConns int

	// ConnMaxLifetime bounds how long a pooled connection may be reused
	// before being recycled. 0 means connections are reused indefinitely.
	ConnMaxLifetime time.Duration

	// Logger receives optional diagnostic messages (connection failures,
	// grevents publish failures, shutdown). A nil Logger disables logging.
	Logger graudit.Logger

	// EventBus, if set, receives one TopicAuditRecorded event per
	// successful Record/RecordChange. A nil EventBus (the default) means
	// Record simply doesn't publish — not an error.
	EventBus grevents.Bus
}

// AuditLog is a PostgreSQL-backed implementation of graudit.AuditLog, using
// GORM.
type AuditLog struct {
	db     *gorm.DB
	logger graudit.Logger
	bus    grevents.Bus

	closed    atomic.Bool
	closeOnce sync.Once
}

var _ graudit.AuditLog = (*AuditLog)(nil)

// NewPostgresAuditLog opens a connection per cfg, auto-migrates the schema
// (table graudit_entries), and validates connectivity via Ping before
// returning.
//
// Parameters:
//   - cfg: PostgresConfig — DSN is required
//
// Returns:
//   - graudit.AuditLog: ready to use
//   - error: non-nil if DSN is empty, the connection/Ping fails (wrapping
//     graudit.ErrBackendUnavailable), or AutoMigrate fails
func NewPostgresAuditLog(cfg PostgresConfig) (graudit.AuditLog, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("graudit/postgres: PostgresConfig.DSN is required")
	}
	appLogger := graudit.OrNop(cfg.Logger)

	gormLogger := logger.Default.LogMode(logger.Silent)
	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{Logger: gormLogger})
	if err != nil {
		appLogger.Errorf("graudit/postgres: open failed: %v", err)
		return nil, fmt.Errorf("graudit/postgres: open: %w", graudit.ErrBackendUnavailable)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("graudit/postgres: underlying sql.DB: %w", graudit.ErrBackendUnavailable)
	}
	if cfg.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		appLogger.Errorf("graudit/postgres: ping failed: %v", err)
		return nil, fmt.Errorf("graudit/postgres: ping: %w", graudit.ErrBackendUnavailable)
	}

	if err := db.AutoMigrate(&auditEntry{}); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("graudit/postgres: automigrate: %w", err)
	}

	appLogger.Infof("graudit/postgres: connected")
	return &AuditLog{db: db, logger: appLogger, bus: cfg.EventBus}, nil
}

func (a *AuditLog) Record(ctx context.Context, event graudit.AuditEvent) (graudit.EntryID, error) {
	if a.closed.Load() {
		return 0, graudit.ErrClosed
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

	var recorded graudit.AuditEvent
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Advisory lock scoped to this one transaction; released
		// automatically on commit/rollback. A single constant key: one
		// global chain in v1, no per-tenant sub-chains.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", chainLockKey).Error; err != nil {
			return err
		}

		var last auditEntry
		err := tx.Order("entry_id DESC").Limit(1).Take(&last).Error
		prevHash := graudit.GenesisPrevHash
		nextID := graudit.EntryID(1)
		switch {
		case err == nil:
			prevHash = last.Hash
			nextID = graudit.EntryID(last.EntryID) + 1
		case errors.Is(err, gorm.ErrRecordNotFound):
			// genesis: no rows yet
		default:
			return err
		}

		hash, err := graudit.ComputeHash(nextID, event.ActorID, event.EntityType, event.EntityID, event.Action, event.Payload, event.Timestamp, prevHash)
		if err != nil {
			return err
		}

		row := auditEntry{
			EntryID: uint64(nextID), ActorID: event.ActorID, EntityType: event.EntityType, EntityID: event.EntityID,
			Action: event.Action, Payload: payloadBytes, Timestamp: event.Timestamp, Hash: hash, PrevHash: prevHash,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}

		recorded = event
		recorded.ID, recorded.Hash, recorded.PrevHash = nextID, hash, prevHash
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("graudit/postgres: record: %w", graudit.ErrBackendUnavailable)
	}

	graudit.PublishRecorded(ctx, a.bus, a.logger, recorded)
	return recorded.ID, nil
}

func (a *AuditLog) RecordChange(ctx context.Context, actorID, entityType, entityID string, before, after any) (graudit.EntryID, error) {
	event, err := graudit.BuildChangeEvent(actorID, entityType, entityID, before, after)
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
func (a *AuditLog) Verify(ctx context.Context, from, to graudit.EntryID) (bool, graudit.VerifyResult, error) {
	if a.closed.Load() {
		return false, graudit.VerifyResult{}, graudit.ErrClosed
	}
	if from < 1 {
		from = 1
	}

	var rows []auditEntry
	if err := a.db.WithContext(ctx).Where("entry_id >= ? AND entry_id <= ?", uint64(from), uint64(to)).
		Order("entry_id ASC").Find(&rows).Error; err != nil {
		return false, graudit.VerifyResult{}, fmt.Errorf("graudit/postgres: verify: %w", graudit.ErrBackendUnavailable)
	}

	var prevHash string
	for i, row := range rows {
		expectPrev := graudit.GenesisPrevHash
		if i > 0 {
			expectPrev = prevHash
		}
		if row.PrevHash != expectPrev {
			return false, graudit.VerifyResult{
				Valid: false, BrokenAt: graudit.EntryID(row.EntryID), Expected: expectPrev, Actual: row.PrevHash,
			}, nil
		}

		payload, err := graudit.DecodeStoredPayload(row.Payload)
		if err != nil {
			return false, graudit.VerifyResult{}, fmt.Errorf("graudit/postgres: verify: %w", err)
		}
		recomputed, err := graudit.ComputeHash(graudit.EntryID(row.EntryID), row.ActorID, row.EntityType, row.EntityID, row.Action, payload, row.Timestamp, row.PrevHash)
		if err != nil {
			return false, graudit.VerifyResult{}, fmt.Errorf("graudit/postgres: verify: %w", err)
		}
		if recomputed != row.Hash {
			return false, graudit.VerifyResult{
				Valid: false, BrokenAt: graudit.EntryID(row.EntryID), Expected: recomputed, Actual: row.Hash,
			}, nil
		}

		prevHash = row.Hash
	}
	return true, graudit.VerifyResult{Valid: true}, nil
}

func (a *AuditLog) Query(ctx context.Context, filter graudit.QueryFilter) ([]graudit.AuditEvent, error) {
	if a.closed.Load() {
		return nil, graudit.ErrClosed
	}

	q := a.db.WithContext(ctx).Model(&auditEntry{}).Order("entry_id ASC")
	if filter.ActorID != "" {
		q = q.Where("actor_id = ?", filter.ActorID)
	}
	if filter.EntityType != "" {
		q = q.Where("entity_type = ?", filter.EntityType)
	}
	if filter.EntityID != "" {
		q = q.Where("entity_id = ?", filter.EntityID)
	}
	if !filter.From.IsZero() {
		q = q.Where("timestamp >= ?", filter.From)
	}
	if !filter.To.IsZero() {
		q = q.Where("timestamp <= ?", filter.To)
	}
	if filter.Limit > 0 {
		q = q.Limit(filter.Limit)
	}

	var rows []auditEntry
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("graudit/postgres: query: %w", graudit.ErrBackendUnavailable)
	}

	out := make([]graudit.AuditEvent, 0, len(rows))
	for _, row := range rows {
		event, err := row.toAuditEvent()
		if err != nil {
			return nil, fmt.Errorf("graudit/postgres: query: %w", err)
		}
		out = append(out, event)
	}
	return out, nil
}

func (a *AuditLog) Close() error {
	var err error
	a.closeOnce.Do(func() {
		a.closed.Store(true)
		sqlDB, dbErr := a.db.DB()
		if dbErr != nil {
			err = dbErr
			return
		}
		err = sqlDB.Close()
		a.logger.Infof("graudit/postgres: audit log closed")
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
