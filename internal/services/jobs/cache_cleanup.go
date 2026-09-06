package jobs

import (
	"database/sql"
	"log/slog"

	"github.com/drevilish/playlist-lab/internal/db"
)

const cacheMaxAgeDays = 7

// RunCacheCleanup ports cache-cleanup-job.ts: removes stale
// cached_playlists rows. SQLite's WAL mode doesn't need frequent
// vacuuming, so (matching the Node job) no VACUUM runs here.
func RunCacheCleanup(sqlDB *sql.DB) error {
	slog.Info("starting cache cleanup job")
	deleted, err := db.DeleteOldCache(sqlDB, cacheMaxAgeDays)
	if err != nil {
		return err
	}
	slog.Info("cache cleanup job completed", "deletedCount", deleted)
	return nil
}
