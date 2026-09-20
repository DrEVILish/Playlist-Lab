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
	// DESIGN.md §14/§11.2: per-user UI preferences, following the same
	// scalar-column-vs-JSON-blob-column split already used above (ai_provider
	// is a plain scalar; mix_settings/matching_settings are JSON blobs) -
	// text_scale is a simple enum, editor_columns is structured.
	{"user_settings", "text_scale", "ALTER TABLE user_settings ADD COLUMN text_scale TEXT DEFAULT 'medium'"},
	{"user_settings", "editor_columns", "ALTER TABLE user_settings ADD COLUMN editor_columns TEXT"},
	// DESIGN.md §11.4: lets Settings > Sessions list a user's active sessions
	// by an indexed column instead of scanning every row in the table and
	// JSON-decoding sess to find the ones that match.
	{"sessions", "user_id", "ALTER TABLE sessions ADD COLUMN user_id INTEGER"},
	// DESIGN.md §11.11: Collections page gate - Plex account ownership of the
	// server (not this app's own admin flag), set once at server-select time.
	{"user_servers", "is_owner", "ALTER TABLE user_servers ADD COLUMN is_owner INTEGER NOT NULL DEFAULT 0"},
	// DESIGN.md §11.11: lets a schedule row target a collection refresh
	// alongside the existing playlist_id/mix_generation schedule types.
	{"schedules", "collection_id", "ALTER TABLE schedules ADD COLUMN collection_id INTEGER REFERENCES collections(id) ON DELETE CASCADE"},
	// DESIGN.md §11.11: multi-server support - the one server music-only
	// features resolve to automatically (GetUserMusicServer) when a user
	// has linked more than one Plex server.
	{"user_servers", "is_default", "ALTER TABLE user_servers ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0"},
	// Dynamic Collections (Kometa's "dynamic_collections" feature, see
	// internal/services/scheduler/dynamic_collections.go): a generated
	// collection's link back to the set that created it, plus a per-
	// collection Plex sort title override.
	{"collections", "sort_title", "ALTER TABLE collections ADD COLUMN sort_title TEXT"},
	{"collections", "dynamic_set_id", "ALTER TABLE collections ADD COLUMN dynamic_set_id INTEGER REFERENCES dynamic_collections(id) ON DELETE CASCADE"},
	{"collections", "dynamic_key", "ALTER TABLE collections ADD COLUMN dynamic_key TEXT"},
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
	// Backfill: every pre-existing user's one server row becomes their
	// default with zero manual steps. Matches nothing once every user has
	// exactly one is_default=1 row, so safe to run unconditionally on every
	// startup like the rest of this function.
	if _, err := sqlDB.Exec(`
		UPDATE user_servers SET is_default = 1
		WHERE id IN (
			SELECT MIN(id) FROM user_servers GROUP BY user_id HAVING SUM(is_default) = 0
		)
	`); err != nil {
		return fmt.Errorf("backfilling user_servers.is_default: %w", err)
	}

	// missing_collection_items briefly shipped with a tmdb_id INTEGER
	// column before being generalized to guid_key TEXT (same session, no
	// real rows ever existed under the old shape - Collections' external-
	// list builder had zero real-world use before the rename) - drop and
	// recreate rather than coercing data that was never real.
	if hasOld, err := hasColumn(sqlDB, "missing_collection_items", "tmdb_id"); err == nil && hasOld {
		if _, err := sqlDB.Exec(`DROP TABLE missing_collection_items`); err != nil {
			return fmt.Errorf("dropping old-shape missing_collection_items: %w", err)
		}
		if _, err := sqlDB.Exec(`
			CREATE TABLE missing_collection_items (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				collection_id INTEGER NOT NULL,
				user_id INTEGER NOT NULL,
				guid_key TEXT NOT NULL,
				media_type TEXT NOT NULL,
				title TEXT NOT NULL,
				year INTEGER,
				added_at INTEGER NOT NULL,
				FOREIGN KEY (collection_id) REFERENCES collections(id) ON DELETE CASCADE,
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
				UNIQUE(collection_id, guid_key)
			)
		`); err != nil {
			return fmt.Errorf("recreating missing_collection_items: %w", err)
		}
		if _, err := sqlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_missing_collection_items_user_id ON missing_collection_items(user_id)`); err != nil {
			return fmt.Errorf("recreating idx_missing_collection_items_user_id: %w", err)
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
