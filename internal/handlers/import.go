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
// The dropdown/URL import above and file import below still run
// synchronously to a fire-and-forget queued job, same as before. The
// primary way to import a specific known playlist - paste a URL, review
// what matched, fix anything wrong, then confirm - is import_review.go's
// preview/review/confirm flow (routes/import.ts's POST /preview + /match +
// /confirm, ImportPage.tsx's real caller of all three), which was missing
// entirely from this port until it was added there.
package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/fileimport"
	"github.com/drevilish/playlist-lab/internal/services/importreview"
	"github.com/drevilish/playlist-lab/internal/services/importsvc"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
	"github.com/drevilish/playlist-lab/internal/services/scrapers"
)

type ImportHandler struct {
	DB            *sql.DB
	PlexAuth      *auth.PlexClient
	Tmpl          *Templates
	Notifications *notifications.Store
	Queue         *actionqueue.Queue
	Registry      *adapters.Registry
	// Reviews backs the preview/review/confirm flow in import_review.go.
	Reviews *importreview.Store
	// PendingFiles backs the file-import modal's flow in import_file.go -
	// set by RegisterImportFile, not a caller-supplied field (nothing
	// outside this package constructs one).
	PendingFiles *pendingFileStore
}

func RegisterImport(r chi.Router, mw *auth.Middleware, h *ImportHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/import", h.page)
		r.Post("/import", h.startImport)
		r.Post("/import/file", h.startFileImport)
		r.Post("/import/ai", h.startAIImport)
		r.Get("/import/billboard/weeks", h.billboardWeeks)
	})
}

// maxImportFileSize mirrors multer's 10MB limit in routes/import.ts.
const maxImportFileSize = 10 << 20

// importSources lists, in display order, the source IDs registered in
// main.go that are actually reachable from this page. youtube-music is
// registered (see main.go) but deliberately left out here - it's the same
// service as youtube to a user picking a source, so only one tab is shown,
// same "registered but not surfaced" treatment apple/tidal/amazon/qobuz get
// above.
var importSources = []string{
	"spotify", "deezer", "youtube", "billboard", "aria", "listenbrainz", "lastfm",
}

func (h *ImportHandler) page(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	userServer, _ := db.GetUserMusicServer(h.DB, user.ID)

	var sources []adapters.ServiceMeta
	for _, id := range importSources {
		if src, ok := h.Registry.GetSource(id); ok {
			sources = append(sources, src.Meta())
		}
	}

	type countryOption struct{ Code, Name string }
	countries := make([]countryOption, len(scrapers.PopularCountries))
	for i, code := range scrapers.PopularCountries {
		countries[i] = countryOption{Code: code, Name: scrapers.PopularCountryName(code)}
	}
	country, _ := db.GetCountry(h.DB, user.ID)

	data := map[string]any{
		"User": user, "HasServer": userServer != nil && userServer.LibraryID.Valid,
		"Sources":        sources,
		"BillboardYears": billboardYearOptions(),
		"Countries":      countries, "Country": country,
	}
	if IsModalRequest(r) {
		h.Tmpl.RenderModal(w, r, "import", "Import", data)
		return
	}
	h.Tmpl.RenderPage(w, r, "import", data)
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

	userServer, err := db.GetUserMusicServer(h.DB, user.ID)
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

	userServer, err := db.GetUserMusicServer(h.DB, user.ID)
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

// billboardHot100Since is the first date the Hot 100 chart was published;
// billboardYearOptions' lower bound.
const billboardHot100Since = 1958

// billboardYearOptions backs the Import page's Billboard year picker
// (DESIGN.md; user feedback: bring back example/top-chart browsing instead
// of only a raw URL paste box) - descending so the most recent year (the
// one anyone browsing charts most likely wants) sorts first.
func billboardYearOptions() []int {
	current := time.Now().Year()
	years := make([]int, 0, current-billboardHot100Since+1)
	for y := current; y >= billboardHot100Since; y-- {
		years = append(years, y)
	}
	return years
}

// billboardValidDates fetches and caches the Hot 100's own published list of
// every real chart date (not every Saturday - the chart's day-of-week
// convention has changed over its history, so computing dates rather than
// reading the source's own index would silently produce dead links for
// older years). Same GitHub mirror the billboard adapter's FetchTracks
// already reads (internal/adapters/billboard/source.go) - this just adds
// its lightweight index file, not a new data source. Cached for the
// process lifetime plus a day: it only grows by one entry a week.
var (
	billboardDatesMu    sync.Mutex
	billboardDatesCache []string
	billboardDatesAt    time.Time
)

const billboardValidDatesURL = "https://raw.githubusercontent.com/mhollingshead/billboard-hot-100/main/valid_dates.json"

func billboardValidDates() ([]string, error) {
	billboardDatesMu.Lock()
	defer billboardDatesMu.Unlock()
	if len(billboardDatesCache) > 0 && time.Since(billboardDatesAt) < 24*time.Hour {
		return billboardDatesCache, nil
	}
	req, err := http.NewRequest(http.MethodGet, billboardValidDatesURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("valid_dates.json: status %d", resp.StatusCode)
	}
	var dates []string
	if err := json.NewDecoder(resp.Body).Decode(&dates); err != nil {
		return nil, err
	}
	billboardDatesCache = dates
	billboardDatesAt = time.Now()
	return dates, nil
}

type billboardWeekOption struct{ URL, Label string }

// billboardWeeks backs the year picker's cascading week <select> (GET
// /import/billboard/weeks?year=YYYY), rendered into the identifier field's
// picker widget in import.html.
func (h *ImportHandler) billboardWeeks(w http.ResponseWriter, r *http.Request) {
	year := r.URL.Query().Get("year")
	dates, err := billboardValidDates()
	if err != nil {
		slog.Error("failed to load Billboard valid dates", "error", err)
		http.Error(w, "Failed to load Billboard chart dates", http.StatusBadGateway)
		return
	}
	var weeks []billboardWeekOption
	for _, d := range dates {
		if !strings.HasPrefix(d, year+"-") {
			continue
		}
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			continue
		}
		weeks = append(weeks, billboardWeekOption{
			URL:   "https://www.billboard.com/charts/hot-100/" + d + "/",
			Label: t.Format("January 2, 2006"),
		})
	}
	h.Tmpl.RenderPartial(w, "partials/billboard_weeks.html", map[string]any{"Weeks": weeks})
}
