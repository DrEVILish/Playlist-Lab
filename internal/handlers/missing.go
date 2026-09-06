// Package handlers: missing.go ports routes/missing.ts to HTMX - the
// "missing tracks" page: tracks that matched in the source but couldn't be
// found on Plex, offering to retry matching, manually rematch, seed a
// sonically-similar replacement, or hand them off to deemix/Lidarr for
// acquisition.
//
// Retrying a large batch does a real Plex search per track and can take
// well over a minute, so (matching missing.ts) a retry runs detached from
// the request that started it; the client polls GET /missing/retry-status
// to watch progress. Deemix/Lidarr downloads and "Deemix All" instead run
// through the shared actionqueue+notifications pattern every other
// background job in this app uses.
package handlers

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	deemixsvc "github.com/drevilish/playlist-lab/internal/services/deemix"
	lidarrsvc "github.com/drevilish/playlist-lab/internal/services/lidarr"
	"github.com/drevilish/playlist-lab/internal/services/matching"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

type MissingHandler struct {
	DB            *sql.DB
	PlexAuth      *auth.PlexClient
	Tmpl          *Templates
	Notifications *notifications.Store
	Queue         *actionqueue.Queue
	Deemix        *deemixsvc.Service
	Lidarr        *lidarrsvc.Service

	mu            sync.Mutex
	activeRetries map[int64]*retryProgress
	pending       map[int64]map[int64]db.MissingTrack
}

// retryProgress is exported-field so html/template (used to render
// partials/missing_retry_status.html) can read it directly.
type retryProgress struct {
	Current, Total int
	Err            string
}

func RegisterMissing(r chi.Router, mw *auth.Middleware, h *MissingHandler) {
	h.activeRetries = map[int64]*retryProgress{}
	h.pending = map[int64]map[int64]db.MissingTrack{}

	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/missing", h.page)
		r.Get("/missing/list", h.list)
		r.Get("/missing/retry-status", h.retryStatus)
		r.Post("/missing/retry", h.retry)
		r.Post("/missing/deemix-all", h.deemixAll)
		r.Post("/missing/{id}/deemix-download", h.deemixDownload)
		r.Post("/missing/{id}/lidarr-download", h.lidarrDownload)
		r.Post("/missing/{id}/rematch", h.rematch)
		r.Post("/missing/{id}/replace-similar", h.replaceSimilar)
		r.Delete("/missing/{id}", h.deleteTrack)
		r.Delete("/missing/playlist/{playlistId}", h.clearPlaylist)
	})
}

// client builds a plex.Client for the current user's selected server,
// matching PlaylistsHandler.client.
func (h *MissingHandler) client(user *db.User) (*plex.Client, *db.UserServer, error) {
	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil {
		return nil, userServer, err
	}
	token := plex.ResolveToken(user.PlexToken, userServer.AccessToken.String)
	return plex.NewClient(userServer.ServerURL, token, h.PlexAuth.ClientID, "Playlist Lab"), userServer, nil
}

func (h *MissingHandler) matchingSettings(userID int64) matching.Settings {
	raw, _ := db.GetMatchingSettingsJSON(h.DB, userID)
	return matching.SettingsFromJSON(raw)
}

type missingGroup struct {
	PlaylistID   int64
	PlaylistName string
	Source       string
	Tracks       []db.MissingTrack
}

func groupMissingTracks(dbConn *sql.DB, tracks []db.MissingTrack) []missingGroup {
	order := []int64{}
	byPlaylist := map[int64]*missingGroup{}
	for _, t := range tracks {
		g, ok := byPlaylist[t.PlaylistID]
		if !ok {
			name := "Unknown"
			if p, _ := db.GetPlaylistByID(dbConn, t.PlaylistID); p != nil {
				name = p.Name
			}
			g = &missingGroup{PlaylistID: t.PlaylistID, PlaylistName: name, Source: t.Source}
			byPlaylist[t.PlaylistID] = g
			order = append(order, t.PlaylistID)
		}
		g.Tracks = append(g.Tracks, t)
	}
	out := make([]missingGroup, 0, len(order))
	for _, id := range order {
		out = append(out, *byPlaylist[id])
	}
	return out
}

