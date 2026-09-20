// Preview/review/confirm import flow, ported from routes/import.ts's
// POST /preview, /match, and /confirm (ImportPage.tsx's handlePreview/
// handleConfirmUpdated - the real caller of all three, on every chart/
// search/URL import in the app, not an edge case). Until this, submitting
// an import ran importsvc.ImportPlaylist and created the Plex playlist
// immediately with no chance to see match results or fix a wrong/missing
// match first.
package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/importreview"
	"github.com/drevilish/playlist-lab/internal/services/importsvc"
	"github.com/drevilish/playlist-lab/internal/services/matching"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

func RegisterImportReview(r chi.Router, mw *auth.Middleware, h *ImportHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Post("/import/preview", h.preview)
		r.Get("/import/preview/{sessionId}/status", h.previewStatus)
		r.Get("/import/review/{sessionId}/track/{idx}/search", h.reviewTrackSearch)
		r.Post("/import/review/{sessionId}/track/{idx}/select", h.reviewTrackSelect)
		r.Post("/import/review/{sessionId}/track/{idx}/skip", h.reviewTrackSkip)
		r.Post("/import/review/{sessionId}/track/{idx}/move", h.reviewTrackMove)
		r.Get("/import/review/{sessionId}/export", h.reviewExportUnmatched)
		r.Post("/import/confirm/{sessionId}", h.confirmImport)
	})
}

// preview ports POST /api/import/preview + /api/import/match in one step:
// fetch (or re-use importsvc.ImportPlaylist's own stale-cache fallback) and
// match sourceIdentifier's tracks against Plex. The fetch/match itself runs
// in a background goroutine (importPreviewJob below) so this handler can
// return immediately with a progress panel that polls
// GET /import/preview/{sessionId}/status once a second until it's done -
// same 1s-polling idea as cross-import's own progress panel
// (cross_import.go's startMatch/matchStatus), chosen over both SSE (this
// app reserves that for the notification bell) and the shared actionqueue
// (that would queue a click-and-see-results preview behind unrelated
// background jobs from other users - a real regression for the single
// most common action on this page). Renders progress/review, never
// creates anything yet.
func (h *ImportHandler) preview(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	_ = r.ParseForm()
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

	dbUser, err := db.GetUserByID(h.DB, user.ID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	userServer, err := db.GetUserMusicServer(h.DB, user.ID)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "No music library selected. Please select a library first.", http.StatusBadRequest)
		return
	}
	client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(dbUser.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")

	sess := h.Reviews.NewBare(source, identifier, user.ID)
	sess.SetProgress(importreview.Progress{Phase: "scraping"})

	// The background job outlives this request (the handler returns well
	// before the fetch/match finishes), so it must not inherit r.Context()
	// - that gets cancelled the moment this response is written, same
	// gotcha cross_import.go's startMatch calls out for its own job.
	go h.runPreview(context.Background(), user.ID, userServer.LibraryID.String, client, source, identifier, customName, sess)

	h.Tmpl.RenderPartial(w, "partials/import_review_modal.html", map[string]any{
		"SessionID": sess.ID, "Progress": sess.GetProgress(),
	})
}

// runPreview is preview's background job body: fetch + match, reporting
// progress into sess as it goes, then filling in the session's
// playlist-name/cover/tracks once done so previewStatus can hand off to the
// review partial.
func (h *ImportHandler) runPreview(ctx context.Context, userID int64, libraryID string, client *plex.Client, source, identifier, customName string, sess *importreview.Session) {
	progress := func(phase string, current, total int) {
		sess.SetProgress(importreview.Progress{Phase: phase, Current: current, Total: total})
	}
	result, err := importsvc.ImportPlaylist(ctx, h.Registry, h.DB, client, source, identifier, importsvc.Options{
		UserID: userID, LibraryID: libraryID, CustomName: customName,
	}, progress, func() bool { return false })
	if err != nil {
		slog.Error("preview: fetch/match failed", "error", err, "source", source)
		sess.SetProgress(importreview.Progress{Phase: "error", Message: err.Error()})
		return
	}

	sess.SetPlaylistName(result.PlaylistName)
	sess.SetCoverURL(result.CoverURL)
	sess.SetTracks(importreview.FromMatchedTracks(result.Matched, result.Unmatched))
	sess.SetProgress(importreview.Progress{Phase: "review"})
}

// previewStatus is the progress panel's poll target, mirroring
// cross_import.go's matchStatus: hand off to the review partial once
// runPreview reaches the "review" phase, otherwise re-render progress.
func (h *ImportHandler) previewStatus(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	sess, ok := h.Reviews.Get(chi.URLParam(r, "sessionId"), user.ID)
	if !ok {
		http.Error(w, "review session not found or expired - try importing again", http.StatusNotFound)
		return
	}
	if sess.GetProgress().Phase == "review" {
		h.renderReview(w, sess)
		return
	}
	h.Tmpl.RenderPartial(w, "partials/import_preview_progress.html", map[string]any{
		"SessionID": sess.ID, "Progress": sess.GetProgress(),
	})
}

