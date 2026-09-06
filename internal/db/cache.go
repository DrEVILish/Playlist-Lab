package db

import (
	"database/sql"
	"encoding/json"
	"time"
)

// DeleteOldCache removes cached_playlists rows scraped more than maxAgeDays
// ago, returning the number of rows deleted.
func DeleteOldCache(sqlDB *sql.DB, maxAgeDays int) (int64, error) {
	cutoff := time.Now().Unix() - int64(maxAgeDays)*86400
	res, err := sqlDB.Exec("DELETE FROM cached_playlists WHERE scraped_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CachedTrack mirrors one entry of cached_playlists.tracks's JSON array -
// import.ts's ExternalPlaylist['tracks'] shape.
type CachedTrack struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
}

// CachedPlaylist mirrors database.ts's getCachedPlaylist row, used by the
// import service as a fallback when a fresh scrape fails.
type CachedPlaylist struct {
	Source      string
	SourceID    string
	Name        string
	Description sql.NullString
	Tracks      []CachedTrack
	CoverURL    sql.NullString
	ScrapedAt   int64
}

// GetCachedPlaylist ports database.ts's getCachedPlaylist. Returns nil, nil
// if there is no cache row for this source/sourceID.
func GetCachedPlaylist(sqlDB *sql.DB, source, sourceID string) (*CachedPlaylist, error) {
	var c CachedPlaylist
	var tracksJSON string
	err := sqlDB.QueryRow(
		`SELECT source, source_id, name, description, tracks, cover_url, scraped_at
		 FROM cached_playlists WHERE source = ? AND source_id = ?`, source, sourceID,
	).Scan(&c.Source, &c.SourceID, &c.Name, &c.Description, &tracksJSON, &c.CoverURL, &c.ScrapedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(tracksJSON), &c.Tracks); err != nil {
		return nil, err
	}
	return &c, nil
}

// SaveCachedPlaylist upserts a scraped playlist's data into cached_playlists,
// ports database.ts's saveCachedPlaylist.
func SaveCachedPlaylist(sqlDB *sql.DB, source, sourceID, name, description string, tracks []CachedTrack, coverURL string) error {
	tracksJSON, err := json.Marshal(tracks)
	if err != nil {
		return err
	}
	_, err = sqlDB.Exec(
		`INSERT INTO cached_playlists (source, source_id, name, description, tracks, cover_url, scraped_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(source, source_id) DO UPDATE SET
		   name = excluded.name, description = excluded.description,
		   tracks = excluded.tracks, cover_url = excluded.cover_url, scraped_at = excluded.scraped_at`,
		source, sourceID, name, nullIfEmpty(description), string(tracksJSON), nullIfEmpty(coverURL), time.Now().Unix(),
	)
	return err
}
