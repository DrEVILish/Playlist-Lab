// Covers the Missing Tracks "Match" modal (DESIGN.md §11.6): the raw
// "type a Plex ratingKey" field it replaced had no test coverage of its
// own, so this is the first real check that the search-and-pick flow
// actually wires together - the modal opens pre-filled with the missing
// track's own title/artist, a live search renders results from Plex, and
// clicking one is wired to the existing rematch endpoint with that result's
// ratingKey.
package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
)

func TestOpenMatchModal_PrefillsQueryAndShowsResults(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	playlist, err := db.CreatePlaylistRow(sqlDB, user.ID, "rk-1", "Road Trip", "spotify", "")
	if err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}
	if err := db.AddMissingTracks(sqlDB, user.ID, playlist.ID, []db.NewMissingTrack{
		{Title: "Song A", Artist: "Artist A", Source: "spotify"},
	}); err != nil {
		t.Fatalf("AddMissingTracks: %v", err)
	}
	tracks, err := db.GetUserMissingTracks(sqlDB, user.ID)
	if err != nil || len(tracks) != 1 {
		t.Fatalf("GetUserMissingTracks: %v (tracks=%v)", err, tracks)
	}
	trackID := tracks[0].ID

	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/hubs/search" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MediaContainer": map[string]any{
					"Hub": []map[string]any{
						{"type": "track", "Metadata": []map[string]any{
							{"ratingKey": "rk-match-1", "title": "Song A (Remastered)", "grandparentTitle": "Artist A", "parentTitle": "Greatest Hits", "librarySectionID": 1},
						}},
					},
				},
			})
			return
		}
		t.Errorf("unexpected Plex request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	h := &MissingHandler{DB: sqlDB, PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab"), Tmpl: nopTemplates()}
	router := testRouter(sqlDB, http.MethodGet, "/missing/{id}/match", h.openMatchModal)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/missing/"+itoa(trackID)+"/match", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="Song A Artist A"`) {
		t.Fatalf("expected the search box pre-filled with the track's own title+artist, body=%s", body)
	}
	if !strings.Contains(body, "Song A (Remastered)") {
		t.Fatalf("expected the fake Plex search result to render, body=%s", body)
	}
	if !strings.Contains(body, `hx-post="/missing/`+itoa(trackID)+`/rematch"`) {
		t.Fatalf("expected the result button wired to the existing rematch endpoint, body=%s", body)
	}
	if !strings.Contains(body, `"ratingKey": "rk-match-1"`) {
		t.Fatalf("expected the result button to carry the matched track's own ratingKey, body=%s", body)
	}
}

func TestMatchSearch_NoQueryPromptsRatherThanSearching(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &MissingHandler{DB: sqlDB, PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab"), Tmpl: nopTemplates()}
	router := testRouter(sqlDB, http.MethodGet, "/missing/{id}/match-search", h.matchSearch)

	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/missing/1/match-search", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Type to search") {
		t.Fatalf("expected the empty-query prompt, body=%s", rec.Body.String())
	}
}
