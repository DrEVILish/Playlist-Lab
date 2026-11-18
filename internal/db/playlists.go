package db

import (
	"database/sql"
	"time"
)

type Playlist struct {
	ID             int64
	UserID         int64
	PlexPlaylistID string
	ServerClientID sql.NullString
	Name           string
	Source         string
	SourceURL      sql.NullString
	CreatedAt      int64
	UpdatedAt      int64
}

const playlistCols = "id, user_id, plex_playlist_id, server_client_id, name, source, source_url, created_at, updated_at"

func CreatePlaylistRow(sqlDB *sql.DB, userID int64, plexPlaylistID, name, source, sourceURL string) (*Playlist, error) {
	return CreatePlaylistRowForServer(sqlDB, userID, "", plexPlaylistID, name, source, sourceURL)
}

func CreatePlaylistRowForServer(sqlDB *sql.DB, userID int64, serverClientID, plexPlaylistID, name, source, sourceURL string) (*Playlist, error) {
	now := time.Now().Unix()
	res, err := sqlDB.Exec(
		`INSERT INTO playlists (user_id, plex_playlist_id, server_client_id, name, source, source_url, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, plexPlaylistID, nullIfEmpty(serverClientID), name, source, nullIfEmpty(sourceURL), now, now,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetPlaylistByID(sqlDB, id)
}

func GetUserPlaylists(sqlDB *sql.DB, userID int64) ([]Playlist, error) {
	return getPlaylists(sqlDB, "WHERE user_id = ?", userID)
}

// Leave ambiguous legacy rows unassigned when there is more than one music server.
const playlistServerCondition = `(server_client_id = ? OR (
	server_client_id IS NULL AND (
		SELECT COUNT(DISTINCT server_client_id) FROM user_servers
		WHERE user_id = playlists.user_id AND library_id IS NOT NULL
	) = 1
))`

func GetUserPlaylistsForServer(sqlDB *sql.DB, userID int64, serverClientID string) ([]Playlist, error) {
	return getPlaylists(sqlDB, "WHERE user_id = ? AND "+playlistServerCondition, userID, serverClientID)
}

func getPlaylists(sqlDB *sql.DB, filter string, args ...any) ([]Playlist, error) {
	rows, err := sqlDB.Query("SELECT "+playlistCols+" FROM playlists "+filter+" ORDER BY created_at DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Playlist
	for rows.Next() {
		var p Playlist
		if err := scanPlaylistFields(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func GetPlaylistByID(sqlDB *sql.DB, id int64) (*Playlist, error) {
	return scanPlaylist(sqlDB.QueryRow(
		"SELECT "+playlistCols+" FROM playlists WHERE id = ?", id,
	))
}

func GetPlaylistByPlexID(sqlDB *sql.DB, userID int64, plexPlaylistID string) (*Playlist, error) {
	return scanPlaylist(sqlDB.QueryRow(
		"SELECT "+playlistCols+" FROM playlists WHERE user_id = ? AND plex_playlist_id = ?", userID, plexPlaylistID,
	))
}

func GetPlaylistByPlexIDForServer(sqlDB *sql.DB, userID int64, serverClientID, plexPlaylistID string) (*Playlist, error) {
	return scanPlaylist(sqlDB.QueryRow("SELECT "+playlistCols+
		" FROM playlists WHERE user_id = ? AND plex_playlist_id = ? AND "+playlistServerCondition+
		" ORDER BY server_client_id IS NULL, id DESC LIMIT 1", userID, plexPlaylistID, serverClientID))
}

func BindPlaylistServer(sqlDB *sql.DB, playlistID int64, serverClientID string) error {
	_, err := sqlDB.Exec("UPDATE playlists SET server_client_id = ? WHERE id = ? AND server_client_id IS NULL", serverClientID, playlistID)
	return err
}

func scanPlaylist(row *sql.Row) (*Playlist, error) {
	var p Playlist
	err := scanPlaylistFields(row, &p)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func scanPlaylistFields(row interface{ Scan(...any) error }, p *Playlist) error {
	return row.Scan(&p.ID, &p.UserID, &p.PlexPlaylistID, &p.ServerClientID, &p.Name, &p.Source, &p.SourceURL, &p.CreatedAt, &p.UpdatedAt)
}

// UpdatePlaylistPlexID sets the real Plex playlist id once one has been
// created for a row that started out as a "pending-..." placeholder (see
// matching.InsertMatchedTrackIntoPlaylist).
func UpdatePlaylistPlexID(sqlDB *sql.DB, id int64, plexPlaylistID string) error {
	_, err := sqlDB.Exec("UPDATE playlists SET plex_playlist_id = ?, updated_at = ? WHERE id = ?", plexPlaylistID, time.Now().Unix(), id)
	return err
}

func TouchPlaylist(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("UPDATE playlists SET updated_at = ? WHERE id = ?", time.Now().Unix(), id)
	return err
}

// RenamePlaylistRow updates this app's own record of a tracked playlist's
// name after the caller has already renamed it in Plex itself - the two
// must be kept in sync or this app's name (shown on the home page) drifts
// from what every real Plex client displays.
func RenamePlaylistRow(sqlDB *sql.DB, id int64, name string) error {
	_, err := sqlDB.Exec("UPDATE playlists SET name = ?, updated_at = ? WHERE id = ?", name, time.Now().Unix(), id)
	return err
}

func DeletePlaylistRow(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("DELETE FROM playlists WHERE id = ?", id)
	return err
}

// GetPlaylistCount is the total-playlists-across-all-users figure for the
// admin Stats tab (mirrors admin.ts's db.getPlaylistCount()).
func GetPlaylistCount(sqlDB *sql.DB) (int, error) {
	var count int
	err := sqlDB.QueryRow("SELECT COUNT(*) FROM playlists").Scan(&count)
	return count, err
}
