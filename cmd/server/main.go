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

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/config"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/handlers"
	"github.com/drevilish/playlist-lab/internal/session"
	"github.com/drevilish/playlist-lab/static"
	"github.com/drevilish/playlist-lab/templates"
)

func main() {
	cfg := config.Load()

	logLevel := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	sqlDB, err := db.Open(cfg.DatabasePath)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer sqlDB.Close()

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
	handlers.RegisterPlaylists(r, mw, &handlers.PlaylistsHandler{DB: sqlDB, PlexAuth: plexClient, Tmpl: tmpl})

	addr := cfg.Host + ":" + cfg.Port
	slog.Info("playlist-lab (go) listening", "addr", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
}
