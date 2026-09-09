package db

import (
	"database/sql"
	"time"
)

// MissingTrack mirrors database/types.ts's MissingTrack: one source track
// that couldn't be matched (or was explicitly removed) during an import,
// kept around so the user can retry matching, manually rematch, or send it
// off to deemix/Lidarr for acquisition.
type MissingTrack struct {
	ID            int64
	UserID        int64
	PlaylistID    int64
	Title         string
	Artist        string
	Album         sql.NullString
	Position      int
	AfterTrackKey sql.NullString
	AddedAt       int64
	Source        string
}

// NewMissingTrack is one row's worth of input to AddMissingTracks - mirrors
// missing.ts's missingTracksInput mapping.
type NewMissingTrack struct {
	Title         string
	Artist        string
	Album         string
	Position      int
	AfterTrackKey string
	Source        string
}

// GetMissingTrackCount is the total across every user, for the admin Stats
// tab (mirrors admin.ts's db.getAllMissingTracks().length).
func GetMissingTrackCount(sqlDB *sql.DB) (int, error) {
	var count int
	err := sqlDB.QueryRow("SELECT COUNT(*) FROM missing_tracks").Scan(&count)
	return count, err
}

func GetUserMissingTracks(sqlDB *sql.DB, userID int64) ([]MissingTrack, error) {
	rows, err := sqlDB.Query(
		`SELECT id, user_id, playlist_id, title, artist, album, position, after_track_key, added_at, source
		 FROM missing_tracks WHERE user_id = ? ORDER BY playlist_id, position`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MissingTrack
	for rows.Next() {
		var t MissingTrack
		if err := rows.Scan(&t.ID, &t.UserID, &t.PlaylistID, &t.Title, &t.Artist, &t.Album, &t.Position, &t.AfterTrackKey, &t.AddedAt, &t.Source); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddMissingTracks replaces one playlist's missing-track rows with a fresh
// batch. Both callers (a scheduled refresh, a first import) pass the
// complete current unmatched set for that playlist run, not an incremental
// delta - inserting on top of whatever the previous run left behind (the
// original Node port's behavior) duplicated every still-missing track once
// per run instead of just updating its added_at, which is what actually
// surfaced as literal duplicate rows in the missing-tracks list once it
// became directly visible in the Playlists table's inline expando.
func AddMissingTracks(sqlDB *sql.DB, userID, playlistID int64, tracks []NewMissingTrack) error {
	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM missing_tracks WHERE playlist_id = ?`, playlistID); err != nil {
		return err
	}
	if len(tracks) == 0 {
		return tx.Commit()
	}

	now := time.Now().Unix()
	stmt, err := tx.Prepare(
		`INSERT INTO missing_tracks (user_id, playlist_id, title, artist, album, position, after_track_key, added_at, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, t := range tracks {
		if _, err := stmt.Exec(userID, playlistID, t.Title, t.Artist, nullIfEmpty(t.Album), t.Position, nullIfEmpty(t.AfterTrackKey), now, t.Source); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MissingTrackStat mirrors database.ts's getMissingTrackStats() row: one
// title+artist aggregated across every user's missing tracks.
type MissingTrackStat struct {
	Title, Artist string
	Count         int
	AddedAt       int64
}

// GetMissingTrackStats groups missing tracks by normalized (case/whitespace
// -insensitive) title+artist, same dedup key as the Node version, for the
// admin "Most Common Missing Tracks" tab.
func GetMissingTrackStats(sqlDB *sql.DB) ([]MissingTrackStat, error) {
	rows, err := sqlDB.Query(`
		SELECT MIN(title) as title, MIN(artist) as artist, COUNT(*) as count, MAX(added_at) as added_at
		FROM missing_tracks
		GROUP BY LOWER(TRIM(title)), LOWER(TRIM(artist))
		ORDER BY count DESC
		LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MissingTrackStat
	for rows.Next() {
		var s MissingTrackStat
		if err := rows.Scan(&s.Title, &s.Artist, &s.Count, &s.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func RemoveMissingTrack(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("DELETE FROM missing_tracks WHERE id = ?", id)
	return err
}

func ClearPlaylistMissingTracks(sqlDB *sql.DB, playlistID int64) error {
	_, err := sqlDB.Exec("DELETE FROM missing_tracks WHERE playlist_id = ?", playlistID)
	return err
}
