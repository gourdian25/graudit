// File: postgres_test.go

package graudit

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
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
// actually just test-isolation bugs. Pool-creation errors are ignored
// (surfaced properly by the subsequent NewPostgresAuditLog call's own
// connect/ping instead) so this is safe to call even when Postgres isn't
// reachable at all.
//
// DROP TABLE (not TRUNCATE) *then* re-apply the schema, in that exact
// order: this table's schema itself changes across graudit versions (e.g.
// the chain_id column/composite primary key added for multi-chain
// support), so TRUNCATE alone would silently leave a locally-iterated-on
// test database on the old schema and fail every test with a confusing
// "column does not exist" error instead of an obviously schema-related
// one. Dropping and explicitly recreating it via applyPostgresSchema makes
// the local/CI loop self-healing across any future schema change too.
//
// applyPostgresSchema must run *after* the DROP, not before: unlike
// gourdiantoken's own equivalent test helper (which re-applies schema
// before its TRUNCATE statements, since TRUNCATE never drops the table
// itself), graudit's cleanup step here actually drops graudit_entries — so
// applying schema first and dropping second would just recreate the table
// and then immediately discard it again, leaving nothing for
// NewPostgresAuditLog to see (it no longer applies schema itself — see
// postgres.go's PostgresSchemaSQL doc comment) and every subsequent
// Record/Query call failing with "relation does not exist" instead of
// running against a clean table.
func truncatePostgresTestDB() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, postgresTestDSN)
	if err != nil {
		return
	}
	defer pool.Close()

	// Ignored if the table doesn't exist yet — applyPostgresSchema below
	// (re)creates it regardless.
	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS graudit_entries")

	// graudit's own test setup, not a real consumer: applies the schema
	// directly rather than through a migration tool, since this is a
	// throwaway local/CI test database this process fully controls. Error
	// ignored for the same reason as the DROP above — a genuine failure
	// here (e.g. Postgres going away between the two calls) is surfaced by
	// the subsequent NewPostgresAuditLog call's own connect/ping instead.
	_ = applyPostgresSchema(ctx, pool)
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

// TestNewPostgresAuditLog_DSNXorPoolRequired covers connectPostgres's
// validation that exactly one of PostgresConfig.DSN/Pool is set — neither
// (already covered by TestNewPostgresAuditLog_MissingDSN, re-asserted here
// alongside its counterpart for symmetry) and both are equally invalid.
func TestNewPostgresAuditLog_DSNXorPoolRequired(t *testing.T) {
	if _, err := NewPostgresAuditLog(PostgresConfig{}); err == nil {
		t.Fatal("NewPostgresAuditLog(neither DSN nor Pool) = nil error, want non-nil")
	}

	pool, err := pgxpool.New(context.Background(), postgresTestDSN)
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}

	if _, err := NewPostgresAuditLog(PostgresConfig{DSN: postgresTestDSN, Pool: pool}); err == nil {
		t.Fatal("NewPostgresAuditLog(both DSN and Pool set) = nil error, want non-nil")
	}
}

// TestNewPostgresAuditLog_ExternalPoolPingFails covers connectPostgres's
// Pool-branch Ping failure — the Pool-supplied analogue of
// TestNewPostgresAuditLog_BadDSN, which covers the same failure on the
// DSN-dialing path. An already-closed pool fails Ping deterministically,
// with no live-server fault injection required.
func TestNewPostgresAuditLog_ExternalPoolPingFails(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), postgresTestDSN)
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	pool.Close()

	if _, err := NewPostgresAuditLog(PostgresConfig{Pool: pool}); err == nil {
		t.Fatal("NewPostgresAuditLog(Pool: <closed pool>) = nil error, want non-nil")
	}
}

// TestNewPostgresAuditLog_WithExternalPool constructs an AuditLog from a
// caller-supplied *pgxpool.Pool (PostgresConfig.Pool) instead of a DSN, and
// confirms Record/Query work normally against it — the multi-tenant use
// case this exists for: one AuditLog instance sharing a pool the rest of
// the application already owns, rather than dialing its own per tenant.
func TestNewPostgresAuditLog_WithExternalPool(t *testing.T) {
	truncatePostgresTestDB()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, postgresTestDSN)
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}

	log, err := NewPostgresAuditLog(PostgresConfig{Pool: pool})
	if err != nil {
		t.Fatalf("NewPostgresAuditLog(Pool: pool): %v", err)
	}
	defer log.Close()

	id, err := log.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"})
	if err != nil {
		t.Fatalf("Record via external pool: %v", err)
	}
	if id != 1 {
		t.Fatalf("Record via external pool: id = %d, want 1", id)
	}

	events, err := log.Query(ctx, QueryFilter{ChainID: testChainID})
	if err != nil {
		t.Fatalf("Query via external pool: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Query via external pool: got %d events, want 1", len(events))
	}
}

