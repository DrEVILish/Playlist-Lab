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

// recordManualMatchTx is RecordManualMatch's actual lookup-then-write,
// against whatever transaction the caller already has open - factored out
// so RecordManualMatch (one match) and RecordManualMatches (many, one
// shared transaction) share the exact same logic instead of drifting.
func recordManualMatchTx(tx *sql.Tx, userID int64, title, artist, album, plexRatingKey string, now int64) error {
	var existingID int64
	err := tx.QueryRow(
		`SELECT id FROM manual_matches
		 WHERE user_id = ?
		   AND LOWER(TRIM(title)) = LOWER(TRIM(?))
		   AND LOWER(TRIM(artist)) = LOWER(TRIM(?))
		   AND LOWER(TRIM(COALESCE(album, ''))) = LOWER(TRIM(COALESCE(?, '')))`,
		userID, title, artist, album,
	).Scan(&existingID)

	if err == nil {
		_, err := tx.Exec("UPDATE manual_matches SET plex_rating_key = ?, updated_at = ? WHERE id = ?", plexRatingKey, now, existingID)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO manual_matches (user_id, title, artist, album, plex_rating_key, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, title, artist, nullIfEmpty(album), plexRatingKey, now, now,
	)
	return err
}

// RecordManualMatch remembers a user's explicit (or automatically learned)
// "use this Plex track" choice for a source track, keyed on
// (title, artist, album) - see matching.RememberedMatchKey, which must stay
// in agreement with this lookup. There's no UNIQUE constraint on that key in
// schema.sql (an ON CONFLICT upsert isn't available), so this finds any
// existing row by the same case/whitespace-insensitive lookup database.ts's
// recordManualMatch uses and updates it, only inserting when none exists.
// The lookup-then-write runs inside one transaction (rather than two bare
// sqlDB calls) so two concurrent calls for the same key can't both see "no
// existing row" and both insert a duplicate.
func RecordManualMatch(sqlDB *sql.DB, userID int64, title, artist, album, plexRatingKey string) error {
	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recordManualMatchTx(tx, userID, title, artist, album, plexRatingKey, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// ManualMatchInput is one row for RecordManualMatches.
type ManualMatchInput struct {
	Title, Artist, Album, PlexRatingKey string
}

// RecordManualMatches is RecordManualMatch batched into a single
// transaction - matching.RememberMatches calls this once per matching run
// instead of calling RecordManualMatch once per track, so remembering a
// large playlist's worth of matches does one BEGIN/COMMIT (one fsync)
// instead of one per track.
func RecordManualMatches(sqlDB *sql.DB, userID int64, matches []ManualMatchInput) error {
	if len(matches) == 0 {
		return nil
	}
	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	for _, m := range matches {
		if err := recordManualMatchTx(tx, userID, m.Title, m.Artist, m.Album, m.PlexRatingKey, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
