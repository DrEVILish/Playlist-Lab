// Package db owns the single SQLite connection and schema, mirroring the
// current Node server's better-sqlite3 setup (WAL mode, foreign keys on,
// synchronous single-writer access) so the existing production database
// file can be opened as-is at cutover with no data migration.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Open connects to the SQLite file at path, creating its parent directory
// and applying schema.sql + any pending additive-column migrations.
//
// SetMaxOpenConns(1) replicates better-sqlite3's synchronous single-writer
// model: SQLite allows only one writer at a time regardless, but a Go
// *sql.DB will otherwise open multiple connections and let writers queue on
// SQLITE_BUSY instead of Go's own connection pool, which is harder to
// reason about than serializing at the pool.
func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating database directory: %w", err)
		}
	}

	sqlDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := sqlDB.Exec(pragma); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}

	if _, err := sqlDB.Exec(schemaSQL); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}

	if err := runMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return sqlDB, nil
}
