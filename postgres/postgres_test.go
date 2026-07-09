// File: postgres/postgres_test.go

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/gourdian25/graudit"
	"github.com/gourdian25/graudit/conformance"
	graudpostgres "github.com/gourdian25/graudit/postgres"
	"github.com/gourdian25/grevents"
)

// testDSN uses the same host/port/credentials as grcache's/gourdiantoken's
// own local Postgres test setup, with a distinct dbname (graudit_test) so
// all three repos' tests can run against the same local instance without
// colliding.
const testDSN = "host=localhost user=postgres_user password=postgres_password dbname=graudit_test port=5432 sslmode=disable"

// truncate clears graudit_entries so the next NewPostgresAuditLog call sees
// an empty chain. Every conformance scenario calls newLog() expecting a
// fresh, empty AuditLog (true by construction for the memory backend, but
// postgres/mongo backends reconnect to the same persistent table/database
// on every call unless explicitly cleared first) — this must run before
// *every* newLog()/newLogWithBus() call, not just once at the top of
// TestConformance, or later scenarios see entries left behind by earlier
// ones and fail with ID/hash mismatches that look like corruption but are
// actually just test-isolation bugs.
func truncate() {
	db, err := gorm.Open(postgres.Open(testDSN), &gorm.Config{})
	if err != nil {
		return // surfaced properly by NewPostgresAuditLog's own connect/ping right after
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	// Ignored if the table doesn't exist yet — AutoMigrate inside
	// NewPostgresAuditLog creates it on first connect.
	_ = db.Exec("TRUNCATE TABLE graudit_entries").Error
}

func newLog() (graudit.AuditLog, error) {
	truncate()
	return graudpostgres.NewPostgresAuditLog(graudpostgres.PostgresConfig{DSN: testDSN})
}

func newLogWithBus(bus grevents.Bus) (graudit.AuditLog, error) {
	truncate()
	return graudpostgres.NewPostgresAuditLog(graudpostgres.PostgresConfig{DSN: testDSN, EventBus: bus})
}

// tamperEntry bypasses the AuditLog interface entirely via a raw SQL
// UPDATE against the same test DB, simulating an attacker or a bug
// elsewhere touching the table directly.
func tamperEntry(t *testing.T, log graudit.AuditLog, entryID graudit.EntryID) {
	t.Helper()
	db, err := gorm.Open(postgres.Open(testDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("connect for tamper: %v", err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()

	res := db.Exec(`UPDATE graudit_entries SET payload = ? WHERE entry_id = ?`, []byte(`{"tampered":true}`), uint64(entryID))
	if res.Error != nil {
		t.Fatalf("tamper UPDATE: %v", res.Error)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("tamper UPDATE affected %d rows, want 1", res.RowsAffected)
	}
}

func TestConformance(t *testing.T) {
	conformance.Run(t, newLog, newLogWithBus, conformance.WithTamperHook(tamperEntry))
}

func TestNewPostgresAuditLog_MissingDSN(t *testing.T) {
	if _, err := graudpostgres.NewPostgresAuditLog(graudpostgres.PostgresConfig{}); err == nil {
		t.Fatal("expected an error for a missing DSN, got nil")
	}
}

func TestNewPostgresAuditLog_BadDSN(t *testing.T) {
	_, err := graudpostgres.NewPostgresAuditLog(graudpostgres.PostgresConfig{
		DSN: "host=localhost user=nope password=nope dbname=nope port=1 sslmode=disable",
	})
	if err == nil {
		t.Fatal("expected an error for an unreachable DSN, got nil")
	}
}

func TestNewPostgresAuditLog_FullConfig(t *testing.T) {
	truncate()
	log, err := graudpostgres.NewPostgresAuditLog(graudpostgres.PostgresConfig{
		DSN:             testDSN,
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxLifetime: time.Minute,
		Logger:          &recordingLogger{},
	})
	if err != nil {
		t.Fatalf("NewPostgresAuditLog with full config: %v", err)
	}
	defer log.Close()
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

// Note: there is no postgres equivalent of mongo's
// TestQuery_InvalidStoredPayload — the payload column is jsonb, and
// Postgres itself rejects syntactically invalid JSON at the type level on
// write (confirmed: a raw UPDATE with a non-JSON string fails with
// SQLSTATE 22P02 before it ever reaches graudit.DecodeStoredPayload), so
// that error branch is genuinely unreachable via SQL-level corruption on
// this backend, not an untested gap.

type recordingLogger struct{}

func (*recordingLogger) Infof(string, ...interface{})  {}
func (*recordingLogger) Warnf(string, ...interface{})  {}
func (*recordingLogger) Errorf(string, ...interface{}) {}
