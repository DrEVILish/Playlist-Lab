package db

import (
	"database/sql"
	"fmt"
)

// columnSpec is one additive column that may be missing from a database file
// created before schema.sql included it. schema.sql's CREATE TABLE IF NOT
// EXISTS only helps a brand-new database; an existing production file needs
// these run explicitly, so this list must stay in lockstep with the ALTER
// TABLE checks the Node server's database/init.ts already performs.
type columnSpec struct {
	table  string
	column string
	ddl    string
}

var additiveColumns = []columnSpec{
	{"user_settings", "gemini_api_key", "ALTER TABLE user_settings ADD COLUMN gemini_api_key TEXT"},
	{"user_settings", "grok_api_key", "ALTER TABLE user_settings ADD COLUMN grok_api_key TEXT"},
	{"user_settings", "ai_provider", "ALTER TABLE user_settings ADD COLUMN ai_provider TEXT DEFAULT 'gemini'"},
	{"users", "spotify_access_token", "ALTER TABLE users ADD COLUMN spotify_access_token TEXT"},
	{"users", "spotify_refresh_token", "ALTER TABLE users ADD COLUMN spotify_refresh_token TEXT"},
	{"users", "spotify_token_expires_at", "ALTER TABLE users ADD COLUMN spotify_token_expires_at INTEGER"},
	{"users", "spotify_client_id", "ALTER TABLE users ADD COLUMN spotify_client_id TEXT"},
	{"users", "spotify_client_secret", "ALTER TABLE users ADD COLUMN spotify_client_secret TEXT"},
	{"cached_playlists", "cover_url", "ALTER TABLE cached_playlists ADD COLUMN cover_url TEXT"},
	{"users", "is_enabled", "ALTER TABLE users ADD COLUMN is_enabled INTEGER DEFAULT 1"},
	{"oauth_connections", "expires_at", "ALTER TABLE oauth_connections ADD COLUMN expires_at INTEGER"},
	{"schedules", "created_at", "ALTER TABLE schedules ADD COLUMN created_at INTEGER"},
	{"user_servers", "access_token", "ALTER TABLE user_servers ADD COLUMN access_token TEXT"},
}

// runMigrations applies any additive columns missing from an existing
// database file. Idempotent: safe to run on every startup, same as the
// Node server's init.ts.
func runMigrations(sqlDB *sql.DB) error {
	for _, spec := range additiveColumns {
		has, err := hasColumn(sqlDB, spec.table, spec.column)
		if err != nil {
			return fmt.Errorf("checking %s.%s: %w", spec.table, spec.column, err)
		}
		if has {
			continue
		}
		if _, err := sqlDB.Exec(spec.ddl); err != nil {
			return fmt.Errorf("adding %s.%s: %w", spec.table, spec.column, err)
		}
	}
	return nil
}

func hasColumn(sqlDB *sql.DB, table, column string) (bool, error) {
	rows, err := sqlDB.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultVal, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
