// Package handlers: cross_import.go ports routes/cross-import.ts to HTMX -
// import a Plex playlist into an external service (currently just YouTube;
// see registrySetup in cmd/server/main.go for why only Plex-source/
// YouTube-target are registered). Progress during matching is reported
// through the shared notification bell like every other background job in
// this app (actionqueue + notifications.Store); the extra per-track review
// step (override a match, mark a track skipped, then execute) needs a
// small session store of its own - see internal/services/crossimport for
// why that's not just another notification.
package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/crossimport"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

type CrossImportHandler struct {
	DB            *sql.DB
	Tmpl          *Templates
	Notifications *notifications.Store
	Queue         *actionqueue.Queue
	Registry      *adapters.Registry
	Sessions      *crossimport.Store
}

func RegisterCrossImport(r chi.Router, mw *auth.Middleware, h *CrossImportHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/cross-import", h.page)
		r.Get("/cross-import/playlists", h.playlists)
		r.Post("/cross-import/match", h.startMatch)
		r.Get("/cross-import/match/{sessionId}/status", h.matchStatus)
		r.Post("/cross-import/match/{sessionId}/cancel", h.cancelMatch)
		r.Post("/cross-import/review/{sessionId}/track/{idx}/skip", h.toggleSkip)
		r.Get("/cross-import/review/{sessionId}/track/{idx}/search", h.searchCandidates)
		r.Post("/cross-import/review/{sessionId}/track/{idx}/select", h.selectCandidate)
		r.Post("/cross-import/execute/{sessionId}", h.execute)
		r.Get("/cross-import/history", h.history)

		r.Get("/cross-import/oauth/services", h.oauthServices)
		r.Get("/cross-import/oauth/{service}", h.oauthStart)
		r.Get("/cross-import/oauth/{service}/callback", h.oauthCallback)
		r.Delete("/cross-import/oauth/{service}", h.oauthRevoke)
	})
}

// targetID is fixed for now (only YouTube is registered as a target - see
// main.go); kept as a lookup rather than a bare constant so a second target
// adapter only needs a form field added to templates/cross_import.html plus
// this registry, not a rewrite of the handler.
const defaultTargetID = "youtube"

func (h *CrossImportHandler) page(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	target, _ := h.Registry.GetTarget(defaultTargetID)

	connected := false
	if oauth, ok := target.(adapters.OAuthCapable); ok {
		connected, _ = oauth.HasValidConnection(r.Context(), user.ID)
	}

	userServer, _ := db.GetUserServer(h.DB, user.ID)
	jobs, _ := db.GetCrossImportJobs(h.DB, user.ID)

	h.Tmpl.RenderPage(w, "cross_import", map[string]any{
		"User": user, "HasServer": userServer != nil,
		"TargetConnected": connected, "TargetName": target.Meta().Name,
		"Jobs": jobs,
	})
}

