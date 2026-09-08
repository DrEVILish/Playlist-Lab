package handlers

import (
	"net/http"
	"strings"
	"testing"
)

func TestSearchTracks_EmptyQueryRendersNoResults(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, "http://127.0.0.1:1")

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodGet, "/playlists/{plexId}/search-tracks", h.searchTracks)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/playlists/rk-1/search-tracks", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "track-search-list") {
		t.Errorf("empty query should render no results, got: %s", rec.Body.String())
	}
}

func TestSearchTracks_RendersMatchesWithCheckboxes(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/hubs/search" {
			w.Write([]byte(`{"MediaContainer":{"Hub":[{"type":"track","title":"Tracks","Metadata":[
				{"ratingKey":"t1","title":"Life in the Fast Lane","originalTitle":"Eagles","parentTitle":"Hotel California","librarySectionID":1}
			]}]}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodGet, "/playlists/{plexId}/search-tracks", h.searchTracks)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/playlists/rk-1/search-tracks?q=eagles", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="t1"`) {
		t.Errorf("missing checkbox for the matched track, got: %s", body)
	}
	if !strings.Contains(body, "Life in the Fast Lane") || !strings.Contains(body, "Eagles") {
		t.Errorf("missing track title/artist, got: %s", body)
	}
	if !strings.Contains(body, `hx-post="/playlists/rk-1/tracks"`) {
		t.Errorf("results form must post to the add-tracks endpoint for this playlist, got: %s", body)
	}
}

func TestSearchTracks_NoMatchesShowsHint(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"Hub":[]}}`))
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodGet, "/playlists/{plexId}/search-tracks", h.searchTracks)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/playlists/rk-1/search-tracks?q=nonexistent", "", "")

	if !strings.Contains(rec.Body.String(), "No tracks found") {
		t.Errorf("want a no-results hint, got: %s", rec.Body.String())
	}
}

func TestAddTracks_RequiresAtLeastOneSelection(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, "http://127.0.0.1:1")

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodPost, "/playlists/{plexId}/tracks", h.addTracks)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/playlists/rk-1/tracks", "", "application/x-www-form-urlencoded")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for no tracks selected", rec.Code)
	}
}

func TestAddTracks_AddsCheckedTracksAndRedirectsToEditor(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	var addedURI string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/":
			w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine-1"}}`))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/items"):
			addedURI = r.URL.Query().Get("uri")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodPost, "/playlists/{plexId}/tracks", h.addTracks)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/playlists/rk-1/tracks",
		"trackId=t1&trackId=t2", "application/x-www-form-urlencoded")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/playlists/rk-1" {
		t.Errorf("HX-Redirect = %q, want back to this playlist's editor", got)
	}
	if !strings.Contains(addedURI, "t1") || !strings.Contains(addedURI, "t2") {
		t.Errorf("added track URI = %q, want it to reference both t1 and t2", addedURI)
	}
}