// TestPostgresAuditLog_SharedPool_CloseDoesNotClosePool is the regression
// test for ownsPool: an AuditLog built from an externally-supplied Pool
// must not close that pool on Close() — the pool, and anything else
// sharing it (here, a second AuditLog instance), must remain usable
// afterward. Mirrors grnoti's own
// TestPostgresStores_SharedPool_CloseDoesNotAffectSiblingStore.
func TestPostgresAuditLog_SharedPool_CloseDoesNotClosePool(t *testing.T) {
	truncatePostgresTestDB()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, postgresTestDSN)
	if err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("PostgreSQL not available, skipping: %v", err)
	}

	log1, err := NewPostgresAuditLog(PostgresConfig{Pool: pool})
	if err != nil {
		t.Fatalf("NewPostgresAuditLog(Pool: pool) [1st]: %v", err)
	}
	log2, err := NewPostgresAuditLog(PostgresConfig{Pool: pool})
	if err != nil {
		t.Fatalf("NewPostgresAuditLog(Pool: pool) [2nd]: %v", err)
	}

	if err := log1.Close(); err != nil {
		t.Fatalf("log1.Close(): %v", err)
	}

	// The shared pool, and the sibling AuditLog still using it, must
	// remain usable after log1.Close() — proving Close() didn't close a
	// pool it doesn't own.
	if _, err := log2.Record(ctx, AuditEvent{ChainID: testChainID, ActorID: "a", EntityType: "t", EntityID: "1", Action: "create"}); err != nil {
		t.Fatalf("Record on log2 after log1.Close(): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pool.Ping() after log1.Close(): %v, want the shared pool to still be open", err)
	}

	_ = log2.Close()
}

// TestPostgresSchemaApplyIsIdempotent proves repeated schema application
// against the same database is safe (CREATE TABLE/INDEX IF NOT EXISTS,
// never erroring on the second run). NewPostgresAuditLog itself no longer
// applies schema at all (see postgres.go), so this now exercises that
// property through newPostgresLog's own explicit applyPostgresSchema call
// (via truncatePostgresTestDB) instead — calling it twice in a row is
// exactly what every test in this file already does implicitly, one call
// at a time; this test just makes the property explicit.
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
// TestMongoAuditLog_Query_InvalidStoredPayload (or its GetEntry
// counterpart, TestMongoAuditLog_GetEntry_InvalidStoredPayload) — the
// payload column is jsonb, and Postgres itself rejects syntactically
// invalid JSON at the type level on write (confirmed: a raw UPDATE with a
// non-JSON string fails with SQLSTATE 22P02 before it ever reaches
// DecodeStoredPayload), so that error branch is genuinely unreachable via
// SQL-level corruption on this backend — in both Query and GetEntry — not
// an untested gap.

// BenchmarkPostgresAuditLog_Record_ChainConcurrency is this repo's
// first-ever Benchmark* function (make bench previously built and passed
// but exercised nothing), added to give the chainLockSubKey narrowing in
// postgres.go's Record (see docs/architecture.md's "Postgres advisory-lock
// chain scoping" section) something concrete to compare before/after: the
// SameChain subbenchmark has every parallel goroutine write to one shared
// chain (fully serialized by the advisory lock, same as before this
// narrowing), CrossChain gives each goroutine its own chain (no
// serialization between them since Stage 2). This is a manually-run
// comparison, not a CI gate — no assertion here would be meaningful, since
// relative throughput depends on the machine and Postgres instance running
// it. Run with:
//
//	go test -run '^$' -bench BenchmarkPostgresAuditLog_Record_ChainConcurrency -benchmem -benchtime=3s .
func BenchmarkPostgresAuditLog_Record_ChainConcurrency(b *testing.B) {
	if _, err := newPostgresLog(); err != nil {
		b.Skipf("PostgreSQL not available, skipping: %v", err)
	}

	b.Run("SameChain", func(b *testing.B) {
		log, err := newPostgresLog()
		if err != nil {
			b.Fatalf("newPostgresLog: %v", err)
		}
		defer log.Close()
		ctx := context.Background()

		var n int64
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				id := atomic.AddInt64(&n, 1)
				if _, err := log.Record(ctx, AuditEvent{
					ChainID: testChainID, ActorID: "bench", EntityType: "t", EntityID: fmt.Sprintf("%d", id), Action: "create",
				}); err != nil {
					b.Fatalf("Record: %v", err)
				}
			}
		})
	})

	b.Run("CrossChain", func(b *testing.B) {
		log, err := newPostgresLog()
		if err != nil {
			b.Fatalf("newPostgresLog: %v", err)
		}
		defer log.Close()
		ctx := context.Background()

		var n, worker int64
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			// Each parallel goroutine (RunParallel's own unit of work)
			// gets its own chain, so no two goroutines ever contend for
			// the same chainLockSubKey.
			chainID := fmt.Sprintf("bench-chain-%d", atomic.AddInt64(&worker, 1))
			for pb.Next() {
				id := atomic.AddInt64(&n, 1)
				if _, err := log.Record(ctx, AuditEvent{
					ChainID: chainID, ActorID: "bench", EntityType: "t", EntityID: fmt.Sprintf("%d", id), Action: "create",
				}); err != nil {
					b.Fatalf("Record: %v", err)
				}
			}
		})
	})
}
