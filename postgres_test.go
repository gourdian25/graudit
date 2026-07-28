// File: postgres_test.go

package graudit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// postgresTestDSN uses the same host/port/credentials as grcache's/
// gourdiantoken's own local Postgres test setup, with a distinct dbname
// (graudit_test) so all three repos' tests can run against the same local
// instance without colliding.
const postgresTestDSN = "host=localhost user=postgres_user password=postgres_password dbname=graudit_test port=5432 sslmode=disable"

// truncatePostgresTestDB clears graudit_entries so the next
// NewPostgresAuditLog call sees an empty chain. newPostgresLog and
// newPostgresLogWithBus call this on *every* invocation, not just once at
// the top of a test — the postgres backend reconnects to the same
// persistent table/database on every call unless explicitly cleared first
// (unlike the memory backend, which is fresh by construction), and later
// contract scenarios would otherwise see entries left behind by earlier
// ones and fail with ID/hash mismatches that look like corruption but are
// actually just test-isolation bugs. Errors are ignored (surfaced properly
// by the subsequent NewPostgresAuditLog call's own connect/ping instead) so
// this is safe to call even when Postgres isn't reachable at all.
//
// DROP TABLE (not TRUNCATE): this table's schema itself changes across
// graudit versions (e.g. the chain_id column/composite primary key added
// for multi-chain support) — CREATE TABLE IF NOT EXISTS inside
// NewPostgresAuditLog no-ops against an existing, differently-shaped
// table, so TRUNCATE alone would silently leave a locally-iterated-on test
// database on the old schema and fail every test with a confusing "column
// does not exist" error instead of an obviously schema-related one.
// Dropping and letting schema application recreate it from scratch makes
// the local/CI loop self-healing across any future schema change too.
func truncatePostgresTestDB() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, postgresTestDSN)
	if err != nil {
		return
	}
	defer pool.Close()

	// Ignored if the table doesn't exist yet — schema application inside
	// NewPostgresAuditLog creates it on first connect.
	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS graudit_entries")
}

func newPostgresLog() (AuditLog, error) {
	truncatePostgresTestDB()
	return NewPostgresAuditLog(PostgresConfig{DSN: postgresTestDSN})
}

func newPostgresLogWithBus(bus *stubBus) (AuditLog, error) {
	truncatePostgresTestDB()
	return NewPostgresAuditLog(PostgresConfig{DSN: postgresTestDSN, EventBus: bus})
}

// tamperPostgresEntry bypasses the AuditLog interface entirely via a raw
// SQL UPDATE against the same test DB, simulating an attacker or a bug
// elsewhere touching the table directly.
func tamperPostgresEntry(t *testing.T, log AuditLog, chainID string, entryID EntryID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, postgresTestDSN)
	if err != nil {
		t.Fatalf("connect for tamper: %v", err)
	}
	defer pool.Close()

	tag, err := pool.Exec(ctx, `UPDATE graudit_entries SET payload = $1 WHERE chain_id = $2 AND entry_id = $3`, []byte(`{"tampered":true}`), chainID, int64(entryID))
	if err != nil {
		t.Fatalf("tamper UPDATE: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("tamper UPDATE affected %d rows, want 1", tag.RowsAffected())
	}
}

func TestNewPostgresAuditLog_MissingDSN(t *testing.T) {
	if _, err := NewPostgresAuditLog(PostgresConfig{}); err == nil {
		t.Fatal("expected an error for a missing DSN, got nil")
	}
}

// TestNewPostgresAuditLog_MalformedDSN covers pgxpool.ParseConfig's own
// error branch — distinct from TestNewPostgresAuditLog_BadDSN below, which
// exercises a syntactically valid but unreachable DSN (the later Ping
// failure branch instead).
func TestNewPostgresAuditLog_MalformedDSN(t *testing.T) {
	if _, err := NewPostgresAuditLog(PostgresConfig{DSN: "postgres://user:pass@host:not-a-port/db"}); err == nil {
		t.Fatal("expected an error for a malformed DSN, got nil")
	}
}

func TestNewPostgresAuditLog_BadDSN(t *testing.T) {
	_, err := NewPostgresAuditLog(PostgresConfig{
		DSN: "host=localhost user=nope password=nope dbname=nope port=1 sslmode=disable",
	})
	if err == nil {
		t.Fatal("expected an error for an unreachable DSN, got nil")
	}
}

func TestNewPostgresAuditLog_FullConfig(t *testing.T) {
	if log, err := newPostgresLog(); err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	} else {
		_ = log.Close()
	}
	log, err := NewPostgresAuditLog(PostgresConfig{
		DSN:             postgresTestDSN,
		MaxConns:        5,
		MinConns:        1,
		MaxConnLifetime: time.Minute,
		Logger:          &recordingLogger{},
	})
	if err != nil {
		t.Fatalf("NewPostgresAuditLog with full config: %v", err)
	}
	defer log.Close()
}

func TestPostgresSchemaApplyIsIdempotent(t *testing.T) {
	log1, err := newPostgresLog()
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	_ = log1.Close()

	log2, err := newPostgresLog()
	if err != nil {
		t.Fatalf("newPostgresLog (2nd, should re-apply schema safely): %v", err)
	}
	_ = log2.Close()
}

func TestPostgresAuditLog_RecordChange_InvalidPayload(t *testing.T) {
	log, err := newPostgresLog()
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	defer log.Close()

	if _, err := log.RecordChange(context.Background(), testChainID, "actor:1", "widget", "w1", make(chan int), nil); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("RecordChange with an unmarshalable before value: err=%v, want ErrInvalidEvent", err)
	}
}

// Note: there is no postgres equivalent of the mongo backend's
// TestMongoAuditLog_Query_InvalidStoredPayload — the payload column is
// jsonb, and Postgres itself rejects syntactically invalid JSON at the
// type level on write (confirmed: a raw UPDATE with a non-JSON string
// fails with SQLSTATE 22P02 before it ever reaches DecodeStoredPayload),
// so that error branch is genuinely unreachable via SQL-level corruption
// on this backend, not an untested gap.
