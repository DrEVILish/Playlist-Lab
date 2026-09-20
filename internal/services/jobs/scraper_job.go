package jobs

import (
	"database/sql"
	"log/slog"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/scrapers"
)

// scraperSupportedCountries ports scraper-job.ts's SUPPORTED_COUNTRIES.
var scraperSupportedCountries = []string{"global", "us", "gb", "au", "ca", "de", "fr", "es", "br", "jp"}

// RunDailyScraper ports scraper-job.ts's runDailyScraperJob: scrapes Deezer
// charts for every supported country plus ARIA charts, caching results into
// cached_playlists so imports of popular playlists don't wait on a live
// scrape. Node's comment on the other platforms still applies: Spotify/
// Apple Music/Tidal/YouTube Music/Amazon Music/Qobuz charts need either
// official APIs or JS rendering and aren't part of this bulk job - they're
// scraped on demand per-playlist instead (see internal/adapters/apple,
// internal/handlers/charts.go).
func RunDailyScraper(sqlDB *sql.DB) error {
	slog.Info("starting daily scraper job")
	var scraped, failed int

	for _, country := range scraperSupportedCountries {
		for _, pl := range scrapers.DeezerCharts(country) {
			if err := saveScraped(sqlDB, pl); err != nil {
				slog.Error("failed to cache playlist", "playlist", pl.Name, "error", err)
				failed++
				continue
			}
			scraped++
		}
	}

	for _, pl := range scrapers.AriaCharts() {
		if err := saveScraped(sqlDB, pl); err != nil {
			slog.Error("failed to cache playlist", "playlist", pl.Name, "error", err)
			failed++
			continue
		}
		scraped++
	}

	slog.Info("daily scraper job completed", "scraped", scraped, "failed", failed)
	return nil
}

func saveScraped(sqlDB *sql.DB, pl scrapers.Playlist) error {
	tracks := make([]db.CachedTrack, 0, len(pl.Tracks))
	for _, t := range pl.Tracks {
		tracks = append(tracks, db.CachedTrack{Title: t.Title, Artist: t.Artist, Album: t.Album})
	}
	return db.SaveCachedPlaylist(sqlDB, pl.Source, pl.ID, pl.Name, pl.Description, tracks, "")
}
