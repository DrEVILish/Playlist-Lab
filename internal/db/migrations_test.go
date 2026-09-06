package db

import "testing"

// TestOpenIdempotent mirrors migration.property.test.ts's expectation that
// running the migration/init path twice against the same database file is
// safe - no error, no duplicate-column failures.
func TestOpenIdempotent(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"

	sqlDB1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := CreateUser(sqlDB1, "plex1", "user1", "tok1", ""); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sqlDB1.Close()

	// Re-open the same file - schema.sql's CREATE TABLE IF NOT EXISTS and
	// runMigrations' hasColumn checks must both tolerate an already-migrated DB.
	sqlDB2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open (should be idempotent): %v", err)
	}
	defer sqlDB2.Close()

	count, err := GetUserCount(sqlDB2)
	if err != nil {
		t.Fatalf("GetUserCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected data to survive re-open, got %d users", count)
	}

	// Also directly call runMigrations twice in a row on the live handle.
	if err := runMigrations(sqlDB2); err != nil {
		t.Fatalf("runMigrations first extra call: %v", err)
	}
	if err := runMigrations(sqlDB2); err != nil {
		t.Fatalf("runMigrations second extra call: %v", err)
	}
}

func TestHasColumn(t *testing.T) {
	sqlDB := newTestDB(t)

	has, err := hasColumn(sqlDB, "users", "plex_user_id")
	if err != nil {
		t.Fatalf("hasColumn: %v", err)
	}
	if !has {
		t.Fatal("expected users.plex_user_id to exist")
	}

	has, err = hasColumn(sqlDB, "users", "no_such_column")
	if err != nil {
		t.Fatalf("hasColumn: %v", err)
	}
	if has {
		t.Fatal("expected users.no_such_column to not exist")
	}
}
