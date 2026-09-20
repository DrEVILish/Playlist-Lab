-- Playlist Lab Web Server Database Schema
-- SQLite database schema for multi-user playlist management

-- Enable foreign key constraints
PRAGMA foreign_keys = ON;

-- Users table
-- Stores authenticated Plex users
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  plex_user_id TEXT UNIQUE NOT NULL,
  plex_username TEXT NOT NULL,
  plex_token TEXT NOT NULL,  -- Encrypted Plex authentication token
  plex_thumb TEXT,           -- User avatar URL
  created_at INTEGER NOT NULL,
  last_login INTEGER NOT NULL,
  is_enabled INTEGER DEFAULT 1,  -- Whether user is allowed to access the app
  gemini_api_key TEXT,
  grok_api_key TEXT,
  ai_provider TEXT DEFAULT 'gemini',
  spotify_access_token TEXT,
  spotify_refresh_token TEXT,
  spotify_token_expires_at INTEGER,
  spotify_client_id TEXT,
  spotify_client_secret TEXT
);

-- Index for fast user lookup by Plex ID
CREATE INDEX IF NOT EXISTS idx_users_plex_user_id ON users(plex_user_id);

-- User servers table
-- Stores each user's Plex server configurations
CREATE TABLE IF NOT EXISTS user_servers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  server_name TEXT NOT NULL,
  server_client_id TEXT NOT NULL,
  server_url TEXT NOT NULL,
  library_id TEXT,
  library_name TEXT,
  access_token TEXT,  -- Server-specific token from Plex resources; NULL means use the account token
  is_owner INTEGER NOT NULL DEFAULT 0,  -- Plex account owns this server (vs a shared/managed user) - gates the Collections page
  is_default INTEGER NOT NULL DEFAULT 0,  -- the one server music-only features (Playlists, Mixes, Import, ...) resolve to automatically
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Index for fast server lookup by user
CREATE INDEX IF NOT EXISTS idx_user_servers_user_id ON user_servers(user_id);

-- A user may link multiple Plex servers (DESIGN.md §11.11) but at most one
-- may be their default - SQLite enforces this directly via a partial
-- unique index rather than relying on application code to keep it true.
-- NOT created here: on a database that predates the is_default column,
-- this would fail schema.sql's exec (which runs before the ALTER TABLE
-- that adds the column via migrations.go) with "no such column" - see
-- db.go's Open(), which creates this same index again after runMigrations,
-- same fix already used for idx_sessions_user_id.

