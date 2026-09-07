// Package handlers: import.go ports routes/import.ts to HTMX for the
// sources that currently have a Go SourceAdapter (see cmd/server/main.go's
// registry wiring): deezer, listenbrainz, youtube (public-playlist
// scraping), youtube-music (unauthenticated innertube browse), spotify
// (per-user token or client-credentials Web API), plus the chart sources
// aria/billboard/lastfm. Node's import.ts pulls tracks via bespoke scraper
// functions (scrapeDeezerPlaylist etc.) rather than the adapters.Registry -
// but the Go port already built each of those scrapers as a SourceAdapter
// for cross-import, so importsvc.ImportPlaylist reuses that registry
// instead of re-deriving a second lookup mechanism (see importsvc's
// package doc).
//
// Apple/Tidal/Amazon/Qobuz do have registered SourceAdapters (chromedp
// browser scrape, see cmd/server/main.go) but are deliberately left out of
// importSources below: every chromedp-backed adapter in this rewrite is
// logic-verified against the original Puppeteer source only, never run
// against a real page (no Chrome/Chromium binary in the sandbox this was
// built in - see docs/GO_REWRITE.md's "Known gaps"), so surfacing them here
// would offer a source nobody has confirmed actually works yet. Add each
// once verified against a real browser. File-based import (m3u/m3u8/pls/
// xspf/csv/txt) is a separate upload form (startFileImport below), not one
// of these dropdown sources, so it isn't in this list either.
//
// Like mixes.go and cross_import.go, importing runs as a background
// action-queue job reported through the notification bell - no separate
// preview/confirm round trip, since import.ts itself has no review step
// either (unmatched tracks just land in missing_tracks, same as here).
package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/fileimport"
	"github.com/drevilish/playlist-lab/internal/services/importsvc"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

type ImportHandler struct {
	DB            *sql.DB
	PlexAuth      *auth.PlexClient
	Tmpl          *Templates
	Notifications *notifications.Store
	Queue         *actionqueue.Queue
	Registry      *adapters.Registry
}

func RegisterImport(r chi.Router, mw *auth.Middleware, h *ImportHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/import", h.page)
		r.Post("/import", h.startImport)
		r.Post("/import/file", h.startFileImport)
	})
}

// maxImportFileSize mirrors multer's 10MB limit in routes/import.ts.
const maxImportFileSize = 10 << 20

// importSources lists, in display order, the source IDs registered in
// main.go that are actually reachable from this page.
var importSources = []string{
	"deezer", "listenbrainz", "youtube", "youtube-music", "spotify", "aria", "billboard", "lastfm",
}

func (h *ImportHandler) page(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	userServer, _ := db.GetUserServer(h.DB, user.ID)

	var sources []adapters.ServiceMeta
	for _, id := range importSources {
		if src, ok := h.Registry.GetSource(id); ok {
			sources = append(sources, src.Meta())
		}
	}

	h.Tmpl.RenderPage(w, "import", map[string]any{
		"User": user, "HasServer": userServer != nil && userServer.LibraryID.Valid,
		"Sources": sources,
	})
}

// startImport enqueues a fetch+match+create-playlist job for one source
// playlist URL/ID, mirroring the single POST /api/import/:source handlers
// in routes/import.ts collapsed into one route since they all did the same
// thing with a different source string.
func (h *ImportHandler) startImport(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	source := r.FormValue("source")
	identifier := r.FormValue("identifier")
	customName := r.FormValue("playlistName")
	if source == "" || identifier == "" {
		http.Error(w, "source and identifier are required", http.StatusBadRequest)
		return
	}
	if _, ok := h.Registry.GetSource(source); !ok {
		http.Error(w, "unsupported import source: "+source, http.StatusBadRequest)
		return
	}

	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "No music library selected. Please select a library first.", http.StatusBadRequest)
		return
	}

	jobID, _ := h.Queue.Enqueue(user.ID, "Importing from "+source, notifications.TypeImport, func(notificationID string) error {
		return h.run(context.Background(), user.ID, userServer, source, identifier, customName, notificationID)
	})

	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{
		"Notifications": h.Notifications.List(user.ID),
		"JobID":         jobID,
	})
}

