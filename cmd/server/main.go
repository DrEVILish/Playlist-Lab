// Command server is the Go+HTMX replacement for the Node/Express backend,
// built out phase by phase per /root/.claude/plans/think-about-how-we-valiant-island.md.
// Phase 0 (auth) and Phase 1 (Plex server setup + playlist CRUD) so far -
// matching, adapters, scheduling, scraping, and AI/mixes land in later
// phases.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/adapters/amazon"
	"github.com/drevilish/playlist-lab/internal/adapters/apple"
	"github.com/drevilish/playlist-lab/internal/adapters/aria"
	"github.com/drevilish/playlist-lab/internal/adapters/billboard"
	"github.com/drevilish/playlist-lab/internal/adapters/deezer"
	"github.com/drevilish/playlist-lab/internal/adapters/lastfmsource"
	"github.com/drevilish/playlist-lab/internal/adapters/listenbrainz"
	"github.com/drevilish/playlist-lab/internal/adapters/plex"
	"github.com/drevilish/playlist-lab/internal/adapters/qobuz"
	"github.com/drevilish/playlist-lab/internal/adapters/tidal"
	"github.com/drevilish/playlist-lab/internal/adapters/youtube"
	"github.com/drevilish/playlist-lab/internal/adapters/youtubeplain"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/config"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/handlers"
	"github.com/drevilish/playlist-lab/internal/logging"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/crossimport"
	"github.com/drevilish/playlist-lab/internal/services/deemix"
	"github.com/drevilish/playlist-lab/internal/services/jobs"
	"github.com/drevilish/playlist-lab/internal/services/lidarr"
	"github.com/drevilish/playlist-lab/internal/services/mixes"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	schedulerjob "github.com/drevilish/playlist-lab/internal/services/scheduler"
	"github.com/drevilish/playlist-lab/internal/session"
	"github.com/drevilish/playlist-lab/static"
	"github.com/drevilish/playlist-lab/templates"
)

