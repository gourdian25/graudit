// File: postgres/postgres_test.go

package postgres_test

import (
	"testing"

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

func truncateTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(postgres.Open(testDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("connect for truncate: %v", err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.Exec("TRUNCATE TABLE graudit_entries").Error; err != nil {
		// Table may not exist yet on a brand-new test DB — AutoMigrate
		// inside NewPostgresAuditLog creates it on first connect.
		t.Logf("truncate graudit_entries (ignored if table doesn't exist yet): %v", err)
	}
}

func newLog() (graudit.AuditLog, error) {
	return graudpostgres.NewPostgresAuditLog(graudpostgres.PostgresConfig{DSN: testDSN})
}

func newLogWithBus(bus grevents.Bus) (graudit.AuditLog, error) {
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
	truncateTestDB(t)
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