// startFileImport ports routes/import.ts's POST /api/import/file: parse the
// uploaded playlist file (multipart/form-data, stdlib mime/multipart via
// ParseMultipartForm - no new dependency needed) into a track list via
// internal/services/fileimport, then run the same fetch+match+create-
// playlist background job as startImport, minus the "fetch" step (the file
// content already is the track list).
func (h *ImportHandler) startFileImport(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	if err := r.ParseMultipartForm(maxImportFileSize); err != nil {
		http.Error(w, "invalid or too-large file upload (10MB limit)", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if !fileimport.IsAllowedExtension(header.Filename) {
		http.Error(w, "invalid file type. Please upload an M3U, M3U8, PLS, XSPF, CSV, or TXT playlist file.", http.StatusBadRequest)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(file, maxImportFileSize+1))
	if err != nil {
		http.Error(w, "unable to read uploaded file", http.StatusBadRequest)
		return
	}
	content := string(raw)
	if strings.TrimSpace(content) == "" {
		http.Error(w, "file is empty. Please upload a valid playlist file.", http.StatusBadRequest)
		return
	}

	customName := r.FormValue("playlistName")
	filename := header.Filename

	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "No music library selected. Please select a library first.", http.StatusBadRequest)
		return
	}

	jobID, _ := h.Queue.Enqueue(user.ID, "Importing "+filename, notifications.TypeImport, func(notificationID string) error {
		return h.runFile(context.Background(), user.ID, userServer, content, filename, customName, notificationID)
	})

	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{
		"Notifications": h.Notifications.List(user.ID),
		"JobID":         jobID,
	})
}

// runFile is startFileImport's background job body, mirroring run() but
// parsing the file content into tracks first (fileimport.Parse) instead of
// calling a SourceAdapter.FetchTracks.
func (h *ImportHandler) runFile(ctx context.Context, userID int64, userServer *db.UserServer, content, filename, customName, notificationID string) error {
	user, err := db.GetUserByID(h.DB, userID)
	if err != nil {
		return err
	}
	serverURL := userServer.ServerURL
	token := plex.ResolveToken(user.PlexToken, userServer.AccessToken.String)
	libraryID := userServer.LibraryID.String
	client := plex.NewClient(serverURL, token, h.PlexAuth.ClientID, "Playlist Lab")

	parsed, err := fileimport.Parse(content, filename)
	if err != nil {
		status := notifications.StatusError
		detail := err.Error()
		h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return err
	}

	progress := func(phase string, current, total int) {
		detail := phase
		if total > 0 {
			detail = fmt.Sprintf("%s (%d/%d)", phase, current, total)
		}
		h.Notifications.Update(userID, notificationID, notifications.Patch{Detail: &detail})
	}

	result, err := importsvc.ImportTracksFromFile(h.DB, client, parsed.Name, parsed.Tracks, importsvc.Options{
		UserID: userID, LibraryID: libraryID, CustomName: customName,
	}, progress, func() bool { return false })
	if err != nil {
		status := notifications.StatusError
		detail := err.Error()
		h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return err
	}

	dbPlaylistID, _, err := importsvc.FinalizeImportResult(h.DB, client, "file", filename, userID,
		userServer.ServerClientID, libraryID, result, importsvc.FinalizeOpts{PlaylistName: customName})
	if err != nil {
		status := notifications.StatusError
		detail := err.Error()
		h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return err
	}
	slog.Info("[Import] Created playlist from file", "playlistId", dbPlaylistID, "filename", filename)

	status := notifications.StatusSuccess
	detail := fmt.Sprintf("Created \"%s\" - matched %d of %d tracks", result.PlaylistName, result.MatchedCount, result.TotalCount)
	pct := 100
	h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail, Progress: &pct})
	return nil
}

// run is the background job body: ports import.ts's importPlaylist() +
// finalizeImportResult() pair, minus the SSE progress plumbing (the
// notification's Detail/Progress fields stand in for that, same as every
// other background job).
func (h *ImportHandler) run(ctx context.Context, userID int64, userServer *db.UserServer, source, identifier, customName, notificationID string) error {
	user, err := db.GetUserByID(h.DB, userID)
	if err != nil {
		return err
	}
	serverURL := userServer.ServerURL
	token := plex.ResolveToken(user.PlexToken, userServer.AccessToken.String)
	libraryID := userServer.LibraryID.String
	client := plex.NewClient(serverURL, token, h.PlexAuth.ClientID, "Playlist Lab")

	progress := func(phase string, current, total int) {
		detail := phase
		if total > 0 {
			detail = fmt.Sprintf("%s (%d/%d)", phase, current, total)
		}
		h.Notifications.Update(userID, notificationID, notifications.Patch{Detail: &detail})
	}

	result, err := importsvc.ImportPlaylist(ctx, h.Registry, h.DB, client, source, identifier, importsvc.Options{
		UserID: userID, LibraryID: libraryID, CustomName: customName,
	}, progress, func() bool { return false })
	if err != nil {
		return err
	}

	dbPlaylistID, _, err := importsvc.FinalizeImportResult(h.DB, client, source, identifier, userID,
		userServer.ServerClientID, libraryID, result, importsvc.FinalizeOpts{PlaylistName: customName})
	if err != nil {
		status := notifications.StatusError
		detail := err.Error()
		h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return err
	}
	slog.Info("[Import] Created playlist", "playlistId", dbPlaylistID, "source", source)

	status := notifications.StatusSuccess
	detail := fmt.Sprintf("Created \"%s\" - matched %d of %d tracks", result.PlaylistName, result.MatchedCount, result.TotalCount)
	pct := 100
	h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail, Progress: &pct})
	return nil
}
