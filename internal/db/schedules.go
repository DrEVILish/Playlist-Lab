// schedules.go ports database.ts's schedule/schedule_executions operations
// (see routes/schedules.ts, services/schedule-checker-job.ts) to plain
// functions over *sql.DB, matching this package's existing convention (see
// cross_import.go, playlists.go) rather than reopening a DatabaseService
// class.
package db

import (
	"database/sql"
	"encoding/json"
	"time"
)

// Schedule mirrors the schedules table. Config is left as a raw JSON string
// (like Playlist.SourceURL and MixTemplate.Configuration elsewhere in this
// package) - callers that need typed access parse it with ScheduleConfig.
type Schedule struct {
	ID           int64
	UserID       int64
	PlaylistID   sql.NullInt64
	ScheduleType string // "playlist_refresh" or "mix_generation"
	Frequency    string // "daily", "weekly", "fortnightly", "monthly"
	StartDate    string // "YYYY-MM-DD"
	LastRun      sql.NullInt64
	Config       sql.NullString
	CreatedAt    int64
}

// ScheduleConfig is the parsed shape of Schedule.Config. RunTime is
// "HH:MM" or empty (any time). UpdateMode governs playlist_refresh
// schedules only; MixType/PlaylistName govern mix_generation schedules -
// see scheduler.ExecuteMixGeneration.
type ScheduleConfig struct {
	RunTime      string `json:"run_time,omitempty"`
	UpdateMode   string `json:"updateMode,omitempty"` // "replace" | "accumulate"
	MixType      string `json:"mixType,omitempty"`
	PlaylistName string `json:"playlistName,omitempty"`
}

func (s Schedule) ParsedConfig() ScheduleConfig {
	var cfg ScheduleConfig
	if s.Config.Valid {
		_ = json.Unmarshal([]byte(s.Config.String), &cfg)
	}
	return cfg
}

type ScheduleExecution struct {
	ID              int64
	ScheduleID      int64
	UserID          int64
	Status          string // "running", "success", "failed"
	StartedAt       int64
	CompletedAt     sql.NullInt64
	TracksMatched   int
	TracksUnmatched int
	ErrorMessage    sql.NullString
	PlaylistName    sql.NullString
}

const scheduleCols = "id, user_id, playlist_id, schedule_type, frequency, start_date, last_run, config, created_at"

