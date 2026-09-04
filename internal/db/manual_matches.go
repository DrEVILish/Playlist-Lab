package db

import (
	"database/sql"
	"time"
)

type ManualMatch struct {
	Title         string
	Artist        string
	Album         sql.NullString
	PlexRatingKey string
}

func GetUserManualMatches(sqlDB *sql.DB, userID int64) ([]ManualMatch, error) {
	rows, err := sqlDB.Query("SELECT title, artist, album, plex_rating_key FROM manual_matches WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManualMatch
	for rows.Next() {
		var m ManualMatch
		if err := rows.Scan(&m.Title, &m.Artist, &m.Album, &m.PlexRatingKey); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RecordManualMatch remembers a user's explicit (or automatically learned)
// "use this Plex track" choice for a source track, keyed on
// (title, artist, album) - see matching.RememberedMatchKey, which must stay
// in agreement with this lookup. There's no UNIQUE constraint on that key in
// schema.sql (an ON CONFLICT upsert isn't available), so this finds any
// existing row by the same case/whitespace-insensitive lookup database.ts's
// recordManualMatch uses and updates it, only inserting when none exists.
func RecordManualMatch(sqlDB *sql.DB, userID int64, title, artist, album, plexRatingKey string) error {
	var existingID int64
	err := sqlDB.QueryRow(
		`SELECT id FROM manual_matches
		 WHERE user_id = ?
		   AND LOWER(TRIM(title)) = LOWER(TRIM(?))
		   AND LOWER(TRIM(artist)) = LOWER(TRIM(?))
		   AND LOWER(TRIM(COALESCE(album, ''))) = LOWER(TRIM(COALESCE(?, '')))`,
		userID, title, artist, album,
	).Scan(&existingID)

	now := time.Now().Unix()
	if err == nil {
		_, err = sqlDB.Exec("UPDATE manual_matches SET plex_rating_key = ?, updated_at = ? WHERE id = ?", plexRatingKey, now, existingID)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = sqlDB.Exec(
		`INSERT INTO manual_matches (user_id, title, artist, album, plex_rating_key, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, title, artist, nullIfEmpty(album), plexRatingKey, now, now,
	)
	return err
}