func (h *CrossImportHandler) playlists(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	source, ok := h.Registry.GetSource("plex")
	if !ok {
		http.Error(w, "source adapter not available", http.StatusInternalServerError)
		return
	}
	lister, ok := source.(adapters.PlaylistLister)
	if !ok {
		http.Error(w, "source does not support listing playlists", http.StatusBadRequest)
		return
	}
	playlists, err := lister.ListPlaylists(r.Context(), user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.Tmpl.RenderPartial(w, "partials/cross_import_playlists.html", map[string]any{"Playlists": playlists})
}

// startMatch enqueues fetch+match as a background action-queue job (same
// pattern as mixes.go's generate) and hands back a progress partial that
// polls matchStatus every second until the job reaches the review phase.
func (h *CrossImportHandler) startMatch(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	playlistID := r.FormValue("playlistId")
	if playlistID == "" {
		http.Error(w, "playlistId is required", http.StatusBadRequest)
		return
	}

	source, ok := h.Registry.GetSource("plex")
	if !ok {
		http.Error(w, "source adapter not available", http.StatusInternalServerError)
		return
	}
	target, ok := h.Registry.GetTarget(defaultTargetID)
	if !ok {
		http.Error(w, "target adapter not available", http.StatusInternalServerError)
		return
	}

	jobID, err := db.CreateCrossImportJob(h.DB, user.ID, "plex", playlistID, defaultTargetID)
	if err != nil {
		http.Error(w, "failed to create job", http.StatusInternalServerError)
		return
	}

	sessionID := uuid.NewString()
	sess := h.Sessions.New(sessionID, jobID, defaultTargetID, adapters.TargetConfig{})
	sess.SetProgress(crossimport.Progress{Phase: crossimport.PhaseFetching})

	// The background job outlives this request (the handler returns well
	// before matching finishes), so it must not inherit r.Context() - that
	// gets cancelled the moment this response is written.
	h.Queue.Enqueue(user.ID, "Cross-import: "+playlistID, notifications.TypeImport, func(notificationID string) error {
		return h.runMatch(context.Background(), user.ID, jobID, playlistID, source, target, sess, notificationID)
	})

	h.Tmpl.RenderPartial(w, "partials/cross_import_progress.html", map[string]any{
		"SessionID": sessionID, "Progress": sess.GetProgress(),
	})
}

// runMatch is the background job body: fetch the source playlist's tracks,
// then run them through the target adapter's matcher, updating both the
// session (for the review page) and the notification feed (for the bell) as
// it goes. Ports the "Phase 1: fetch tracks" / "Phase 2: match tracks"
// sections of POST /api/cross-import/match.
func (h *CrossImportHandler) runMatch(ctx context.Context, userID, jobID int64, playlistID string, source adapters.SourceAdapter, target adapters.TargetAdapter, sess *crossimport.Session, notificationID string) error {
	sess.SetProgress(crossimport.Progress{Phase: crossimport.PhaseFetching})

	playlist, tracks, err := source.FetchTracks(ctx, playlistID, userID)
	if err != nil {
		sess.SetProgress(crossimport.Progress{Phase: crossimport.PhaseError, Message: err.Error()})
		_ = db.UpdateCrossImportJobStatus(h.DB, jobID, "failed")
		return err
	}
	_ = db.UpdateCrossImportJobPlaylist(h.DB, jobID, playlist.Name, len(tracks))
	sess.SetPlaylistName(playlist.Name)

	if sess.IsCancelled() {
		_ = db.DeleteCrossImportJob(h.DB, jobID)
		return nil
	}

	sess.SetProgress(crossimport.Progress{Phase: crossimport.PhaseMatching, Total: len(tracks), PlaylistName: playlist.Name})
	detail := "Matching tracks..."
	h.Notifications.Update(userID, notificationID, notifications.Patch{Detail: &detail})

	progress := func(current, total int) {
		sess.SetProgress(crossimport.Progress{Phase: crossimport.PhaseMatching, Current: current, Total: total, PlaylistName: playlist.Name})
	}
	results, err := target.MatchTracks(ctx, tracks, sess.TargetConfig, userID, progress, sess.IsCancelled)
	if err != nil {
		sess.SetProgress(crossimport.Progress{Phase: crossimport.PhaseError, Message: err.Error()})
		_ = db.UpdateCrossImportJobStatus(h.DB, jobID, "failed")
		return err
	}

	if sess.IsCancelled() {
		_ = db.DeleteCrossImportJob(h.DB, jobID)
		return nil
	}

	sess.SetResults(results)
	_ = db.UpdateCrossImportJobStatus(h.DB, jobID, "review")

	matched := 0
	for _, res := range results {
		if res.Matched {
			matched++
		}
	}
	status := notifications.StatusSuccess
	doneDetail := fmt.Sprintf("Matched %d of %d tracks - ready to review", matched, len(results))
	h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &doneDetail})
	return nil
}

func (h *CrossImportHandler) matchStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	sess, ok := h.Sessions.Get(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	progress := sess.GetProgress()
	if progress.Phase == crossimport.PhaseReview {
		h.renderReview(w, sessionID, sess)
		return
	}
	h.Tmpl.RenderPartial(w, "partials/cross_import_progress.html", map[string]any{
		"SessionID": sessionID, "Progress": progress,
	})
}

