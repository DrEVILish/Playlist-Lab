package db

import (
	"database/sql"
	"time"
)

// CrossImportJob is one row of cross_import_jobs, ported from
// routes/cross-import.ts's job bookkeeping (insert on POST /match, update on
// completion, list on GET /history).
type CrossImportJob struct {
	ID                  int64
	UserID              int64
	SourceService       string
	SourcePlaylistName  string
	TargetService       string
	TargetPlaylistName  sql.NullString
	MatchedCount        int
	UnmatchedCount      int
	SkippedCount        int
	TotalCount          int
	Status              string
	UnmatchedTracksJSON sql.NullString
	CreatedAt           int64
	CompletedAt         sql.NullInt64
}

func CreateCrossImportJob(sqlDB *sql.DB, userID int64, sourceService, sourcePlaylistName, targetService string) (int64, error) {
	res, err := sqlDB.Exec(
		`INSERT INTO cross_import_jobs (user_id, source_service, source_playlist_name, target_service, status, total_count, created_at)
		 VALUES (?, ?, ?, ?, 'matching', 0, ?)`,
		userID, sourceService, sourcePlaylistName, targetService, time.Now().UnixMilli(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateCrossImportJobPlaylist(sqlDB *sql.DB, jobID int64, playlistName string, totalCount int) error {
	_, err := sqlDB.Exec(
		`UPDATE cross_import_jobs SET source_playlist_name = ?, total_count = ? WHERE id = ?`,
		playlistName, totalCount, jobID,
	)
	return err
}

func UpdateCrossImportJobStatus(sqlDB *sql.DB, jobID int64, status string) error {
	_, err := sqlDB.Exec(`UPDATE cross_import_jobs SET status = ? WHERE id = ?`, status, jobID)
	return err
}

func DeleteCrossImportJob(sqlDB *sql.DB, jobID int64) error {
	_, err := sqlDB.Exec(`DELETE FROM cross_import_jobs WHERE id = ?`, jobID)
	return err
}

func CompleteCrossImportJob(sqlDB *sql.DB, jobID, userID int64, targetPlaylistName string, matched, unmatched, skipped, total int, unmatchedTracksJSON string) error {
	_, err := sqlDB.Exec(
		`UPDATE cross_import_jobs SET
		   status = 'complete', target_playlist_name = ?, matched_count = ?, unmatched_count = ?,
		   skipped_count = ?, total_count = ?, unmatched_tracks = ?, completed_at = ?
		 WHERE id = ? AND user_id = ?`,
		targetPlaylistName, matched, unmatched, skipped, total, unmatchedTracksJSON, time.Now().UnixMilli(), jobID, userID,
	)
	return err
}

func GetCrossImportJobs(sqlDB *sql.DB, userID int64) ([]CrossImportJob, error) {
	rows, err := sqlDB.Query(
		`SELECT id, user_id, source_service, source_playlist_name, target_service, target_playlist_name,
		        matched_count, unmatched_count, skipped_count, total_count, status, unmatched_tracks, created_at, completed_at
		 FROM cross_import_jobs WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CrossImportJob
	for rows.Next() {
		var j CrossImportJob
		if err := rows.Scan(&j.ID, &j.UserID, &j.SourceService, &j.SourcePlaylistName, &j.TargetService, &j.TargetPlaylistName,
			&j.MatchedCount, &j.UnmatchedCount, &j.SkippedCount, &j.TotalCount, &j.Status, &j.UnmatchedTracksJSON, &j.CreatedAt, &j.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