func scanSchedule(row interface{ Scan(...any) error }) (*Schedule, error) {
	var s Schedule
	err := row.Scan(&s.ID, &s.UserID, &s.PlaylistID, &s.ScheduleType, &s.Frequency, &s.StartDate, &s.LastRun, &s.Config, &s.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateSchedule inserts a schedule row. playlistID of 0 stores NULL -
// unlike the TS version, this Go port only supports schedules linked to an
// already-imported playlist row (see scheduler package doc), so callers
// always pass a real playlist_id for playlist_refresh; mix_generation
// schedules pass 0 until their first run links one.
func CreateSchedule(sqlDB *sql.DB, userID, playlistID int64, scheduleType, frequency, startDate, configJSON string) (*Schedule, error) {
	now := time.Now().Unix()
	res, err := sqlDB.Exec(
		`INSERT INTO schedules (user_id, playlist_id, schedule_type, frequency, start_date, config, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, nullIfZero(playlistID), scheduleType, frequency, startDate, nullIfEmpty(configJSON), now,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetScheduleByID(sqlDB, id)
}

func GetScheduleByID(sqlDB *sql.DB, id int64) (*Schedule, error) {
	return scanSchedule(sqlDB.QueryRow("SELECT "+scheduleCols+" FROM schedules WHERE id = ?", id))
}

// GetScheduleByPlaylistID finds the (at most one) schedule tied to a given
// playlist - what the ScheduleModal.tsx port needs to decide between
// "Create Schedule" and "Manage Schedule" for a given playlist row.
func GetScheduleByPlaylistID(sqlDB *sql.DB, playlistID int64) (*Schedule, error) {
	return scanSchedule(sqlDB.QueryRow("SELECT "+scheduleCols+" FROM schedules WHERE playlist_id = ? LIMIT 1", playlistID))
}

func GetUserSchedules(sqlDB *sql.DB, userID int64) ([]Schedule, error) {
	rows, err := sqlDB.Query("SELECT "+scheduleCols+" FROM schedules WHERE user_id = ? ORDER BY created_at DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// GetDueSchedules returns every schedule belonging to an enabled user -
// scheduler.IsDue still has to be applied by the caller to filter down to
// schedules actually due right now (kept as a pure function there so it's
// unit-testable without a DB, see scheduler/scheduler_test.go).
func GetDueSchedules(sqlDB *sql.DB) ([]Schedule, error) {
	rows, err := sqlDB.Query(`
		SELECT schedules.id, schedules.user_id, schedules.playlist_id, schedules.schedule_type,
		       schedules.frequency, schedules.start_date, schedules.last_run, schedules.config, schedules.created_at
		FROM schedules
		JOIN users ON users.id = schedules.user_id
		WHERE users.is_enabled = 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

type ScheduleUpdate struct {
	Frequency *string
	StartDate *string
	Config    *string // raw JSON, nil means "don't touch"
}

func UpdateSchedule(sqlDB *sql.DB, id int64, u ScheduleUpdate) error {
	fields := []string{}
	values := []any{}
	if u.Frequency != nil {
		fields = append(fields, "frequency = ?")
		values = append(values, *u.Frequency)
	}
	if u.StartDate != nil {
		fields = append(fields, "start_date = ?")
		values = append(values, *u.StartDate)
	}
	if u.Config != nil {
		fields = append(fields, "config = ?")
		values = append(values, *u.Config)
	}
	if len(fields) == 0 {
		return nil
	}
	values = append(values, id)
	q := "UPDATE schedules SET "
	for i, f := range fields {
		if i > 0 {
			q += ", "
		}
		q += f
	}
	q += " WHERE id = ?"
	_, err := sqlDB.Exec(q, values...)
	return err
}

func UpdateScheduleLastRun(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("UPDATE schedules SET last_run = ? WHERE id = ?", time.Now().Unix(), id)
	return err
}

// LinkSchedulePlaylist points a schedule at the playlist row its first run
// resolved/created - only needed for mix_generation schedules, which have no
// playlist to link at creation time (see CreateSchedule's doc).
func LinkSchedulePlaylist(sqlDB *sql.DB, scheduleID, playlistID int64) error {
	_, err := sqlDB.Exec("UPDATE schedules SET playlist_id = ? WHERE id = ?", playlistID, scheduleID)
	return err
}

func DeleteSchedule(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("DELETE FROM schedules WHERE id = ?", id)
	return err
}

// ==================== Schedule Executions ====================

func CreateScheduleExecution(sqlDB *sql.DB, scheduleID, userID int64, playlistName string) (int64, error) {
	res, err := sqlDB.Exec(
		`INSERT INTO schedule_executions (schedule_id, user_id, status, started_at, playlist_name)
		 VALUES (?, ?, 'running', ?, ?)`,
		scheduleID, userID, time.Now().Unix(), nullIfEmpty(playlistName),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func CompleteScheduleExecution(sqlDB *sql.DB, executionID int64, status string, tracksMatched, tracksUnmatched int, errMsg string) error {
	_, err := sqlDB.Exec(
		`UPDATE schedule_executions SET status = ?, completed_at = ?, tracks_matched = ?, tracks_unmatched = ?, error_message = ? WHERE id = ?`,
		status, time.Now().Unix(), tracksMatched, tracksUnmatched, nullIfEmpty(errMsg), executionID,
	)
	return err
}

func GetScheduleExecutions(sqlDB *sql.DB, scheduleID int64, limit int) ([]ScheduleExecution, error) {
	rows, err := sqlDB.Query(
		`SELECT id, schedule_id, user_id, status, started_at, completed_at, tracks_matched, tracks_unmatched, error_message, playlist_name
		 FROM schedule_executions WHERE schedule_id = ? ORDER BY started_at DESC LIMIT ?`,
		scheduleID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanExecutions(rows)
}

// GetUserRecentExecutions returns a user's most recent schedule executions
// across all their schedules, newest first - powers the Status page's
// succeeded/failed tally (mirrors StatusReportsModal.tsx's
// apiClient.getRecentExecutions(100), previously GET /executions/recent).
func GetUserRecentExecutions(sqlDB *sql.DB, userID int64, limit int) ([]ScheduleExecution, error) {
	rows, err := sqlDB.Query(
		`SELECT id, schedule_id, user_id, status, started_at, completed_at, tracks_matched, tracks_unmatched, error_message, playlist_name
		 FROM schedule_executions WHERE user_id = ? ORDER BY started_at DESC LIMIT ?`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanExecutions(rows)
}

// LatestExecution is a schedule's most recent run: its outcome and when it
// started. The Playlists page needs both - the status drives "Needs
// Attention" (per the React source's needsAttention(), a schedule whose last
// run failed counts alongside playlists with missing tracks) and the
// timestamp fills its "Last Run" column.
type LatestExecution struct {
	Status    string
	StartedAt int64
}

// GetLatestExecutionStatuses returns each of a user's schedules' most
// recent execution, keyed by schedule ID.
func GetLatestExecutionStatuses(sqlDB *sql.DB, userID int64) (map[int64]LatestExecution, error) {
	rows, err := sqlDB.Query(
		`SELECT schedule_id, status, started_at FROM schedule_executions
		 WHERE user_id = ? AND id IN (
		   SELECT MAX(id) FROM schedule_executions WHERE user_id = ? GROUP BY schedule_id
		 )`,
		userID, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]LatestExecution)
	for rows.Next() {
		var scheduleID int64
		var e LatestExecution
		// started_at is nullable for a run that never recorded a start.
		var startedAt sql.NullInt64
		if err := rows.Scan(&scheduleID, &e.Status, &startedAt); err != nil {
			return nil, err
		}
		e.StartedAt = startedAt.Int64
		out[scheduleID] = e
	}
	return out, rows.Err()
}

func scanExecutions(rows *sql.Rows) ([]ScheduleExecution, error) {
	var out []ScheduleExecution
	for rows.Next() {
		var e ScheduleExecution
		if err := rows.Scan(&e.ID, &e.ScheduleID, &e.UserID, &e.Status, &e.StartedAt, &e.CompletedAt, &e.TracksMatched, &e.TracksUnmatched, &e.ErrorMessage, &e.PlaylistName); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
