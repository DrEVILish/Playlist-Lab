package db

import (
	"database/sql"
	"time"
)

// dedupeMissingCollectionItems drops later rows with the same GuidKey as an
// earlier one - mirrors missing_tracks.go's dedupeMissingTracks. A
// provider's fetched list can itself contain the same external id twice
// (e.g. two different Letterboxd film pages resolving to the same TMDb
// id), and without this every duplicate would violate this table's
// UNIQUE(collection_id, guid_key) constraint, failing the whole batch.
func dedupeMissingCollectionItems(items []NewMissingCollectionItem) []NewMissingCollectionItem {
	seen := make(map[string]bool, len(items))
	out := make([]NewMissingCollectionItem, 0, len(items))
	for _, it := range items {
		if seen[it.GuidKey] {
			continue
		}
		seen[it.GuidKey] = true
		out = append(out, it)
	}
	return out
}

// MissingCollectionItem mirrors missing_tracks.go's MissingTrack, scoped to
// an external-list-sourced Collection instead of an imported playlist
// (DESIGN.md §11.11): one list/chart entry that didn't match anything in
// the target Plex library on the collection's last refresh. GuidKey is the
// provider-prefixed id (e.g. "tmdb://11974", "imdb://tt0096734") - see
// internal/services/medialist.
type MissingCollectionItem struct {
	ID           int64
	CollectionID int64
	UserID       int64
	GuidKey      string
	MediaType    string // "movie" | "tv"
	Title        string
	Year         sql.NullInt64
	AddedAt      int64
}

// NewMissingCollectionItem is one row's worth of input to
// ReplaceMissingCollectionItems.
type NewMissingCollectionItem struct {
	GuidKey   string
	MediaType string
	Title     string
	Year      int
}

// ReplaceMissingCollectionItems replaces one collection's missing-item rows
// with the current refresh run's complete unmatched set - same full-
// replace-per-run convention as AddMissingTracks (missing_tracks.go), so a
// title that's still missing gets its added_at refreshed rather than
// duplicated, and one that's no longer missing (now in the library, or
// dropped from the source list) disappears on its own.
func ReplaceMissingCollectionItems(sqlDB *sql.DB, userID, collectionID int64, items []NewMissingCollectionItem) error {
	items = dedupeMissingCollectionItems(items)

	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM missing_collection_items WHERE collection_id = ?`, collectionID); err != nil {
		return err
	}
	if len(items) == 0 {
		return tx.Commit()
	}

	now := time.Now().Unix()
	stmt, err := tx.Prepare(
		`INSERT INTO missing_collection_items (collection_id, user_id, guid_key, media_type, title, year, added_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, it := range items {
		var year any
		if it.Year > 0 {
			year = it.Year
		}
		if _, err := stmt.Exec(collectionID, userID, it.GuidKey, it.MediaType, it.Title, year, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func GetUserMissingCollectionItems(sqlDB *sql.DB, userID int64) ([]MissingCollectionItem, error) {
	rows, err := sqlDB.Query(
		`SELECT id, collection_id, user_id, guid_key, media_type, title, year, added_at
		 FROM missing_collection_items WHERE user_id = ? ORDER BY collection_id, title`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MissingCollectionItem
	for rows.Next() {
		var m MissingCollectionItem
		if err := rows.Scan(&m.ID, &m.CollectionID, &m.UserID, &m.GuidKey, &m.MediaType, &m.Title, &m.Year, &m.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func RemoveMissingCollectionItem(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("DELETE FROM missing_collection_items WHERE id = ?", id)
	return err
}
