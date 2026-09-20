package db

import "database/sql"

// UserServer is one Plex Media Server a user has linked. A user may have
// several (DESIGN.md §11.11/multi-server support) - exactly one is flagged
// IsDefault at any time (DB-enforced by a partial unique index on
// user_servers, schema.sql), used by every music-only feature (Playlists,
// Mixes, Import, ...) that has no reason to ask "which server" when the
// user only ever has one music library configured. Collections instead
// carries its own server_id per row and never relies on the default.
type UserServer struct {
	ID             int64
	UserID         int64
	ServerName     string
	ServerClientID string
	ServerURL      string
	LibraryID      sql.NullString
	LibraryName    sql.NullString
	AccessToken    sql.NullString
	IsOwner        bool // Plex account owns this server (vs a shared/managed user) - gates the Collections page, DESIGN.md §11.11
	IsDefault      bool // the one server music-only features resolve to automatically (GetUserMusicServer)
}

const userServerCols = "id, user_id, server_name, server_client_id, server_url, library_id, library_name, access_token, is_owner, is_default"

// AddUserServer inserts a new linked server for a user - unlike the old
// SaveUserServer, this never deletes an existing row: a user may link
// several servers (e.g. one for movies/TV, one for music). The first
// server ever linked for a user automatically becomes their default
// (COUNT check inside the same transaction); every later one is added
// as non-default until explicitly promoted via SetDefaultServer.
func AddUserServer(sqlDB *sql.DB, userID int64, serverName, serverClientID, serverURL, libraryID, libraryName, accessToken string, owned bool) (*UserServer, error) {
	tx, err := sqlDB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var existing int
	if err := tx.QueryRow("SELECT COUNT(*) FROM user_servers WHERE user_id = ?", userID).Scan(&existing); err != nil {
		return nil, err
	}
	isDefault := existing == 0

	res, err := tx.Exec(
		`INSERT INTO user_servers (user_id, server_name, server_client_id, server_url, library_id, library_name, access_token, is_owner, is_default)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, serverName, serverClientID, serverURL, nullIfEmpty(libraryID), nullIfEmpty(libraryName), nullIfEmpty(accessToken), owned, isDefault,
	)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetUserServerByID(sqlDB, id)
}

func GetUserServerByID(sqlDB *sql.DB, id int64) (*UserServer, error) {
	return scanUserServer(sqlDB.QueryRow("SELECT "+userServerCols+" FROM user_servers WHERE id = ?", id))
}

// GetUserServers returns every server a user has linked, default first.
func GetUserServers(sqlDB *sql.DB, userID int64) ([]UserServer, error) {
	rows, err := sqlDB.Query("SELECT "+userServerCols+" FROM user_servers WHERE user_id = ? ORDER BY is_default DESC, id ASC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserServer
	for rows.Next() {
		var s UserServer
		if err := rows.Scan(&s.ID, &s.UserID, &s.ServerName, &s.ServerClientID, &s.ServerURL, &s.LibraryID, &s.LibraryName, &s.AccessToken, &s.IsOwner, &s.IsDefault); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetUserMusicServer resolves the one server every music-only feature
// (Playlists, Mixes, Import, matching, ...) should use: the default among
// servers that actually have a music library configured, else the
// lowest-id one with a library. Returns nil, nil (not an error) when the
// user has no music-configured server yet, same nil-signal GetUserServer
// used to give - every existing "if userServer == nil" caller keeps
// working unchanged.
//
// ponytail: silently prefers the lowest-id music server when 2+ exist and
// neither is the account default - fine for exactly one music-type server
// (the only case this app's setup wizard, artist-library-only, currently
// produces); add an ambiguity signal + a picker only if a second music
// server is ever linked.
func GetUserMusicServer(sqlDB *sql.DB, userID int64) (*UserServer, error) {
	return scanUserServer(sqlDB.QueryRow(
		"SELECT "+userServerCols+" FROM user_servers WHERE user_id = ? AND library_id IS NOT NULL ORDER BY is_default DESC, id ASC LIMIT 1", userID,
	))
}

// SetDefaultServer promotes serverID to be userID's default, demoting any
// other. Returns sql.ErrNoRows if serverID doesn't belong to userID.
func SetDefaultServer(sqlDB *sql.DB, userID, serverID int64) error {
	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("UPDATE user_servers SET is_default = 0 WHERE user_id = ?", userID); err != nil {
		return err
	}
	res, err := tx.Exec("UPDATE user_servers SET is_default = 1 WHERE id = ? AND user_id = ?", serverID, userID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// RemoveUserServer deletes a linked server. If it was the default and other
// servers remain, the lowest-id remaining one is promoted so a default
// always exists whenever >=1 server does. Callers are responsible for
// warning about the collections/schedules ON DELETE CASCADE removes along
// with it (DESIGN.md §11.11) before calling this.
func RemoveUserServer(sqlDB *sql.DB, userID, serverID int64) error {
	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var wasDefault bool
	if err := tx.QueryRow("SELECT is_default FROM user_servers WHERE id = ? AND user_id = ?", serverID, userID).Scan(&wasDefault); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM user_servers WHERE id = ? AND user_id = ?", serverID, userID); err != nil {
		return err
	}
	if wasDefault {
		if _, err := tx.Exec(
			`UPDATE user_servers SET is_default = 1 WHERE user_id = ? AND id = (SELECT MIN(id) FROM user_servers WHERE user_id = ?)`,
			userID, userID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CascadeCountsForServer counts how many collections (and, transitively,
// their schedules) reference a server - both have ON DELETE CASCADE FKs to
// user_servers/collections, so removing a server silently removes these
// too. Callers must surface this before deleting (DESIGN.md §11.11) - the
// Plex-side collection itself is untouched, only Playlist Lab's tracking
// of it disappears, but that's not something to do silently.
func CascadeCountsForServer(sqlDB *sql.DB, serverID int64) (collections, schedules int, err error) {
	if err = sqlDB.QueryRow("SELECT COUNT(*) FROM collections WHERE server_id = ?", serverID).Scan(&collections); err != nil {
		return 0, 0, err
	}
	if err = sqlDB.QueryRow(
		"SELECT COUNT(*) FROM schedules WHERE collection_id IN (SELECT id FROM collections WHERE server_id = ?)", serverID,
	).Scan(&schedules); err != nil {
		return 0, 0, err
	}
	return collections, schedules, nil
}

// UpdateServerURL refreshes a linked server's stored connection URL - the
// Settings "refresh connection" action (DESIGN.md §11.11), for when network
// topology changes which of a server's connections (local/remote/relay)
// is best after it was originally linked.
func UpdateServerURL(sqlDB *sql.DB, id int64, url string) error {
	_, err := sqlDB.Exec("UPDATE user_servers SET server_url = ? WHERE id = ?", url, id)
	return err
}

func scanUserServer(row *sql.Row) (*UserServer, error) {
	var s UserServer
	err := row.Scan(&s.ID, &s.UserID, &s.ServerName, &s.ServerClientID, &s.ServerURL, &s.LibraryID, &s.LibraryName, &s.AccessToken, &s.IsOwner, &s.IsDefault)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// CopyServerConfig gives toUserID copies of every server fromUserID has
// linked, if any - ports database.ts's copyServerConfig, used both at
// login-time auto-approval and admin's manual "Enable" so a newly-approved
// user doesn't have to run the server/library picker themselves. Copies
// all linked servers, not just the default: an admin who's linked both a
// movie/TV server and a music server clearly intends an approved user to
// have both, not just one. access_token is deliberately not copied: it's
// the source user's own Plex auth, not something the new user should
// inherit. Same reasoning for is_owner=false: toUserID is being granted
// access to the admin's already-configured servers, not becoming their
// Plex owner. AddUserServer auto-defaults the first insert, and
// GetUserServers already returns default-first, so the copy's default
// lands on the equivalent server with no extra call.
func CopyServerConfig(sqlDB *sql.DB, fromUserID, toUserID int64) error {
	servers, err := GetUserServers(sqlDB, fromUserID)
	if err != nil {
		return err
	}
	for _, src := range servers {
		if _, err := AddUserServer(sqlDB, toUserID, src.ServerName, src.ServerClientID, src.ServerURL, src.LibraryID.String, src.LibraryName.String, "", false); err != nil {
			return err
		}
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