// page renders GET /missing - the list itself loads via hx-trigger="load"
// against GET /missing/list, matching cross_import.html's pattern.
func (h *MissingHandler) page(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	h.Tmpl.RenderPage(w, "missing", map[string]any{"User": user})
}

// list renders GET /missing/list: every missing track grouped by playlist.
func (h *MissingHandler) list(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	h.renderGroups(w, user.ID)
}

func (h *MissingHandler) renderRetryStatus(w http.ResponseWriter, userID int64) {
	h.mu.Lock()
	progress := h.activeRetries[userID]
	h.mu.Unlock()
	h.Tmpl.RenderPartial(w, "partials/missing_retry_status.html", map[string]any{"Progress": progress})
}

func (h *MissingHandler) retryStatus(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	h.renderRetryStatus(w, user.ID)
}

// retry ports POST /api/missing/retry: retries either one playlist's
// tracks, a specific set, or everything, in the background.
func (h *MissingHandler) retry(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	_ = r.ParseForm()

	all, err := db.GetUserMissingTracks(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var toRetry []db.MissingTrack
	if tid := r.FormValue("trackId"); tid != "" {
		id, _ := strconv.ParseInt(tid, 10, 64)
		for _, t := range all {
			if t.ID == id {
				toRetry = append(toRetry, t)
			}
		}
	} else if pid := r.FormValue("playlistId"); pid != "" {
		id, _ := strconv.ParseInt(pid, 10, 64)
		for _, t := range all {
			if t.PlaylistID == id {
				toRetry = append(toRetry, t)
			}
		}
	} else {
		toRetry = all
	}

	if len(toRetry) == 0 {
		h.renderRetryStatus(w, user.ID)
		return
	}

	dbUser, err := db.GetUserByID(h.DB, user.ID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil {
		http.Error(w, "No server selected. Please select a server first.", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	existing := h.activeRetries[user.ID]
	if existing != nil && existing.Err != "" {
		delete(h.activeRetries, user.ID)
		existing = nil
	}
	if existing != nil {
		queue := h.pending[user.ID]
		if queue == nil {
			queue = map[int64]db.MissingTrack{}
			h.pending[user.ID] = queue
		}
		for _, t := range toRetry {
			queue[t.ID] = t
		}
		h.mu.Unlock()
		h.renderRetryStatus(w, user.ID)
		return
	}
	h.mu.Unlock()

	go h.startRetryChain(user.ID, dbUser, userServer, toRetry)
	h.renderRetryStatus(w, user.ID)
}

func (h *MissingHandler) retryBatchTitle(tracks []db.MissingTrack) string {
	seen := map[int64]bool{}
	var ids []int64
	for _, t := range tracks {
		if !seen[t.PlaylistID] {
			seen[t.PlaylistID] = true
			ids = append(ids, t.PlaylistID)
		}
	}
	if len(ids) > 1 {
		return fmt.Sprintf("Matching %d playlists", len(ids))
	}
	if len(ids) == 1 {
		if p, _ := db.GetPlaylistByID(h.DB, ids[0]); p != nil {
			return "Matching: " + p.Name
		}
	}
	return "Matching missing tracks"
}

// startRetryChain runs one retry batch, then - if more tracks were queued
// while it ran - immediately runs another for those, matching missing.ts's
// startRetryChain. Meant to run in its own goroutine.
func (h *MissingHandler) startRetryChain(userID int64, user *db.User, userServer *db.UserServer, tracks []db.MissingTrack) {
	h.mu.Lock()
	h.activeRetries[userID] = &retryProgress{Total: len(tracks)}
	h.mu.Unlock()

	notification := h.Notifications.Add(userID, notifications.TypeRetryMatch, h.retryBatchTitle(tracks), fmt.Sprintf("0 of %d", len(tracks)), notifications.StatusInProgress, nil)

	totalAttempted, totalMatched := 0, 0
	batch := tracks
	for len(batch) > 0 {
		totalAttempted += len(batch)
		matched, err := h.runRetryBatch(userID, user, userServer, batch, notification.ID)
		if err != nil {
			slog.Error("Missing-track retry chain failed", "error", err, "userId", userID)
			h.mu.Lock()
			progress := h.activeRetries[userID]
			if progress == nil {
				progress = &retryProgress{Total: len(tracks)}
			}
			progress.Err = err.Error()
			h.activeRetries[userID] = progress
			delete(h.pending, userID)
			h.mu.Unlock()
			status, detail := notifications.StatusError, err.Error()
			h.Notifications.Update(userID, notification.ID, notifications.Patch{Status: &status, Detail: &detail})
			return
		}
		totalMatched += matched

		h.mu.Lock()
		queue := h.pending[userID]
		delete(h.pending, userID)
		h.mu.Unlock()
		if len(queue) == 0 {
			break
		}

		stillMissing, _ := db.GetUserMissingTracks(h.DB, userID)
		stillMissingIDs := map[int64]bool{}
		for _, t := range stillMissing {
			stillMissingIDs[t.ID] = true
		}
		batch = nil
		for _, t := range queue {
			if stillMissingIDs[t.ID] {
				batch = append(batch, t)
			}
		}
		if len(batch) > 0 {
			h.mu.Lock()
			h.activeRetries[userID] = &retryProgress{Total: len(batch)}
			h.mu.Unlock()
			// missing.ts renames the notification's title to the queued
			// follow-up batch's playlist here - notifications.Patch has no
			// Title field yet (nothing else needed one), so the title is
			// left as the first batch's for now.
		}
	}

	h.mu.Lock()
	delete(h.activeRetries, userID)
	h.mu.Unlock()
	status := notifications.StatusSuccess
	detail := fmt.Sprintf("Matched %d of %d", totalMatched, totalAttempted)
	progressPct := 100
	h.Notifications.Update(userID, notification.ID, notifications.Patch{Status: &status, Progress: &progressPct, Detail: &detail})
}

// runRetryBatch matches and re-adds one batch of missing tracks, mirroring
// missing.ts's runRetryInBackground.
func (h *MissingHandler) runRetryBatch(userID int64, user *db.User, userServer *db.UserServer, tracks []db.MissingTrack, notificationID string) (int, error) {
	sourceTracks := make([]matching.Track, len(tracks))
	for i, t := range tracks {
		sourceTracks[i] = matching.Track{Title: t.Title, Artist: t.Artist, Album: t.Album.String}
	}

	manualMatches, _ := db.GetUserManualMatches(h.DB, userID)
	remembered := make([]matching.RememberedMatch, len(manualMatches))
	for i, m := range manualMatches {
		remembered[i] = matching.RememberedMatch{Title: m.Title, Artist: m.Artist, Album: m.Album.String, PlexRatingKey: m.PlexRatingKey}
	}
	rememberedMap := matching.BuildRememberedMatchMap(remembered)

	client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(user.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")
	progress := func(current, total int) {
		h.mu.Lock()
		h.activeRetries[userID] = &retryProgress{Current: current, Total: total}
		h.mu.Unlock()
		detail := fmt.Sprintf("%d of %d", current, total)
		pct := 0
		if total > 0 {
			pct = current * 100 / total
		}
		h.Notifications.Update(userID, notificationID, notifications.Patch{Progress: &pct, Detail: &detail})
	}

	settings := h.matchingSettings(userID)
	matched, err := matching.MatchPlaylist(sourceTracks, client, userServer.LibraryID.String, settings, progress, nil, rememberedMap)
	if err != nil {
		return 0, err
	}
	matching.RememberMatches(h.DB, userID, matched)

	target := matching.PlaylistTarget{ServerClientID: userServer.ServerClientID, LibraryID: userServer.LibraryID.String}
	matchedCount := 0
	for i, m := range matched {
		if m.Matched && m.PlexRatingKey != "" {
			if matching.InsertMatchedTrackIntoPlaylist(h.DB, client, target, tracks[i], m.PlexRatingKey) {
				matchedCount++
			}
		}
	}
	return matchedCount, nil
}

// deemixDownload ports POST /:id/deemix-download: search deemix for this
// track and queue the top match for download.
func (h *MissingHandler) deemixDownload(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	trackID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid track id", http.StatusBadRequest)
		return
	}
	track, ok := h.findMissingTrack(user.ID, trackID)
	if !ok {
		http.Error(w, "missing track not found", http.StatusNotFound)
		return
	}

	dbUser, _ := db.GetUserByID(h.DB, user.ID)
	userServer, _ := db.GetUserServer(h.DB, user.ID)
	settings := h.matchingSettings(user.ID)

	result, err := h.queueDeemixForTrack(user.ID, dbUser, userServer, track, settings)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to queue deemix download: %s", err), http.StatusInternalServerError)
		return
	}
	if result == nil {
		http.Error(w, fmt.Sprintf("No deemix match found for %q - %q", track.Artist, track.Title), http.StatusNotFound)
		return
	}
	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{"Notifications": h.Notifications.List(user.ID)})
}

type deemixQueueResult struct {
	Title, Artist string
	Score         float64
}

// queueDeemixForTrack searches deemix for one missing track and queues its
// best match, if there is one good enough - shared by the single-track
// route and the "Deemix All" batch, mirroring missing.ts's
// queueDeemixForMissingTrack.
func (h *MissingHandler) queueDeemixForTrack(userID int64, user *db.User, userServer *db.UserServer, track db.MissingTrack, settings matching.Settings) (*deemixQueueResult, error) {
	firstArtist := firstArtistName(track.Artist)
	matches, err := h.Deemix.FindBestMatches(track.Title, firstArtist, settings, 1)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	best := matches[0]

	downloadURL := h.Deemix.ResolveDownloadURL(best.Match)
	isFullAlbum := downloadURL != best.Match.Link
	queued, err := h.Deemix.QueueDownload(downloadURL, best.Match.Link)
	if err != nil {
		return nil, err
	}

	detail := best.Match.Artist.Name
	if isFullAlbum {
		detail += " · full album"
	}
	detail += fmt.Sprintf(" · %d%% match", int(best.Score+0.5))

	var reconcile *deemixsvc.ReconcileContext
	if user != nil && userServer != nil {
		reconcile = &deemixsvc.ReconcileContext{
			MissingTrackID: track.ID, ServerURL: userServer.ServerURL,
			PlexToken: plex.ResolveToken(user.PlexToken, userServer.AccessToken.String),
			LibraryID: userServer.LibraryID.String, ServerClientID: userServer.ServerClientID,
		}
	}
	h.Deemix.StartDownload(deemixsvc.DownloadRequest{
		UserID: userID, Title: track.Title, Detail: detail, UUID: queued.UUID,
		AlreadyQueued: queued.AlreadyQueued, Reconcile: reconcile,
	})

	slog.Info("Queued deemix download for missing track", "userId", userID, "trackId", track.ID, "score", best.Score, "alreadyQueued", queued.AlreadyQueued)
	return &deemixQueueResult{Title: best.Match.Title, Artist: best.Match.Artist.Name, Score: best.Score}, nil
}

func firstArtistName(artist string) string {
	for _, sep := range []string{",", "&", "/"} {
		if idx := strings.Index(artist, sep); idx != -1 {
			artist = artist[:idx]
		}
	}
	return strings.TrimSpace(artist)
}

// deemixAll ports POST /deemix-all: retry matching against Plex, then queue
// every still-missing track (of one playlist, or the whole library) for
// download, through the shared action queue.
func (h *MissingHandler) deemixAll(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	_ = r.ParseForm()
	var playlistID int64
	if pid := r.FormValue("playlistId"); pid != "" {
		playlistID, _ = strconv.ParseInt(pid, 10, 64)
	}

	dbUser, err := db.GetUserByID(h.DB, user.ID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil {
		http.Error(w, "No server selected. Please select a server first.", http.StatusBadRequest)
		return
	}
	settings := h.matchingSettings(user.ID)

	missingFor := func() []db.MissingTrack {
		all, _ := db.GetUserMissingTracks(h.DB, user.ID)
		if playlistID == 0 {
			return all
		}
		var out []db.MissingTrack
		for _, t := range all {
			if t.PlaylistID == playlistID {
				out = append(out, t)
			}
		}
		return out
	}
	title := "Deemix All"
	if playlistID != 0 {
		if p, _ := db.GetPlaylistByID(h.DB, playlistID); p != nil {
			title = "Deemix All: " + p.Name
		}
	}

	h.Queue.Enqueue(user.ID, title, notifications.TypeDeemix, func(notificationID string) error {
		toRetry := missingFor()
		if len(toRetry) > 0 {
			detail := fmt.Sprintf("Matching %d track(s) against Plex first...", len(toRetry))
			h.Notifications.Update(user.ID, notificationID, notifications.Patch{Detail: &detail})
			h.startRetryChainSync(user.ID, dbUser, userServer, toRetry)
		}

		stillMissing := missingFor()
		if len(stillMissing) == 0 {
			status, detail, pct := notifications.StatusSuccess, "Everything matched in Plex - nothing left to download", 100
			h.Notifications.Update(user.ID, notificationID, notifications.Patch{Status: &status, Progress: &pct, Detail: &detail})
			return nil
		}

		queued, unmatched, failed := 0, 0, 0
		for i, track := range stillMissing {
			pct := i * 100 / len(stillMissing)
			detail := fmt.Sprintf("Searching deemix - %d of %d", i+1, len(stillMissing))
			h.Notifications.Update(user.ID, notificationID, notifications.Patch{Progress: &pct, Detail: &detail})

			result, err := h.queueDeemixForTrack(user.ID, dbUser, userServer, track, settings)
			switch {
			case err != nil:
				failed++
				slog.Warn("Deemix All: failed to queue a track", "error", err, "userId", user.ID, "trackId", track.ID)
			case result == nil:
				unmatched++
			default:
				queued++
			}
		}

		parts := []string{fmt.Sprintf("Queued %d of %d", queued, len(stillMissing))}
		if unmatched > 0 {
			parts = append(parts, fmt.Sprintf("%d with no good enough match", unmatched))
		}
		if failed > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", failed))
		}
		status := notifications.StatusSuccess
		if failed > 0 {
			status = notifications.StatusError
		}
		detail := strings.Join(parts, " · ")
		pct := 100
		h.Notifications.Update(user.ID, notificationID, notifications.Patch{Status: &status, Progress: &pct, Detail: &detail})
		return nil
	})

	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{"Notifications": h.Notifications.List(user.ID)})
}

// startRetryChainSync runs the retry chain and waits for it to finish -
// used by deemixAll, which is itself already backgrounded by the action
// queue, so blocking here (unlike the standalone POST /retry route) is fine.
func (h *MissingHandler) startRetryChainSync(userID int64, user *db.User, userServer *db.UserServer, tracks []db.MissingTrack) {
	done := make(chan struct{})
	go func() {
		h.startRetryChain(userID, user, userServer, tracks)
		close(done)
	}()
	<-done
}

// lidarrDownload ports POST /:id/lidarr-download.
func (h *MissingHandler) lidarrDownload(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	trackID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid track id", http.StatusBadRequest)
		return
	}
	track, ok := h.findMissingTrack(user.ID, trackID)
	if !ok {
		http.Error(w, "missing track not found", http.StatusNotFound)
		return
	}

	dbUser, _ := db.GetUserByID(h.DB, user.ID)
	userServer, _ := db.GetUserServer(h.DB, user.ID)

	firstArtist := firstArtistName(track.Artist)
	artistLookup, err := h.Lidarr.FindArtist(firstArtist)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if artistLookup == nil {
		http.Error(w, fmt.Sprintf("No Lidarr match found for artist %q", firstArtist), http.StatusNotFound)
		return
	}

	artistID, err := h.Lidarr.AddAndMonitorArtist(*artistLookup)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	album, _ := h.Lidarr.FindAlbumForArtist(artistID, track.Album.String)
	var albumID int
	if album != nil {
		albumID = album.ID
	}
	commandID, err := h.Lidarr.TriggerSearch(artistID, albumID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	detail := artistLookup.ArtistName + " · full discography"
	if album != nil {
		detail = artistLookup.ArtistName + " · " + album.Title
	}
	notification := h.Notifications.Add(user.ID, notifications.TypeLidarr, track.Title, detail, notifications.StatusInProgress, nil)

	if dbUser != nil && userServer != nil {
		go h.Lidarr.TrackSearch(user.ID, notification.ID, commandID, lidarrsvc.ReconcileContext{
			MissingTrackID: trackID, ServerURL: userServer.ServerURL,
			PlexToken: plex.ResolveToken(dbUser.PlexToken, userServer.AccessToken.String),
			LibraryID: userServer.LibraryID.String, ServerClientID: userServer.ServerClientID,
		})
	} else {
		status, errDetail := notifications.StatusError, "No Plex server configured to reconcile against"
		h.Notifications.Update(user.ID, notification.ID, notifications.Patch{Status: &status, Detail: &errDetail})
	}

	slog.Info("Triggered Lidarr search for missing track", "userId", user.ID, "trackId", trackID, "artistId", artistID, "commandId", commandID)
	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{"Notifications": h.Notifications.List(user.ID)})
}