func (h *ImportHandler) renderReview(w http.ResponseWriter, sess *importreview.Session) {
	total, matched, unmatched, skipped := sess.Counts()
	h.Tmpl.RenderPartial(w, "partials/import_review.html", map[string]any{
		"SessionID": sess.ID, "PlaylistName": sess.PlaylistName(), "Tracks": sess.Tracks(),
		"TotalCount": total, "MatchedCount": matched, "UnmatchedCount": unmatched, "SkippedCount": skipped,
	})
}

func (h *ImportHandler) reviewSession(w http.ResponseWriter, r *http.Request) (*importreview.Session, int, bool) {
	sess, ok := h.Reviews.Get(chi.URLParam(r, "sessionId"), auth.CurrentUser(r).ID)
	if !ok {
		http.Error(w, "review session not found or expired - try importing again", http.StatusNotFound)
		return nil, 0, false
	}
	idx, err := strconv.Atoi(chi.URLParam(r, "idx"))
	if err != nil {
		http.Error(w, "invalid track index", http.StatusBadRequest)
		return nil, 0, false
	}
	return sess, idx, true
}

// reviewTrackSearch is the review row's "Search" disclosure, backed by the
// same plex.Client.SearchTrack/track_search_results.html plumbing
// playlists.go's editor search box uses - reused here as a plain read-only
// candidate list rather than duplicating the search-and-render logic for a
// second time in this codebase.
func (h *ImportHandler) reviewTrackSearch(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	sess, idx, ok := h.reviewSession(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		h.Tmpl.RenderPartial(w, "partials/import_review_candidates.html", map[string]any{"SessionID": sess.ID, "Index": idx})
		return
	}

	userServer, err := db.GetUserMusicServer(h.DB, user.ID)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	dbUser, err := db.GetUserByID(h.DB, user.ID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(dbUser.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")
	tracks, err := client.SearchTrack(q, userServer.LibraryID.String, "", "")
	if err != nil {
		http.Error(w, "search failed", http.StatusBadGateway)
		return
	}
	if len(tracks) > 20 {
		tracks = tracks[:20]
	}
	h.Tmpl.RenderPartial(w, "partials/import_review_candidates.html", map[string]any{
		"SessionID": sess.ID, "Index": idx, "Results": toTrackRows(tracks),
	})
}

func (h *ImportHandler) reviewTrackSelect(w http.ResponseWriter, r *http.Request) {
	sess, idx, ok := h.reviewSession(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	ratingKey, title, trackArtist, album := r.FormValue("ratingKey"), r.FormValue("title"), r.FormValue("artist"), r.FormValue("album")
	if ratingKey == "" {
		http.Error(w, "ratingKey is required", http.StatusBadRequest)
		return
	}
	if !sess.Update(idx, func(t *importreview.Track) {
		t.Matched, t.Skipped = true, false
		t.PlexRatingKey, t.PlexTitle, t.PlexArtist, t.PlexAlbum = ratingKey, title, trackArtist, album
	}) {
		http.Error(w, "invalid track index", http.StatusBadRequest)
		return
	}
	h.renderReview(w, sess)
}

func (h *ImportHandler) reviewTrackSkip(w http.ResponseWriter, r *http.Request) {
	sess, idx, ok := h.reviewSession(w, r)
	if !ok {
		return
	}
	if !sess.Update(idx, func(t *importreview.Track) { t.Skipped = !t.Skipped }) {
		http.Error(w, "invalid track index", http.StatusBadRequest)
		return
	}
	h.renderReview(w, sess)
}

// reviewTrackMove backs the review list's drag-to-reorder/move buttons
// (import_review.html's wireDragReorder script): idx is the track being
// moved, afterIndex (form value, -1 meaning "move to the front") is where
// - both are positions in the list as it was last rendered, same
// stable-until-the-next-render contract as playlists.go's moveTrack uses
// with a Plex item id instead of a position.
func (h *ImportHandler) reviewTrackMove(w http.ResponseWriter, r *http.Request) {
	sess, idx, ok := h.reviewSession(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	afterIndex, err := strconv.Atoi(r.FormValue("afterIndex"))
	if err != nil {
		http.Error(w, "invalid afterIndex", http.StatusBadRequest)
		return
	}
	if !sess.Move(idx, afterIndex) {
		http.Error(w, "invalid move", http.StatusBadRequest)
		return
	}
	h.renderReview(w, sess)
}

// reviewExportUnmatched is the review screen's "Export missing tracks"
// button - same encoding/csv-straight-to-ResponseWriter pattern as
// missing.go's exportCSV, just scoped to this one session's unmatched
// tracks instead of the whole Missing Tracks list.
func (h *ImportHandler) reviewExportUnmatched(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	sess, ok := h.Reviews.Get(chi.URLParam(r, "sessionId"), user.ID)
	if !ok {
		http.Error(w, "review session not found or expired - try importing again", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="unmatched-tracks.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"Title", "Artist", "Album"})
	for _, t := range sess.Tracks() {
		if t.Skipped || t.Matched {
			continue
		}
		_ = cw.Write([]string{t.Title, t.Artist, t.Album})
	}
	cw.Flush()
}

// confirmImport ports POST /api/import/confirm: create the Plex playlist
// from the review session's current (possibly hand-edited) match state.
// Synchronous, matching the original - this is one CreatePlaylist call plus
// one batched AddToPlaylist, not the slow scrape+match step preview()
// already finished.
func (h *ImportHandler) confirmImport(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	sessionID := chi.URLParam(r, "sessionId")
	sess, ok := h.Reviews.Get(sessionID, user.ID)
	if !ok {
		http.Error(w, "review session not found or expired - try importing again", http.StatusNotFound)
		return
	}
	_ = r.ParseForm()
	playlistName := strings.Join(strings.Fields(r.FormValue("playlistName")), " ")
	if playlistName == "" {
		http.Error(w, "playlistName is required", http.StatusBadRequest)
		return
	}
	overwriteExisting := r.FormValue("overwriteExisting") == "true"
	keepExistingCover := r.FormValue("keepExistingCover") == "true"

	dbUser, err := db.GetUserByID(h.DB, user.ID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	userServer, err := db.GetUserMusicServer(h.DB, user.ID)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "No music library selected.", http.StatusBadRequest)
		return
	}
	client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(dbUser.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")

	var matched, unmatched []matching.MatchedTrack
	for _, t := range sess.Tracks() {
		if t.Skipped {
			continue
		}
		if !t.Matched {
			unmatched = append(unmatched, matching.MatchedTrack{Title: t.Title, Artist: t.Artist, Album: t.Album})
			continue
		}
		matched = append(matched, matching.MatchedTrack{
			Title: t.Title, Artist: t.Artist, Album: t.Album,
			Matched: true, PlexRatingKey: t.PlexRatingKey,
			PlexTitle: t.PlexTitle, PlexArtist: t.PlexArtist, PlexAlbum: t.PlexAlbum,
		})
	}
	if len(matched) == 0 {
		http.Error(w, "no tracks matched (or all were skipped) - nothing to add to a playlist", http.StatusBadRequest)
		return
	}

	existingCoverURL := ""
	if overwriteExisting {
		existingCoverURL = h.overwriteExistingPlaylist(client, playlistName, keepExistingCover, userServer, dbUser)
	}

	coverURL := sess.CoverURL()
	if keepExistingCover && existingCoverURL != "" {
		coverURL = existingCoverURL
	}

	result := &importsvc.Result{PlaylistName: playlistName, Source: sess.Source, Matched: matched, Unmatched: unmatched, MatchedCount: len(matched), TotalCount: len(matched) + len(unmatched), CoverURL: coverURL}
	dbPlaylistID, _, err := importsvc.FinalizeImportResult(h.DB, client, sess.Source, sess.SourceIdentifier, user.ID,
		userServer.ServerClientID, userServer.LibraryID.String, result, importsvc.FinalizeOpts{PlaylistName: playlistName})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	slog.Info("[Import] Created playlist from review", "playlistId", dbPlaylistID, "source", sess.Source, "matched", len(matched))
	h.Reviews.Delete(sessionID)

	status, pct := notifications.StatusSuccess, 100
	detail := fmt.Sprintf("Created %q - added %d tracks", playlistName, len(matched))
	h.Notifications.Add(user.ID, notifications.TypeImport, playlistName, detail, status, &pct)
	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{"Notifications": h.Notifications.List(user.ID)})
}

// overwriteExistingPlaylist deletes every existing Plex playlist whose
// name normalizes (trim, collapse whitespace, lowercase) to the same
// value as the new one, matching import.ts's own normalize-compare-delete-
// all-matches loop (deliberately deleting every match, not just the first,
// since duplicates can and do accumulate). Returns the first match's
// composite-thumb URL (with this user's token attached) if the caller
// wants to keep it - "" if there was no existing playlist, or no cover to
// keep.
func (h *ImportHandler) overwriteExistingPlaylist(client *plex.Client, playlistName string, keepCover bool, userServer *db.UserServer, dbUser *db.User) string {
	normalize := func(s string) string {
		return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(s)), " "))
	}
	target := normalize(playlistName)

	playlists, err := client.GetPlaylists()
	if err != nil {
		slog.Warn("overwrite: failed to list existing playlists", "error", err)
		return ""
	}
	existingCoverURL := ""
	for _, p := range playlists {
		if p.PlaylistType != "audio" || normalize(p.Title) != target {
			continue
		}
		if keepCover && existingCoverURL == "" && p.Composite != "" {
			token := plex.ResolveToken(dbUser.PlexToken, userServer.AccessToken.String)
			existingCoverURL = userServer.ServerURL + p.Composite + "?X-Plex-Token=" + token
		}
		if err := client.DeletePlaylist(p.RatingKey); err != nil {
			slog.Warn("overwrite: failed to delete an existing playlist", "error", err, "ratingKey", p.RatingKey)
		}
	}
	return existingCoverURL
}
