// Command server is the Go+HTMX replacement for the Node/Express backend,
// built out phase by phase per /root/.claude/plans/think-about-how-we-valiant-island.md.
// Phase 0 (auth) and Phase 1 (Plex server setup + playlist CRUD) so far -
// matching, adapters, scheduling, scraping, and AI/mixes land in later
// phases.
package main

import (
	"database/sql"
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
	"github.com/drevilish/playlist-lab/internal/adapters/spotify"
	"github.com/drevilish/playlist-lab/internal/adapters/tidal"
	"github.com/drevilish/playlist-lab/internal/adapters/youtube"
	"github.com/drevilish/playlist-lab/internal/adapters/youtubemusic"
	"github.com/drevilish/playlist-lab/internal/adapters/youtubeplain"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/config"
	"github.com/drevilish/playlist-lab/internal/crypto"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/handlers"
	"github.com/drevilish/playlist-lab/internal/logging"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/crossimport"
	"github.com/drevilish/playlist-lab/internal/services/deemix"
	"github.com/drevilish/playlist-lab/internal/services/importreview"
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

	reencryptLegacySpotifyCredentials(sqlDB, cfg.SessionSecret)

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
	// Lets pageData (templates.go) look up the current user's saved
	// text-size preference server-side (DESIGN.md §14) instead of every
	// page reading it from localStorage after load.
	tmpl.DB = sqlDB

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
	// handlers.Recoverer replaces chi's default: same recover+log-stack
	// behavior, but answers with the themed error page (DESIGN.md §10)
	// instead of chi's plain "500 Internal Server Error" text body.
	r.Use(handlers.Recoverer(tmpl))
	if cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(mw.WithSession)
	r.Use(mw.OptionalAuth)

	// Themed 404/405 (DESIGN.md §10) - both run through the same top-level
	// middleware stack above (WithSession/OptionalAuth included), so .User
	// still renders the real header for a logged-in user who hits a bad URL.
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		tmpl.RenderErrorPage(w, r, http.StatusNotFound, "Page not found", "The page you're looking for doesn't exist or may have moved.")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		tmpl.RenderErrorPage(w, r, http.StatusMethodNotAllowed, "Method not allowed", "That action isn't supported on this page.")
	})

	// go:embed files carry a zero mtime, so http.FileServerFS never emits a
	// Last-Modified/ETag validator for them - with no Cache-Control either,
	// browsers are left to their own heuristics for how long to keep an old
	// build's JS/CSS around after a deploy, which is exactly the kind of
	// "works in a clean session, breaks after a normal refresh" staleness
	// this app hit right after each of today's live redeploys. Forcing
	// revalidation on every load keeps every reload on the current build.
	staticHandler := http.StripPrefix("/static/", http.FileServerFS(static.FS))
	r.Handle("/static/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		staticHandler.ServeHTTP(w, req)
	}))

	handlers.RegisterAuth(r, &handlers.AuthHandler{
		DB: sqlDB, Plex: plexClient, Auth: mw, Store: store,
		Secure: secure, Tmpl: tmpl,
	})
	notificationStore := notifications.NewStore()
	handlers.RegisterServers(r, mw, &handlers.ServersHandler{DB: sqlDB, Plex: plexClient, Tmpl: tmpl, Notifications: notificationStore})
	handlers.RegisterStatus(r, mw, &handlers.StatusHandler{DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl})
	// Cover art is relayed through the server rather than linked directly -
	// see internal/handlers/proxy.go for why.
	handlers.RegisterProxy(r, mw, &handlers.ProxyHandler{DB: sqlDB, PlexAuth: plexClient})
	handlers.RegisterAdminLogs(r, mw, &handlers.AdminLogsHandler{DB: sqlDB, Tmpl: tmpl, LogDir: cfg.LogDir, Notifications: notificationStore})
	handlers.RegisterBackup(r, mw, &handlers.BackupHandler{DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl, Notifications: notificationStore})
	handlers.RegisterNotifications(r, mw, &handlers.NotificationsHandler{Store: notificationStore, Tmpl: tmpl})

	actionQueue := actionqueue.New(notificationStore)
	playlistsHandler := &handlers.PlaylistsHandler{
		DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue,
	}
	handlers.RegisterPlaylists(r, mw, playlistsHandler)
	handlers.RegisterExport(r, mw, playlistsHandler)
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
	// browser scrape - see adapters/{apple,tidal,amazon,qobuz}/source.go),
	// and now spotify (per-user token or client-credentials Web API,
	// internal/adapters/spotify/source.go) and youtube-music (unauthenticated
	// innertube browse, internal/adapters/youtubemusic/target.go's
	// FetchTracks). Both handlers share one registry.
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
	registry.RegisterSource(spotify.NewSource(sqlDB, cfg.SessionSecret, cfg.SpotifyClientID, cfg.SpotifyClientSecret))
	// Spotify's Web API 403s a client-credentials token on most playlist
	// reads unless the app is in Extended Quota Mode (a Spotify-side
	// approval, not something this app can route around) - the source
	// above worked for nothing until the user's own OAuth token is used
	// instead. spotify.Target already implements the full OAuthCapable
	// flow (GetOAuthURL/HandleOAuthCallback/HasValidConnection); it just
	// sat unregistered, so "Connect Spotify" never appeared next to
	// YouTube's under Connected Services. Registering it as a target is
	// what makes cross_import.go's existing generic OAuth routes
	// (/cross-import/oauth/spotify/...) available for it.
	registry.RegisterTarget(spotify.NewTarget(sqlDB, cfg.SessionSecret, cfg.SpotifyRedirectURI))
	registry.RegisterSource(youtubemusic.NewTarget(sqlDB, cfg.SessionSecret))
	// Same admin_config-overrides-env pattern as deemix_arl/lidarr below -
	// lets an admin paste Google OAuth credentials into /admin instead of
	// editing .env and restarting (routes/youtube-config.ts's approach).
	youtubeClientID, youtubeClientSecret, youtubeRedirectURI := cfg.YouTubeClientID, cfg.YouTubeClientSecret, cfg.YouTubeRedirectURI
	if v, ok, _ := db.GetAdminConfig(sqlDB, "youtube_client_id"); ok {
		youtubeClientID = v
	}
	if v, ok, _ := db.GetAdminConfig(sqlDB, "youtube_client_secret"); ok {
		youtubeClientSecret = v
	}
	if v, ok, _ := db.GetAdminConfig(sqlDB, "youtube_redirect_uri"); ok {
		youtubeRedirectURI = v
	}
	youtubeTarget := youtube.NewTarget(sqlDB, cfg.SessionSecret, youtubeClientID, youtubeClientSecret, youtubeRedirectURI)
	registry.RegisterTarget(youtubeTarget)
	handlers.RegisterCrossImport(r, mw, &handlers.CrossImportHandler{
		DB: sqlDB, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue,
		Registry: registry, Sessions: crossimport.NewStore(), Store: store,
	})
	importHandler := &handlers.ImportHandler{
		DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue,
		Registry: registry, Reviews: importreview.NewStore(),
	}
	handlers.RegisterImport(r, mw, importHandler)
	handlers.RegisterImportReview(r, mw, importHandler)
	handlers.RegisterImportFile(r, mw, importHandler)
	handlers.RegisterPlexHome(r, mw, importHandler)
	handlers.RegisterCharts(r, mw, &handlers.ChartsHandler{
		DB: sqlDB, Tmpl: tmpl, SessionSecret: cfg.SessionSecret,
		SpotifyClientID: cfg.SpotifyClientID, SpotifyClientSecret: cfg.SpotifyClientSecret,
		Notifications: notificationStore,
	})

	// Missing tracks + acquisition (Phase 6d): deemix (our local Deezer
	// downloader) and Lidarr are both plain REST clients, wired the same way
	// as every other plex-direct handler (playlists.go, mixes.go) rather
	// than through the cross-import adapter registry, since neither is a
	// cross-import source/target.
	deemixArl := cfg.DeemixArl
	if v, ok, _ := db.GetAdminConfig(sqlDB, "deemix_arl"); ok {
		deemixArl = v
	}
	lidarrURL, lidarrAPIKey := cfg.LidarrURL, cfg.LidarrAPIKey
	if v, ok, _ := db.GetAdminConfig(sqlDB, "lidarr_url"); ok {
		lidarrURL = v
	}
	if v, ok, _ := db.GetAdminConfig(sqlDB, "lidarr_api_key"); ok {
		lidarrAPIKey = v
	}
	deemixService := deemix.New(deemix.Config{ARL: deemixArl}, sqlDB, notificationStore)
	lidarrService := lidarr.New(lidarr.Config{URL: lidarrURL, APIKey: lidarrAPIKey}, sqlDB, notificationStore)
	handlers.RegisterMissing(r, mw, &handlers.MissingHandler{
		DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue,
		Deemix: deemixService, Lidarr: lidarrService,
	})
	handlers.RegisterAdmin(r, mw, &handlers.AdminHandler{
		DB: sqlDB, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue,
		Deemix: deemixService, Lidarr: lidarrService, YouTube: youtubeTarget,
	})
	// Settings' admin-only tabs (Statistics/Users/.../YouTube) are gated by
	// .IsAdmin in settings.html, not by a separate page - see admin.go's
	// package comment - but each one's actual content still lazy-loads
	// from AdminHandler's own routes above (same hx-trigger="revealed"
	// pattern the rest of this file already uses), so SettingsHandler
	// itself needs no Deemix/Lidarr/YouTube handles of its own.
	handlers.RegisterSettings(r, mw, &handlers.SettingsHandler{DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl, Store: store, Notifications: notificationStore})

	// Scheduling (Phase 6f): playlist-refresh + mix-generation schedules,
	// closing out the schedule-checker job deferred through Phase 4/5 (see
	// scheduler.Deps below and jobs.RunScheduleChecker's registration).
	schedulerDeps := schedulerjob.Deps{DB: sqlDB, Registry: registry, Mixes: mixService, ClientID: cfg.PlexClientID}
	handlers.RegisterSchedules(r, mw, &handlers.SchedulesHandler{
		DB: sqlDB, Tmpl: tmpl, Deps: schedulerDeps, Notifications: notificationStore,
	})

	// Collections (DESIGN.md §11.11): Kometa-style Plex Collection
	// management, gated to the real Plex server owner (RequirePlexOwner),
	// reusing the same scheduler.Deps built above for its refresh engine.
	handlers.RegisterCollections(r, mw, &handlers.CollectionsHandler{
		DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl, Notifications: notificationStore,
		Queue: actionQueue, SchedulerDeps: schedulerDeps,
	})

	// Dynamic Collections (DESIGN.md §11.11 / Kometa's "dynamic_collections"
	// feature): expands one set definition into many Collections rows -
	// same gate, same background queue as Collections itself.
	handlers.RegisterDynamicCollections(r, mw, &handlers.DynamicCollectionsHandler{
		DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl, Notifications: notificationStore, Queue: actionQueue,
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

// legacyDefaultSessionSecret is config.defaultSessionSecret's value,
// duplicated here (it's unexported there, and this is the one legitimate
// reason to reference it: recovering from having used it). Not a secret
// itself - it's the well-known fallback published in this repo's source,
// which is exactly why UsingDefaultSessionSecret's production guard exists.
const legacyDefaultSessionSecret = "default-secret-change-in-production"

// reencryptLegacySpotifyCredentials self-heals the one concrete cost of
// rotating SESSION_SECRET during the v2->v3 cutover: any user who'd
// already saved their own Spotify app Client ID/Secret (encrypted under
// whatever secret was in effect at the time - the published default,
// pre-rotation) can no longer have them decrypted under the new secret.
// Runs on every startup but is a no-op once done: it only touches a row
// whose stored value fails to decrypt with the current secret but
// succeeds with the legacy default, re-encrypting it under the current
// secret so the credential survives without the user re-entering it.
func reencryptLegacySpotifyCredentials(sqlDB *sql.DB, currentSecret string) {
	if currentSecret == legacyDefaultSessionSecret {
		return
	}
	users, err := db.GetAllUsers(sqlDB)
	if err != nil {
		slog.Error("reencryptLegacySpotifyCredentials: failed to list users", "error", err)
		return
	}
	for _, u := range users {
		creds, err := db.GetSpotifyCredentials(sqlDB, u.ID)
		if err != nil || creds == nil {
			continue
		}
		if _, err := crypto.Decrypt(creds.ClientID, currentSecret); err == nil {
			continue // already readable under the current secret
		}
		clientID, err := crypto.Decrypt(creds.ClientID, legacyDefaultSessionSecret)
		if err != nil {
			continue // not the legacy-secret case either; leave it alone
		}
		clientSecret, err := crypto.Decrypt(creds.ClientSecret, legacyDefaultSessionSecret)
		if err != nil {
			continue
		}
		newID, err := crypto.Encrypt(clientID, currentSecret)
		if err != nil {
			slog.Error("reencryptLegacySpotifyCredentials: re-encrypt failed", "userID", u.ID, "error", err)
			continue
		}
		newSecret, err := crypto.Encrypt(clientSecret, currentSecret)
		if err != nil {
			slog.Error("reencryptLegacySpotifyCredentials: re-encrypt failed", "userID", u.ID, "error", err)
			continue
		}
		if err := db.SaveSpotifyCredentials(sqlDB, u.ID, newID, newSecret); err != nil {
			slog.Error("reencryptLegacySpotifyCredentials: save failed", "userID", u.ID, "error", err)
			continue
		}
		slog.Info("reencryptLegacySpotifyCredentials: recovered Spotify credentials after SESSION_SECRET rotation", "userID", u.ID)
	}
}
