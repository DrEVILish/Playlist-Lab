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

	// Run after runMigrations, not folded into schema.sql above: on a
	// database that predates the sessions.user_id column, an index on it
	// would fail schema.sql's exec (which runs before the ALTER TABLE that
	// adds the column) with "no such column". CREATE INDEX IF NOT EXISTS is
	// idempotent either way, so this is safe to run on every startup.
	if _, err := sqlDB.Exec("CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id)"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("creating sessions user_id index: %w", err)
	}

	// Same reasoning as the sessions index above: on a database that
	// predates user_servers.is_default, this index would fail schema.sql's
	// exec (which runs before runMigrations adds the column).
	if _, err := sqlDB.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_user_servers_one_default_per_user
		ON user_servers(user_id) WHERE is_default = 1
	`); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("creating user_servers default index: %w", err)
	}

	return sqlDB, nil
}
