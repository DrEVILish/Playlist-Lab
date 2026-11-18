package db

import (
	"database/sql"
	"strings"
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

// AddMissingTracks synchronizes one playlist's complete unmatched set.
// Downloads in progress refer to existing missing-track IDs.
func AddMissingTracks(sqlDB *sql.DB, userID, playlistID int64, tracks []NewMissingTrack) error {
	tracks = dedupeMissingTracks(tracks)

	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	existing, err := tx.Query(`SELECT id, title, artist FROM missing_tracks WHERE playlist_id = ? AND user_id = ? ORDER BY id`, playlistID, userID)
	if err != nil {
		return err
	}
	defer existing.Close()
	ids := make(map[string]int64)
	var stale []int64
	for existing.Next() {
		var id int64
		var title, artist string
		if err := existing.Scan(&id, &title, &artist); err != nil {
			return err
		}
		key := missingTrackKey(title, artist)
		if _, found := ids[key]; found {
			stale = append(stale, id)
		} else {
			ids[key] = id
		}
	}
	if err := existing.Err(); err != nil {
		return err
	}

	now := time.Now().Unix()
	for _, t := range tracks {
		key := missingTrackKey(t.Title, t.Artist)
		if _, err := tx.Exec(
			`INSERT INTO missing_tracks (id, user_id, playlist_id, title, artist, album, position, after_track_key, added_at, source)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET title = excluded.title, artist = excluded.artist,
			 album = excluded.album, position = excluded.position, after_track_key = excluded.after_track_key, source = excluded.source`,
			nullIfZero(ids[key]), userID, playlistID, t.Title, t.Artist, nullIfEmpty(t.Album), t.Position, nullIfEmpty(t.AfterTrackKey), now, t.Source,
		); err != nil {
			return err
		}
		delete(ids, key)
	}
	for _, id := range ids {
		stale = append(stale, id)
	}
	for _, id := range stale {
		if _, err := tx.Exec("DELETE FROM missing_tracks WHERE id = ?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// dedupeMissingTracks drops later rows with the same normalized (case/
// whitespace-insensitive) title+artist as an earlier one. The source track
// list a playlist is matched against can itself list the same track more
// than once (a scrape returning a repeated entry, a retry re-appending
// tracks that were already unmatched); MatchPlaylist has no reason to merge
// those, so without this every still-unmatched duplicate landed in
// missing_tracks verbatim and rendered as 2-4 identical rows in a row.
func dedupeMissingTracks(tracks []NewMissingTrack) []NewMissingTrack {
	seen := make(map[string]bool, len(tracks))
	out := make([]NewMissingTrack, 0, len(tracks))
	for _, t := range tracks {
		key := missingTrackKey(t.Title, t.Artist)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

func missingTrackKey(title, artist string) string {
	return strings.ToLower(strings.TrimSpace(title)) + "\x00" + strings.ToLower(strings.TrimSpace(artist))
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