func (h *CrossImportHandler) cancelMatch(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	if sess, ok := h.Sessions.Get(sessionID); ok {
		sess.Cancel()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CrossImportHandler) trackIndex(r *http.Request) (int, error) {
	return strconv.Atoi(chi.URLParam(r, "idx"))
}

func (h *CrossImportHandler) toggleSkip(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	sess, ok := h.Sessions.Get(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	idx, err := h.trackIndex(r)
	if err != nil {
		http.Error(w, "invalid track index", http.StatusBadRequest)
		return
	}
	sess.Update(idx, func(m *adapters.MatchResult) { m.Skipped = !m.Skipped })
	h.renderReview(w, sessionID, sess)
}

// renderReview also tallies matched/unmatched/skipped counts for the review
// table's summary bar - Go templates have no arithmetic-over-a-slice
// primitive, so it's cheaper to count once here than to reimplement a loop
// three times in the template.
func (h *CrossImportHandler) renderReview(w http.ResponseWriter, sessionID string, sess *crossimport.Session) {
	results := sess.GetResults()
	matched, skipped := 0, 0
	for _, res := range results {
		switch {
		case res.Skipped:
			skipped++
		case res.Matched:
			matched++
		}
	}
	h.Tmpl.RenderPartial(w, "partials/cross_import_review.html", map[string]any{
		"SessionID": sessionID, "Results": results, "PlaylistName": sess.GetPlaylistName(),
		"MatchedCount": matched, "SkippedCount": skipped,
		"UnmatchedCount": len(results) - matched - skipped, "TotalCount": len(results),
	})
}

// searchCandidates lets the review page re-search the target catalog for a
// track the automatic match got wrong, mirroring POST /api/cross-import/search.
func (h *CrossImportHandler) searchCandidates(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	sess, ok := h.Sessions.Get(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	idx, err := h.trackIndex(r)
	if err != nil {
		http.Error(w, "invalid track index", http.StatusBadRequest)
		return
	}
	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "q is required", http.StatusBadRequest)
		return
	}
	target, ok := h.Registry.GetTarget(sess.TargetID)
	if !ok {
		http.Error(w, "target adapter not available", http.StatusInternalServerError)
		return
	}
	user := auth.CurrentUser(r)
	results, err := target.SearchCatalog(r.Context(), query, user.ID, false, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	h.Tmpl.RenderPartial(w, "partials/cross_import_candidates.html", map[string]any{
		"SessionID": sessionID, "Index": idx, "Candidates": results,
	})
}

func (h *CrossImportHandler) selectCandidate(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	sess, ok := h.Sessions.Get(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	idx, err := h.trackIndex(r)
	if err != nil {
		http.Error(w, "invalid track index", http.StatusBadRequest)
		return
	}
	trackID := r.FormValue("targetTrackId")
	title := r.FormValue("targetTitle")
	artist := r.FormValue("targetArtist")
	if trackID == "" {
		http.Error(w, "targetTrackId is required", http.StatusBadRequest)
		return
	}
	sess.Update(idx, func(m *adapters.MatchResult) {
		m.TargetTrackID = trackID
		m.TargetTitle = title
		m.TargetArtist = artist
		m.Matched = true
		m.Skipped = false
		m.Confidence = 100
	})
	h.renderReview(w, sessionID, sess)
}

// execute creates the target playlist from the session's current (possibly
// user-edited) results, ports POST /api/cross-import/execute.
func (h *CrossImportHandler) execute(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	sessionID := chi.URLParam(r, "sessionId")
	sess, ok := h.Sessions.Get(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	playlistName := strings.TrimSpace(r.FormValue("playlistName"))
	if playlistName == "" {
		http.Error(w, "playlistName is required", http.StatusBadRequest)
		return
	}
	target, ok := h.Registry.GetTarget(sess.TargetID)
	if !ok {
		http.Error(w, "target adapter not available", http.StatusInternalServerError)
		return
	}
	results := sess.GetResults()
	jobID := sess.JobID

	h.Queue.Enqueue(user.ID, "Import: "+playlistName, notifications.TypeImport, func(notificationID string) error {
		_, name, _, err := target.CreatePlaylist(context.Background(), playlistName, results, sess.TargetConfig, user.ID)
		if err != nil {
			_ = db.UpdateCrossImportJobStatus(h.DB, jobID, "failed")
			return err
		}

		matched, unmatched, skipped := 0, 0, 0
		var unmatchedTracks []map[string]string
		for _, res := range results {
			switch {
			case res.Skipped:
				skipped++
			case res.Matched:
				matched++
			default:
				unmatched++
				unmatchedTracks = append(unmatchedTracks, map[string]string{
					"title": res.SourceTrack.Title, "artist": res.SourceTrack.Artist,
				})
			}
		}
		unmatchedJSON, _ := json.Marshal(unmatchedTracks)
		if err := db.CompleteCrossImportJob(h.DB, jobID, user.ID, name, matched, unmatched, skipped, len(results), string(unmatchedJSON)); err != nil {
			return err
		}

		status := notifications.StatusSuccess
		detail := fmt.Sprintf("Imported %d tracks", matched)
		pct := 100
		h.Notifications.Update(user.ID, notificationID, notifications.Patch{Status: &status, Detail: &detail, Progress: &pct})
		h.Sessions.Delete(sessionID)
		return nil
	})

	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{
		"Notifications": h.Notifications.List(user.ID),
	})
}

func (h *CrossImportHandler) history(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	jobs, err := db.GetCrossImportJobs(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.Tmpl.RenderPartial(w, "partials/cross_import_history.html", map[string]any{"Jobs": jobs})
}

// oauthStart/oauthCallback port GET /oauth/:service and its callback for
// whichever target adapters implement OAuthCapable (currently just
// YouTube - see main.go's registry wiring).
func (h *CrossImportHandler) oauthStart(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	service := chi.URLParam(r, "service")
	target, ok := h.Registry.GetTarget(service)
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	oauth, ok := target.(adapters.OAuthCapable)
	if !ok {
		http.Error(w, "service does not support OAuth", http.StatusBadRequest)
		return
	}
	redirectURI := fmt.Sprintf("%s://%s/cross-import/oauth/%s/callback", schemeOf(r), r.Host, service)
	authURL, err := oauth.GetOAuthURL(r.Context(), user.ID, redirectURI)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (h *CrossImportHandler) oauthCallback(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	service := chi.URLParam(r, "service")
	target, ok := h.Registry.GetTarget(service)
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	oauth, ok := target.(adapters.OAuthCapable)
	if !ok {
		http.Error(w, "service does not support OAuth", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	redirectURI := fmt.Sprintf("%s://%s/cross-import/oauth/%s/callback", schemeOf(r), r.Host, service)
	if err := oauth.HandleOAuthCallback(r.Context(), code, user.ID, redirectURI); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/cross-import", http.StatusFound)
}

// oauthServices ports GET /api/cross-import/targets' connection-status list
// (the bit of it Settings/Cross-Import actually need - name + connected):
// every registered target that supports OAuth, with whether the current
// user has a valid connection. Shared by both Settings' "Connected
// Services" section and Cross-Import's reconnect/disconnect control so
// there's one source of truth for "what's connected" instead of two.
type oauthServiceStatus struct {
	ID, Name  string
	Connected bool
}

func (h *CrossImportHandler) oauthServices(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	var services []oauthServiceStatus
	for _, target := range h.Registry.ListTargets() {
		oauth, ok := target.(adapters.OAuthCapable)
		if !ok {
			continue
		}
		connected, _ := oauth.HasValidConnection(r.Context(), user.ID)
		services = append(services, oauthServiceStatus{ID: target.Meta().ID, Name: target.Meta().Name, Connected: connected})
	}
	h.Tmpl.RenderPartial(w, "partials/oauth_services.html", map[string]any{"Services": services})
}

// oauthRevoke ports DELETE /api/cross-import/oauth/:service - removes the
// stored OAuth connection, then re-renders the same services list so
// whichever page called it (Settings or Cross-Import) sees the updated
// connect/disconnect state immediately.
func (h *CrossImportHandler) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	service := chi.URLParam(r, "service")
	target, ok := h.Registry.GetTarget(service)
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	oauth, ok := target.(adapters.OAuthCapable)
	if !ok {
		http.Error(w, "service does not support OAuth revocation", http.StatusBadRequest)
		return
	}
	if err := oauth.RevokeConnection(r.Context(), user.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.oauthServices(w, r)
}

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