func (h *MissingHandler) findMissingTrack(userID, trackID int64) (db.MissingTrack, bool) {
	all, err := db.GetUserMissingTracks(h.DB, userID)
	if err != nil {
		return db.MissingTrack{}, false
	}
	for _, t := range all {
		if t.ID == trackID {
			return t, true
		}
	}
	return db.MissingTrack{}, false
}

// rematch ports POST /:id/rematch: manually rematch a missing track to a
// specific Plex track by ratingKey.
func (h *MissingHandler) rematch(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	trackID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid track id", http.StatusBadRequest)
		return
	}
	_ = r.ParseForm()
	ratingKey := r.FormValue("ratingKey")
	if ratingKey == "" {
		http.Error(w, "ratingKey is required", http.StatusBadRequest)
		return
	}
	track, ok := h.findMissingTrack(user.ID, trackID)
	if !ok {
		http.Error(w, "missing track not found", http.StatusNotFound)
		return
	}
	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "No server selected", http.StatusBadRequest)
		return
	}

	target := matching.PlaylistTarget{ServerClientID: userServer.ServerClientID, LibraryID: userServer.LibraryID.String}
	if !matching.InsertMatchedTrackIntoPlaylist(h.DB, client, target, track, ratingKey) {
		http.Error(w, "Failed to add track to playlist", http.StatusInternalServerError)
		return
	}
	_ = db.RecordManualMatch(h.DB, user.ID, track.Title, track.Artist, track.Album.String, ratingKey)
	slog.Info("Missing track manually rematched", "userId", user.ID, "trackId", trackID, "ratingKey", ratingKey)

	h.renderGroups(w, user.ID)
}

