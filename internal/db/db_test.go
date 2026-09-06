package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// newTestDB opens a throwaway file-backed database for a test. modernc.org/sqlite's
// ":memory:" DSN combined with SetMaxOpenConns(1) works fine for a single connection,
// but Open() appends a busy_timeout pragma to the path via "?", so a plain temp file
// is the least surprising choice and matches how the real server opens its DB.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	sqlDB, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return sqlDB
}