func main() {
	cfg := config.Load()
	if cfg.IsProduction() && cfg.UsingDefaultSessionSecret() {
		slog.Error("SESSION_SECRET must be set to a non-default value in production; refusing to start with a publicly-known key protecting session cookies and encrypted OAuth tokens/API keys")
		os.Exit(1)
	}

	// Mirrors utils/logger.ts: stdout (for the systemd journal, unchanged)
	// plus size-rotated ./logs/combined.log and ./logs/error.log, so any
	// ops tooling built around those file paths keeps working post-cutover.
	// The admin in-app log-viewer tab remains deferred (handlers/admin.go's
	// header comment) - this only restores the files themselves.
	closeLogs, err := logging.Setup(cfg.LogDir, cfg.LogLevel)
	if err != nil {
		slog.Error("failed to set up logging", "error", err)
		os.Exit(1)
	}
	defer closeLogs()

	sqlDB, err := db.Open(cfg.DatabasePath)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer sqlDB.Close()

	// A level set from the admin Logs tab outranks LOG_LEVEL, and has to be
	// reapplied here because logging.Setup ran before the database was open.
	// Same admin_config-overrides-env pattern as deemix_arl/lidarr below.
	if v, ok, _ := db.GetAdminConfig(sqlDB, "log_level"); ok {
		if err := logging.SetLevel(v); err != nil {
			slog.Warn("ignoring unusable persisted log level", "error", err)
		}
	}

	store := session.NewStore(sqlDB)
	stop := make(chan struct{})
	defer close(stop)
	store.StartCleanup(stop)

	tmpl, err := handlers.LoadTemplates(templates.FS)
	if err != nil {
		slog.Error("failed to load templates", "error", err)
		os.Exit(1)
	}

	secure := cfg.CookieSecure
	mw := &auth.Middleware{
		DB:      sqlDB,
		Store:   store,
		Secure:  secure,
		DevMode: !cfg.IsProduction(),
		DevAuto: cfg.DevNoAuth,
	}
	plexClient := auth.NewPlexClient(cfg.PlexClientID, "Playlist Lab")

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	if cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(mw.WithSession)
	r.Use(mw.OptionalAuth)

	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServerFS(static.FS)))

	handlers.RegisterAuth(r, &handlers.AuthHandler{
		DB: sqlDB, Plex: plexClient, Auth: mw, Store: store,
		Secure: secure, Tmpl: tmpl, BaseURL: cfg.PublicURL,
	})
	handlers.RegisterServers(r, mw, &handlers.ServersHandler{DB: sqlDB, Plex: plexClient, Tmpl: tmpl})
	handlers.RegisterSettings(r, mw, &handlers.SettingsHandler{DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl})
	handlers.RegisterStatus(r, mw, &handlers.StatusHandler{DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl})
	// Cover art is relayed through the server rather than linked directly -
	// see internal/handlers/proxy.go for why.
	handlers.RegisterProxy(r, mw, &handlers.ProxyHandler{DB: sqlDB, PlexAuth: plexClient})
	handlers.RegisterAdminLogs(r, mw, &handlers.AdminLogsHandler{DB: sqlDB, Tmpl: tmpl, LogDir: cfg.LogDir})
	handlers.RegisterBackup(r, mw, &handlers.BackupHandler{DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl})
	notificationStore := notifications.NewStore()
	handlers.RegisterNotifications(r, mw, &handlers.NotificationsHandler{Store: notificationStore, Tmpl: tmpl})

	actionQueue := actionqueue.New(notificationStore)
	handlers.RegisterPlaylists(r, mw, &handlers.PlaylistsHandler{
		DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue,
	})
	mixService := mixes.New()
	handlers.RegisterMixes(r, mw, &handlers.MixesHandler{
		DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue, Mixes: mixService,
	})
	handlers.RegisterMixTemplates(r, mw, &handlers.MixTemplatesHandler{
		DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue, Mixes: mixService,
	})

	// Cross-import (Phase 6c) only ever exposes Plex as a source and
	// YouTube as a target (see cross-import.ts's FILTER comments, ported
	// verbatim rather than re-opened), and plain import (Phase 6e) has a
	// Go SourceAdapter for deezer/listenbrainz/youtube (public playlist
	// scraping) plus, as of Phase 5/5b, apple/tidal/amazon/qobuz (chromedp
	// browser scrape - see adapters/{apple,tidal,amazon,qobuz}/source.go).
	// Spotify's browser-scrape source (scrapeSpotifyWithBrowser) still
	// isn't ported - left unregistered as a source rather than faked.
	// Both handlers share one registry.
	registry := adapters.NewRegistry()
	registry.RegisterSource(plex.NewSource(sqlDB, plexClient))
	registry.RegisterSource(deezer.NewSource())
	registry.RegisterSource(listenbrainz.NewSource())
	registry.RegisterSource(youtubeplain.NewSource(sqlDB, cfg.SessionSecret))
	registry.RegisterSource(apple.NewSource())
	registry.RegisterSource(tidal.NewSource())
	registry.RegisterSource(amazon.NewSource())
	registry.RegisterSource(qobuz.NewSource())
	registry.RegisterSource(aria.NewSource())
	registry.RegisterSource(billboard.NewSource())
	registry.RegisterSource(lastfmsource.NewSource())
	registry.RegisterTarget(youtube.NewTarget(sqlDB, cfg.SessionSecret, cfg.YouTubeClientID, cfg.YouTubeClientSecret, cfg.YouTubeRedirectURI))
	handlers.RegisterCrossImport(r, mw, &handlers.CrossImportHandler{
		DB: sqlDB, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue,
		Registry: registry, Sessions: crossimport.NewStore(),
	})
	handlers.RegisterImport(r, mw, &handlers.ImportHandler{
		DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue,
		Registry: registry,
	})
	handlers.RegisterCharts(r, mw, &handlers.ChartsHandler{
		DB: sqlDB, SessionSecret: cfg.SessionSecret,
		SpotifyClientID: cfg.SpotifyClientID, SpotifyClientSecret: cfg.SpotifyClientSecret,
	})

	// Missing tracks + acquisition (Phase 6d): deemix (our local Deezer
	// downloader) and Lidarr are both plain REST clients, wired the same way
	// as every other plex-direct handler (playlists.go, mixes.go) rather
	// than through the cross-import adapter registry, since neither is a
	// cross-import source/target.
	deemixService := deemix.New(deemix.Config{URL: cfg.DeemixURL, ARL: cfg.DeemixArl}, sqlDB, notificationStore)
	lidarrService := lidarr.New(lidarr.Config{URL: cfg.LidarrURL, APIKey: cfg.LidarrAPIKey}, sqlDB, notificationStore)
	handlers.RegisterMissing(r, mw, &handlers.MissingHandler{
		DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue,
		Deemix: deemixService, Lidarr: lidarrService,
	})
	handlers.RegisterAdmin(r, mw, &handlers.AdminHandler{
		DB: sqlDB, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue, Deemix: deemixService,
	})

	// Scheduling (Phase 6f): playlist-refresh + mix-generation schedules,
	// closing out the schedule-checker job deferred through Phase 4/5 (see
	// scheduler.Deps below and jobs.RunScheduleChecker's registration).
	schedulerDeps := schedulerjob.Deps{DB: sqlDB, Registry: registry, Mixes: mixService, ClientID: cfg.PlexClientID}
	handlers.RegisterSchedules(r, mw, &handlers.SchedulesHandler{
		DB: sqlDB, Tmpl: tmpl, Deps: schedulerDeps,
	})

	// deemix keeps downloading across a restart of this server, but the
	// pollers watching those downloads - and the link back to the missing
	// track each one was for - only lived in memory, so a restart left the
	// arriving files with nothing to reconcile them. Pick them back up, and
	// re-test the ARL now rather than waiting for the daily job, so an admin
	// checking right after a restart sees a real answer immediately.
	deemixService.ResumeDownloads()
	go deemixService.CheckArlAndNotifyAdmins()

	// Background jobs (Phase 4/5/6f).
	scheduler := jobs.NewScheduler()
	jobConfigs := []jobs.Config{
		{
			Name: "cache-cleanup", Spec: "0 3 * * 0", // 3:00 AM every Sunday
			Handler: func() error { return jobs.RunCacheCleanup(sqlDB) },
			Enabled: cfg.EnableCacheCleanup,
		},
		// Deezer charts + ARIA charts, cached for fast popular-playlist
		// import - see jobs.RunDailyScraper's doc for which platforms this
		// does NOT cover (and why).
		{
			Name: "daily-scraper", Spec: cfg.ScraperSchedule,
			Handler: func() error { return jobs.RunDailyScraper(sqlDB) },
			Enabled: cfg.EnableScraperJob,
		},
		// The Deezer ARL deemix logs in with expires every few months;
		// checking it on a schedule (and notifying admins on failure) turns
		// that from "downloads have been silently failing for a week" into
		// something someone is actually told about. Deferred until now
		// (Phase 4 left this job body unwritten) since it needed the Deemix
		// integration this phase ports.
		{
			Name: "deemix-arl-check", Spec: cfg.DeemixArlCheckSchedule,
			Handler: func() error { deemixService.CheckArlAndNotifyAdmins(); return nil },
			Enabled: cfg.EnableDeemixArlCheck,
		},
		{
			Name: "schedule-checker", Spec: cfg.ScheduleCheckerSchedule,
			Handler: func() error { return jobs.RunScheduleChecker(schedulerDeps) },
			Enabled: cfg.EnableScheduleChecker,
		},
	}
	// Fed straight to the admin Schedules tab (handlers.BackgroundJob), so
	// its "enabled"/"next run" view can never drift from what actually got
	// registered below - jobs.Scheduler itself exposes no status API to
	// read this back from (see BackgroundJob's doc).
	backgroundJobs := make([]handlers.BackgroundJob, len(jobConfigs))
	for i, jc := range jobConfigs {
		scheduler.Register(jc)
		backgroundJobs[i] = handlers.BackgroundJob{Name: jc.Name, Spec: jc.Spec, Enabled: jc.Enabled}
	}
	handlers.RegisterAdminSchedules(r, mw, &handlers.AdminSchedulesHandler{DB: sqlDB, Tmpl: tmpl, Jobs: backgroundJobs})
	if cfg.IsProduction() || cfg.EnableJobs {
		scheduler.Start()
		defer scheduler.Stop()
		slog.Info("background jobs started")
	} else {
		slog.Info("background jobs disabled in development mode")
	}

	addr := cfg.Host + ":" + cfg.Port
	slog.Info("playlist-lab (go) listening", "addr", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
}