-- User settings table
-- Stores per-user configuration for matching and mix generation
CREATE TABLE IF NOT EXISTS user_settings (
  user_id INTEGER PRIMARY KEY,
  country TEXT DEFAULT 'global',
  matching_settings TEXT,  -- JSON: matching algorithm configuration
  mix_settings TEXT,       -- JSON: mix generation configuration
  gemini_api_key TEXT,     -- Encrypted Gemini API key for AI features
  grok_api_key TEXT,       -- Encrypted Grok API key for AI features
  ai_provider TEXT DEFAULT 'gemini',  -- 'gemini' or 'grok'
  text_scale TEXT DEFAULT 'medium',   -- Settings > Appearance (DESIGN.md §14): 'small'/'medium'/'large'
  editor_columns TEXT,                -- JSON: Editor page column show/hide + order (DESIGN.md §11.2)
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Playlists table
-- Stores playlists created by users
CREATE TABLE IF NOT EXISTS playlists (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  plex_playlist_id TEXT NOT NULL,
  name TEXT NOT NULL,
  source TEXT NOT NULL,  -- 'spotify', 'deezer', 'apple', 'tidal', etc.
  source_url TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Index for fast playlist lookup by user
CREATE INDEX IF NOT EXISTS idx_playlists_user_id ON playlists(user_id);

-- Index for fast playlist lookup by Plex playlist ID
CREATE INDEX IF NOT EXISTS idx_playlists_plex_playlist_id ON playlists(plex_playlist_id);

-- Collections table
-- Stores Kometa-style Plex Collection definitions (movie/show/music
-- libraries). Plex itself remains the source of truth for membership -
-- this is a thin definition row, mirroring how the playlists table works.
CREATE TABLE IF NOT EXISTS collections (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  server_id INTEGER NOT NULL,
  library_section_id TEXT NOT NULL,
  library_type TEXT NOT NULL,           -- Plex's own 'movie' | 'show' | 'artist'
  plex_collection_id TEXT,              -- NULL until the first successful refresh creates it
  name TEXT NOT NULL,
  description TEXT,
  builder_type TEXT NOT NULL DEFAULT 'smart',  -- 'smart' (rule-based filter) | 'manual' (explicit item list) | 'external_list' (TMDb/IMDb/TVDb/Letterboxd list or chart)
  rules TEXT,            -- JSON: []Rule (AND-combined) when builder_type='smart', or a single ExternalListSource object when builder_type='external_list'
  manual_items TEXT,     -- JSON array of Plex ratingKeys, used when builder_type = 'manual'
  sync_mode TEXT NOT NULL DEFAULT 'sync',  -- 'sync' (add + remove stale) | 'add_only'
  sort_title TEXT,       -- optional Plex sort title override (Kometa-style "+1_Trending" ordering prefixes)
  dynamic_set_id INTEGER,  -- set when this row was generated by a dynamic_collections set rather than created by hand
  dynamic_key TEXT,        -- the facet value (e.g. genre name) this row was generated for, when dynamic_set_id is set
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY (server_id) REFERENCES user_servers(id) ON DELETE CASCADE,
  FOREIGN KEY (dynamic_set_id) REFERENCES dynamic_collections(id) ON DELETE CASCADE
);

-- Dynamic Collections (Kometa's "dynamic_collections" feature): one
-- definition that expands into many real `collections` rows, one per
-- distinct value of a library facet (genre, decade, year, content rating,
-- studio/network, actor, mood, style) - see
-- internal/services/scheduler/dynamic_collections.go for the generation
-- logic and internal/handlers/dynamic_collections.go for the UI.
CREATE TABLE IF NOT EXISTS dynamic_collections (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  server_id INTEGER NOT NULL,
  library_section_id TEXT NOT NULL,
  library_type TEXT NOT NULL,
  name TEXT NOT NULL,               -- label for this set, e.g. "Genres" - not a generated collection's own name
  facet_type TEXT NOT NULL,         -- 'genre' | 'decade' | 'year' | 'content_rating' | 'studio' | 'actor' | 'mood' | 'style'
  title_format TEXT NOT NULL DEFAULT '<<key_name>>',  -- Kometa's own placeholder convention; must contain <<key_name>>
  include_keys TEXT,   -- JSON []string - if non-empty, only these keys get a collection (mutually exclusive with exclude_keys)
  exclude_keys TEXT,   -- JSON []string - keys to skip
  sync_mode TEXT NOT NULL DEFAULT 'sync',  -- 'sync' removes previously-generated collections whose key no longer appears; 'add_only' never removes one
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY (server_id) REFERENCES user_servers(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_dynamic_collections_user_id ON dynamic_collections(user_id);

-- Index for fast collection lookup by user
CREATE INDEX IF NOT EXISTS idx_collections_user_id ON collections(user_id);

-- Missing Films/TV Shows (DESIGN.md §11.11): an external-list-sourced
-- collection's list/chart entries that don't match anything in the target
-- Plex library - the movie/show analog of missing_tracks below, scoped to
-- a collection instead of a playlist. Replaced wholesale on every refresh
-- of that collection (see db.ReplaceMissingCollectionItems), same
-- convention as AddMissingTracks. guid_key is the provider-prefixed id
-- (e.g. 'tmdb://11974', 'imdb://tt0096734', 'tvdb://5869') - the same
-- string Plex's own Guid data would carry for a matching library item, see
-- internal/services/medialist.
CREATE TABLE IF NOT EXISTS missing_collection_items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  collection_id INTEGER NOT NULL,
  user_id INTEGER NOT NULL,
  guid_key TEXT NOT NULL,
  media_type TEXT NOT NULL,  -- 'movie' | 'tv'
  title TEXT NOT NULL,
  year INTEGER,
  added_at INTEGER NOT NULL,
  FOREIGN KEY (collection_id) REFERENCES collections(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  UNIQUE(collection_id, guid_key)
);

CREATE INDEX IF NOT EXISTS idx_missing_collection_items_user_id ON missing_collection_items(user_id);

-- Schedules table
-- Stores automated playlist refresh and mix generation schedules
CREATE TABLE IF NOT EXISTS schedules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  playlist_id INTEGER,
  collection_id INTEGER,
  schedule_type TEXT NOT NULL,  -- 'playlist_refresh', 'mix_generation', or 'collection_refresh'
  frequency TEXT NOT NULL,      -- 'daily', 'weekly', 'fortnightly', 'monthly'
  start_date TEXT NOT NULL,
  last_run INTEGER,
  config TEXT,  -- JSON: additional configuration (mix types, etc.)
  created_at INTEGER,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY (playlist_id) REFERENCES playlists(id) ON DELETE CASCADE,
  FOREIGN KEY (collection_id) REFERENCES collections(id) ON DELETE CASCADE
);

-- Index for fast schedule lookup by user
CREATE INDEX IF NOT EXISTS idx_schedules_user_id ON schedules(user_id);

-- Index for finding due schedules efficiently
CREATE INDEX IF NOT EXISTS idx_schedules_last_run ON schedules(last_run);

-- Missing tracks table
-- Stores tracks that couldn't be matched during import
CREATE TABLE IF NOT EXISTS missing_tracks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  playlist_id INTEGER NOT NULL,
  title TEXT NOT NULL,
  artist TEXT NOT NULL,
  album TEXT,
  position INTEGER NOT NULL,
  after_track_key TEXT,
  added_at INTEGER NOT NULL,
  source TEXT NOT NULL,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY (playlist_id) REFERENCES playlists(id) ON DELETE CASCADE
);

-- Index for fast missing tracks lookup by user
CREATE INDEX IF NOT EXISTS idx_missing_tracks_user_id ON missing_tracks(user_id);

-- Index for fast missing tracks lookup by playlist
CREATE INDEX IF NOT EXISTS idx_missing_tracks_playlist_id ON missing_tracks(playlist_id);

-- Composite index for grouping missing tracks by user and playlist
CREATE INDEX IF NOT EXISTS idx_missing_tracks_user_playlist ON missing_tracks(user_id, playlist_id);

-- Cached playlists table
-- Stores scraped playlist data to avoid re-scraping
CREATE TABLE IF NOT EXISTS cached_playlists (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source TEXT NOT NULL,      -- 'spotify', 'deezer', etc.
  source_id TEXT NOT NULL,   -- External playlist ID or chart ID
  name TEXT NOT NULL,
  description TEXT,
  tracks TEXT NOT NULL,      -- JSON array of tracks
  cover_url TEXT,            -- Playlist cover image URL
  scraped_at INTEGER NOT NULL,
  UNIQUE(source, source_id)
);

-- Index for fast cache lookup by source and ID
CREATE INDEX IF NOT EXISTS idx_cached_playlists_source_id ON cached_playlists(source, source_id);

-- Index for finding stale cache entries
CREATE INDEX IF NOT EXISTS idx_cached_playlists_scraped_at ON cached_playlists(scraped_at);

-- Sessions table
-- Stores user sessions for authentication
CREATE TABLE IF NOT EXISTS sessions (
  sid TEXT PRIMARY KEY,
  sess TEXT NOT NULL,
  expired INTEGER NOT NULL,
  user_id INTEGER  -- denormalized out of sess's JSON for the Settings > Sessions list (DESIGN.md §11.4) to query by indexed column instead of scanning/decoding every row
);

-- Index for cleaning up expired sessions
CREATE INDEX IF NOT EXISTS idx_sessions_expired ON sessions(expired);

-- Admin users table
-- Stores which users have admin privileges
CREATE TABLE IF NOT EXISTS admin_users (
  user_id INTEGER PRIMARY KEY,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Playlist shares table
-- Tracks which playlists have been shared with which users
CREATE TABLE IF NOT EXISTS playlist_shares (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  playlist_id INTEGER NOT NULL,
  owner_user_id INTEGER NOT NULL,
  shared_with_user_id INTEGER NOT NULL,
  plex_playlist_id TEXT NOT NULL,
  playlist_name TEXT NOT NULL,
  shared_at INTEGER NOT NULL,
  FOREIGN KEY (playlist_id) REFERENCES playlists(id) ON DELETE CASCADE,
  FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY (shared_with_user_id) REFERENCES users(id) ON DELETE CASCADE,
  UNIQUE(playlist_id, shared_with_user_id)
);

-- Index for finding playlists shared with a user
CREATE INDEX IF NOT EXISTS idx_playlist_shares_shared_with ON playlist_shares(shared_with_user_id);

-- Index for finding shares by owner
CREATE INDEX IF NOT EXISTS idx_playlist_shares_owner ON playlist_shares(owner_user_id);

-- Manual matches table
-- Remembers a user's explicit "use this Plex track" choice for a source
-- track (title/artist/album), so future imports/retries reuse it instead of
-- re-running the fuzzy search for the same track every time.
CREATE TABLE IF NOT EXISTS manual_matches (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  title TEXT NOT NULL,
  artist TEXT NOT NULL,
  album TEXT,
  plex_rating_key TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_manual_matches_user_id ON manual_matches(user_id);
CREATE INDEX IF NOT EXISTS idx_manual_matches_lookup ON manual_matches(user_id, title, artist);

-- Cross-import jobs table
-- Stores completed and in-progress cross-playlist import operations
CREATE TABLE IF NOT EXISTS cross_import_jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  source_service TEXT NOT NULL,
  source_playlist_name TEXT NOT NULL,
  target_service TEXT NOT NULL,
  target_playlist_name TEXT,
  matched_count INTEGER NOT NULL DEFAULT 0,
  unmatched_count INTEGER NOT NULL DEFAULT 0,
  skipped_count INTEGER NOT NULL DEFAULT 0,
  total_count INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pending',  -- 'pending', 'matching', 'review', 'complete', 'failed'
  unmatched_tracks TEXT,                   -- JSON array of {title, artist}
  created_at INTEGER NOT NULL,
  completed_at INTEGER,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_cross_import_jobs_user_id ON cross_import_jobs(user_id);
CREATE INDEX IF NOT EXISTS idx_cross_import_jobs_created_at ON cross_import_jobs(created_at);

-- OAuth connections table
-- Stores OAuth tokens for external service targets (all services except Spotify, which uses the users table)
CREATE TABLE IF NOT EXISTS oauth_connections (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  service TEXT NOT NULL,         -- e.g. 'deezer', 'tidal', 'youtube'
  access_token TEXT NOT NULL,    -- encrypted
  refresh_token TEXT,            -- encrypted, if provided (NEW: for YouTube OAuth)
  token_expires_at INTEGER,      -- token expiry timestamp in ms (NEW: for YouTube OAuth)
  expires_at INTEGER,            -- alias for token_expires_at (for compatibility)
  scope TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(user_id, service),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_oauth_connections_user_service ON oauth_connections(user_id, service);

-- Mix templates table
-- Stores saved mix configurations for quick regeneration
CREATE TABLE IF NOT EXISTS mix_templates (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  description TEXT,
  mix_type TEXT NOT NULL,        -- 'artist', 'album', 'genre', 'mood', 'decade', 'custom'
  configuration TEXT NOT NULL,   -- JSON blob with all mix parameters
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  last_used_at INTEGER,
  use_count INTEGER DEFAULT 0,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_mix_templates_user_id ON mix_templates(user_id);
CREATE INDEX IF NOT EXISTS idx_mix_templates_mix_type ON mix_templates(mix_type);
-- Composite index for sorting by last_used_at and updated_at (performance optimization)
CREATE INDEX IF NOT EXISTS idx_mix_templates_user_usage ON mix_templates(user_id, last_used_at DESC, updated_at DESC);

-- Schedule executions table
-- Stores execution history for schedules
CREATE TABLE IF NOT EXISTS schedule_executions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  schedule_id INTEGER NOT NULL,
  user_id INTEGER NOT NULL,
  status TEXT NOT NULL,           -- 'running', 'success', 'failed'
  started_at INTEGER NOT NULL,
  completed_at INTEGER,
  tracks_matched INTEGER DEFAULT 0,
  tracks_unmatched INTEGER DEFAULT 0,
  error_message TEXT,
  playlist_name TEXT,
  FOREIGN KEY (schedule_id) REFERENCES schedules(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_schedule_executions_schedule_id ON schedule_executions(schedule_id);
CREATE INDEX IF NOT EXISTS idx_schedule_executions_user_id ON schedule_executions(user_id);
CREATE INDEX IF NOT EXISTS idx_schedule_executions_started_at ON schedule_executions(started_at DESC);

-- Saved Spotify users table
-- Stores Spotify user IDs that users want quick access to
CREATE TABLE IF NOT EXISTS saved_spotify_users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  spotify_user_id TEXT NOT NULL,
  display_name TEXT NOT NULL,
  added_at INTEGER NOT NULL,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  UNIQUE(user_id, spotify_user_id)
);

CREATE INDEX IF NOT EXISTS idx_saved_spotify_users_user_id ON saved_spotify_users(user_id);

-- Import queue table
-- Stores import jobs that are queued or in progress
CREATE TABLE IF NOT EXISTS import_queue (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT UNIQUE NOT NULL,
  user_id INTEGER NOT NULL,
  source TEXT NOT NULL,
  url TEXT NOT NULL,
  playlist_name TEXT,
  status TEXT NOT NULL DEFAULT 'queued',  -- 'queued', 'processing', 'completed', 'failed', 'cancelled'
  progress INTEGER DEFAULT 0,
  total INTEGER DEFAULT 0,
  error_message TEXT,
  result TEXT,  -- JSON: import result data
  created_at INTEGER NOT NULL,
  started_at INTEGER,
  completed_at INTEGER,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_import_queue_user_id ON import_queue(user_id);
CREATE INDEX IF NOT EXISTS idx_import_queue_status ON import_queue(status);
CREATE INDEX IF NOT EXISTS idx_import_queue_session_id ON import_queue(session_id);


-- Deemix downloads table
-- One row per download queued with deemix-server, kept only while it is in
-- flight. deemix's own queue survives a restart of this server, but the
-- poller that mirrors its progress into a notification - and, crucially, the
-- link back to the missing_tracks row the download was queued for - lived
-- only in memory. A restart mid-download therefore left the file arriving in
-- the library with nothing left to reconcile it, so the track stayed
-- "missing" forever unless the user happened to hit Retry. These rows let
-- those pollers be restarted on boot, and are deleted as soon as a download
-- reaches a terminal state.
CREATE TABLE IF NOT EXISTS deemix_downloads (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  uuid TEXT NOT NULL,           -- deemix's own queue id, `${type}_${id}_${bitrate}`
  missing_track_id INTEGER,     -- NULL for admin bulk downloads, which have no single missing row to reconcile back to
  title TEXT NOT NULL,
  detail TEXT,
  created_at INTEGER NOT NULL,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_deemix_downloads_user_id ON deemix_downloads(user_id);

-- Admin config table
-- Simple key/value store for admin-editable, server-wide settings (Deemix
-- ARL, Lidarr URL/API key) that the Node server instead persisted to a JSON
-- config file - a table on the same already-open DB is less machinery than
-- a second config file for the handful of values the Go admin page edits.
CREATE TABLE IF NOT EXISTS admin_config (
  key TEXT PRIMARY KEY,
  value TEXT
);