// replaceSimilar ports POST /:id/replace-similar: seeds off one of the
// artist's other tracks already in the library and uses Plex's own
// sonic-analysis "nearest" results to pick a stand-in.
func (h *MissingHandler) replaceSimilar(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	trackID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid track id", http.StatusBadRequest)
		return
	}
	track, ok := h.findMissingTrack(user.ID, trackID)
	if !ok {
		http.Error(w, "missing track not found", http.StatusNotFound)
		return
	}
	client, userServer, err := h.client(user)
	if err != nil || userServer == nil || userServer.LibraryID.String == "" {
		http.Error(w, "No server selected", http.StatusBadRequest)
		return
	}

	artist, err := client.SearchArtist(userServer.LibraryID.String, track.Artist)
	if err != nil || artist == nil || artist.RatingKey == "" {
		http.Error(w, fmt.Sprintf("No artist matching %q found in your Plex library", track.Artist), http.StatusNotFound)
		return
	}
	seedTracks, err := client.GetArtistPopularTracks(userServer.LibraryID.String, artist.RatingKey, 1)
	if err != nil || len(seedTracks) == 0 {
		http.Error(w, fmt.Sprintf("%q has no tracks in your Plex library to seed a similarity search from", track.Artist), http.StatusNotFound)
		return
	}
	candidates, err := client.GetSonicallySimilarTracks(seedTracks[0].RatingKey, userServer.LibraryID.String, plex.SonicSimilarOptions{MaxDistance: 0.25, Limit: 10})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	existingKeys := map[string]bool{}
	if playlist, _ := db.GetPlaylistByID(h.DB, track.PlaylistID); playlist != nil && !strings.HasPrefix(playlist.PlexPlaylistID, "pending-") {
		if playlistTracks, err := client.GetPlaylistTracks(playlist.PlexPlaylistID); err == nil {
			for _, t := range playlistTracks {
				existingKeys[t.RatingKey] = true
			}
		}
	}
	var replacement *plex.Track
	for i := range candidates {
		if candidates[i].RatingKey != "" && !existingKeys[candidates[i].RatingKey] {
			replacement = &candidates[i]
			break
		}
	}
	if replacement == nil {
		http.Error(w, "No sonically similar replacement found for this track", http.StatusNotFound)
		return
	}

	target := matching.PlaylistTarget{ServerClientID: userServer.ServerClientID, LibraryID: userServer.LibraryID.String}
	if !matching.InsertMatchedTrackIntoPlaylist(h.DB, client, target, track, replacement.RatingKey) {
		http.Error(w, "Found a similar track but failed to add it to the playlist", http.StatusInternalServerError)
		return
	}
	slog.Info("Replaced missing track with sonically similar match", "userId", user.ID, "trackId", trackID,
		"original", track.Artist+" - "+track.Title, "replacement", replacement.GrandparentTitle+" - "+replacement.Title)

	h.renderGroups(w, user.ID)
}

