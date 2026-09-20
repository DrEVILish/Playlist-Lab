// Package handlers: missing_match.go replaces the Missing Tracks page's old
// "type a Plex ratingKey" field - meaningless to a user who has no reason to
// know what a ratingKey is - with a real search: a modal pre-filled with the
// missing track's own title/artist, showing fuzzy matches from the user's
// Plex library as the query is edited, letting them click the right one.
// Clicking a result posts straight to the existing rematch handler
// (missing.go) with that result's ratingKey - the same insert-into-playlist
// + RecordManualMatch logic already used by the old ratingKey field, so a
// match made here is remembered exactly the same way and won't be
// overwritten by a future automated re-match (matchOneTrack, matchengine.go,
// already checks remembered manual matches before searching fresh).
package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
)

// matchTargetSelector is the element the modal's own rematch clicks should
// refresh once a match is made - the same container the row's other actions
// already target (missing_list.html), computed the same way from
// ?listScope= so the modal doesn't need to know whether it was opened from
// the standalone /missing page or a Playlists-row expando.
func matchTargetSelector(r *http.Request) string {
	if scope := r.URL.Query().Get("listScope"); scope != "" {
		return "#missing-row-" + scope + "-content"
	}
	return "#missing-list"
}

// openMatchModal is GET /missing/{id}/match: the modal shell, pre-filled
// with the track's own title+artist as the initial query and already
// showing that search's results (DESIGN.md §8.12 - live search fields
// still ought to show something on first open, not force an empty state).
func (h *MissingHandler) openMatchModal(w http.ResponseWriter, r *http.Request) {
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
	query := strings.TrimSpace(track.Title + " " + track.Artist)
	results, searchErr := h.searchPlexForMatch(user, query)

	h.Tmpl.RenderPartial(w, "partials/missing_match.html", map[string]any{
		"TrackID": trackID, "Title": track.Title, "Artist": track.Artist,
		"Query": query, "Target": matchTargetSelector(r), "ListScope": r.URL.Query().Get("listScope"),
		"Results": results, "SearchError": searchErr != nil,
	})
}

// matchSearch is GET /missing/{id}/match-search?q=... - re-run as the modal's
// search box is edited (hx-trigger="input changed delay:400ms, search"),
// re-rendering just the results list.
func (h *MissingHandler) matchSearch(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	trackID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid track id", http.StatusBadRequest)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	results, searchErr := h.searchPlexForMatch(user, query)

	h.Tmpl.RenderPartial(w, "partials/missing_match_results.html", map[string]any{
		"TrackID": trackID, "Target": matchTargetSelector(r), "ListScope": r.URL.Query().Get("listScope"),
		"Query": query, "Results": results, "SearchError": searchErr != nil,
	})
}

// searchPlexForMatch is the fuzzy-search half shared by both handlers above -
// the same plex.Client.SearchTrack every other search box in this app already
// uses (playlists.go's searchTracks, for the editor's "add tracks" box),
// capped to 20 results the same way.
func (h *MissingHandler) searchPlexForMatch(user *db.User, query string) ([]trackRow, error) {
	if query == "" {
		return nil, nil
	}
	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		return nil, err
	}
	tracks, err := client.SearchTrack(query, userServer.LibraryID.String, "", "")
	if err != nil {
		return nil, err
	}
	if len(tracks) > 20 {
		tracks = tracks[:20]
	}
	return toTrackRows(tracks), nil
}
