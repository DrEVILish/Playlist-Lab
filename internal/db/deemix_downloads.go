package db

import (
	"database/sql"
	"time"
)

// DeemixDownload is one in-flight deemix-server queue item this server is
// tracking, so its progress poller can be resumed after a restart (see
// services/deemix.ts's resumeDeemixDownloads / schema.sql's comment on this
// table for why it exists at all).
type DeemixDownload struct {
	ID             int64
	UserID         int64
	UUID           string
	MissingTrackID sql.NullInt64
	Title          string
	Detail         sql.NullString
	CreatedAt      int64
}

func AddDeemixDownload(sqlDB *sql.DB, userID int64, uuid, title, detail string, missingTrackID *int64) (int64, error) {
	var mtID any
	if missingTrackID != nil {
		mtID = *missingTrackID
	}
	res, err := sqlDB.Exec(
		`INSERT INTO deemix_downloads (user_id, uuid, missing_track_id, title, detail, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		userID, uuid, mtID, title, nullIfEmpty(detail), time.Now().UnixMilli(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetActiveDeemixDownloads returns every row newer than maxAge, for
// resuming pollers on startup.
func GetActiveDeemixDownloads(sqlDB *sql.DB, maxAge time.Duration) ([]DeemixDownload, error) {
	cutoff := time.Now().Add(-maxAge).UnixMilli()
	rows, err := sqlDB.Query(
		`SELECT id, user_id, uuid, missing_track_id, title, detail, created_at
		 FROM deemix_downloads WHERE created_at >= ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeemixDownload
	for rows.Next() {
		var d DeemixDownload
		if err := rows.Scan(&d.ID, &d.UserID, &d.UUID, &d.MissingTrackID, &d.Title, &d.Detail, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func DeleteDeemixDownload(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("DELETE FROM deemix_downloads WHERE id = ?", id)
	return err
}