func (h *MissingHandler) renderGroups(w http.ResponseWriter, userID int64) {
	tracks, _ := db.GetUserMissingTracks(h.DB, userID)
	h.Tmpl.RenderPartial(w, "partials/missing_list.html", map[string]any{"Groups": groupMissingTracks(h.DB, tracks), "TotalCount": len(tracks)})
}

// deleteTrack ports DELETE /:id.
func (h *MissingHandler) deleteTrack(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	trackID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid track id", http.StatusBadRequest)
		return
	}
	if _, ok := h.findMissingTrack(user.ID, trackID); !ok {
		http.Error(w, "missing track not found", http.StatusNotFound)
		return
	}
	if err := db.RemoveMissingTrack(h.DB, trackID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.renderGroups(w, user.ID)
}

// clearPlaylist ports DELETE /playlist/:playlistId.
func (h *MissingHandler) clearPlaylist(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	playlistID, err := strconv.ParseInt(chi.URLParam(r, "playlistId"), 10, 64)
	if err != nil {
		http.Error(w, "invalid playlist id", http.StatusBadRequest)
		return
	}
	playlist, err := db.GetPlaylistByID(h.DB, playlistID)
	if err != nil || playlist == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if playlist.UserID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := db.ClearPlaylistMissingTracks(h.DB, playlistID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.renderGroups(w, user.ID)
}
