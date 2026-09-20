// admin_stats.go: daily trend buckets for the Admin dashboard's stat-tile
// sparklines and its one larger trend chart (DESIGN.md §11.5, §8.10). Every
// bucket here groups a timestamp column that already exists for its own
// reason (signup time, playlist creation, missing-track discovery, schedule
// run history) - nothing new is tracked just to feed a chart.
package db

import (
	"database/sql"
	"time"
)

// DayCount is one point in a daily trend series: an ISO date and its count.
type DayCount struct {
	Day   string
	Count int
}

// dailyCounts buckets rows in table by calendar day over the last `days`
// days (today inclusive), oldest first, zero-filling days with no rows so
// sparkline/chart rendering never has to special-case gaps. millis selects
// whether timeCol is stored in unix millis (users.created_at/last_login) or
// unix seconds (everything else in this schema).
func dailyCounts(sqlDB *sql.DB, table, timeCol string, millis bool, days int) ([]DayCount, error) {
	startOfDay := dayBucketStart(days)
	since := startOfDay.Unix()
	divisor := "1"
	if millis {
		since = startOfDay.UnixMilli()
		divisor = "1000"
	}

	rows, err := sqlDB.Query(
		`SELECT date(`+timeCol+`/`+divisor+`, 'unixepoch') as day, COUNT(*)
		 FROM `+table+` WHERE `+timeCol+` >= ? GROUP BY day`,
		since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byDay := make(map[string]int)
	for rows.Next() {
		var day string
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		byDay[day] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return fillDays(startOfDay, days, func(key string) DayCount {
		return DayCount{Day: key, Count: byDay[key]}
	}), nil
}

// dayBucketStart is midnight, `days` days ago (today counts as one of them).
func dayBucketStart(days int) time.Time {
	start := time.Now().AddDate(0, 0, -(days - 1))
	return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())
}

func fillDays[T any](start time.Time, days int, at func(dayKey string) T) []T {
	out := make([]T, days)
	for i := 0; i < days; i++ {
		out[i] = at(start.AddDate(0, 0, i).Format("2006-01-02"))
	}
	return out
}

// GetUserSignupsByDay is the Total Users stat tile's sparkline: new
// accounts created per day.
func GetUserSignupsByDay(sqlDB *sql.DB, days int) ([]DayCount, error) {
	return dailyCounts(sqlDB, "users", "created_at", true, days)
}

// GetActiveUsersByDay is the Active Users stat tile's sparkline: how many
// users' most recent login falls on each day - a distribution of the
// existing last_login column, not a separate login-history log.
func GetActiveUsersByDay(sqlDB *sql.DB, days int) ([]DayCount, error) {
	return dailyCounts(sqlDB, "users", "last_login", true, days)
}

// GetPlaylistsCreatedByDay is the Playlists Tracked stat tile's sparkline.
func GetPlaylistsCreatedByDay(sqlDB *sql.DB, days int) ([]DayCount, error) {
	return dailyCounts(sqlDB, "playlists", "created_at", false, days)
}

// GetMissingTracksAddedByDay is the Missing Tracks stat tile's sparkline.
func GetMissingTracksAddedByDay(sqlDB *sql.DB, days int) ([]DayCount, error) {
	return dailyCounts(sqlDB, "missing_tracks", "added_at", false, days)
}

// ScheduleExecutionTrend is one day's sync activity: how many schedule runs
// succeeded vs failed. Backs the dashboard's one larger trend chart
// (DESIGN.md §11.5/§8.10) - the closest existing proxy for the "active
// syncs"/"error count" trend the spec calls out, without inventing new
// tracking beyond schedule_executions, which the scheduler already writes.
type ScheduleExecutionTrend struct {
	Day       string
	Succeeded int
	Failed    int
}

// GetScheduleExecutionTrend buckets schedule_executions by day and status
// over the last `days` days, zero-filled, oldest first.
func GetScheduleExecutionTrend(sqlDB *sql.DB, days int) ([]ScheduleExecutionTrend, error) {
	startOfDay := dayBucketStart(days)

	rows, err := sqlDB.Query(
		`SELECT date(started_at, 'unixepoch') as day, status, COUNT(*)
		 FROM schedule_executions WHERE started_at >= ? GROUP BY day, status`,
		startOfDay.Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	succeeded := make(map[string]int)
	failed := make(map[string]int)
	for rows.Next() {
		var day, status string
		var count int
		if err := rows.Scan(&day, &status, &count); err != nil {
			return nil, err
		}
		switch status {
		case "success":
			succeeded[day] = count
		case "failed":
			failed[day] = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return fillDays(startOfDay, days, func(key string) ScheduleExecutionTrend {
		return ScheduleExecutionTrend{Day: key, Succeeded: succeeded[key], Failed: failed[key]}
	}), nil
}
