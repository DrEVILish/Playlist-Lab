package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
)

// newFakePlexServer starts an httptest.Server standing in for a user's Plex
// Media Server, closed automatically when the test ends.
func newFakePlexServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// seedUserServer gives a test user a selected Plex server pointing at
// serverURL, matching what PlaylistsHandler.client(user) needs to build a
// real plex.Client instead of erroring out as "no server selected".
func seedUserServer(t *testing.T, sqlDB *sql.DB, userID int64, serverURL string) {
	t.Helper()
	if _, err := db.SaveUserServer(sqlDB, userID, "Test Server", "client-1", serverURL, "1", "Music", ""); err != nil {
		t.Fatalf("SaveUserServer: %v", err)
	}
}

func newPlaylistsHandler(sqlDB *sql.DB) *PlaylistsHandler {
	return &PlaylistsHandler{
		DB:       sqlDB,
		PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab"),
		Tmpl:     nopTemplates(),
	}
}

// TestDeletePlaylist_RemovesTrackingRowAndProxiesToPlex covers the common
// path: Plex confirms the delete, so the local tracking row (missing
// tracks, schedule, etc. all key off it) is cleaned up too.
func TestDeletePlaylist_RemovesTrackingRowAndProxiesToPlex(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	var gotMethod, gotPath string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	tracked, err := db.CreatePlaylistRow(sqlDB, user.ID, "rk-1", "My Playlist", "spotify", "")
	if err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, "DELETE", "/playlists/{plexId}", h.deletePlaylist)
	rec := authedRequest(t, sqlDB, router, user, "DELETE", "/playlists/rk-1", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/" {
		t.Errorf("HX-Redirect = %q, want /", got)
	}
	if gotMethod != http.MethodDelete || gotPath != "/playlists/rk-1" {
		t.Errorf("Plex request = %s %s, want DELETE /playlists/rk-1", gotMethod, gotPath)
	}
	if row, _ := db.GetPlaylistByPlexID(sqlDB, user.ID, "rk-1"); row != nil {
		t.Errorf("tracking row for %d still present after delete", tracked.ID)
	}
}

// TestDeletePlaylist_NoServerSelected covers the "never finished setup"
// guard - every playlist action needs a selected Plex server before it can
// build a client at all.
func TestDeletePlaylist_NoServerSelected(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, "DELETE", "/playlists/{plexId}", h.deletePlaylist)
	rec := authedRequest(t, sqlDB, router, user, "DELETE", "/playlists/rk-1", "", "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestBulkDelete_ContinuesPastIndividualFailures covers the
// Promise.allSettled-equivalent tolerance: a single playlist Plex rejects
// the delete for must not stop the rest, and only the successfully deleted
// ones lose their tracking row.
func TestBulkDelete_ContinuesPastIndividualFailures(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/playlists/rk-bad" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	if _, err := db.CreatePlaylistRow(sqlDB, user.ID, "rk-good", "Good Playlist", "spotify", ""); err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}
	if _, err := db.CreatePlaylistRow(sqlDB, user.ID, "rk-bad", "Bad Playlist", "spotify", ""); err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, "POST", "/playlists/bulk-delete", h.bulkDelete)
	rec := authedRequest(t, sqlDB, router, user, "POST", "/playlists/bulk-delete",
		"id=rk-good&id=rk-bad", "application/x-www-form-urlencoded")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with a partial failure; body=%s", rec.Code, rec.Body.String())
	}
	if row, _ := db.GetPlaylistByPlexID(sqlDB, user.ID, "rk-good"); row != nil {
		t.Error("rk-good's tracking row should be gone (Plex delete succeeded)")
	}
	if row, _ := db.GetPlaylistByPlexID(sqlDB, user.ID, "rk-bad"); row == nil {
		t.Error("rk-bad's tracking row should remain (Plex delete failed)")
	}
}

// TestMoveTrack_ProxiesAfterIDToPlex covers the reorder-by-drag action:
// the after-id from the form body must reach Plex's move endpoint as the
// query param it expects.
func TestMoveTrack_ProxiesAfterIDToPlex(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	var gotMethod, gotPath, gotAfter string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAfter = r.Method, r.URL.Path, r.URL.Query().Get("after")
		w.WriteHeader(http.StatusOK)
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, "PUT", "/playlists/{plexId}/tracks/{trackId}/move", h.moveTrack)
	rec := authedRequest(t, sqlDB, router, user, "PUT", "/playlists/rk-1/tracks/item-5/move",
		"afterId=item-3", "application/x-www-form-urlencoded")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if gotMethod != http.MethodPut || gotPath != "/playlists/rk-1/items/item-5/move" {
		t.Errorf("Plex request = %s %s, want PUT /playlists/rk-1/items/item-5/move", gotMethod, gotPath)
	}
	if gotAfter != "item-3" {
		t.Errorf("after query param = %q, want item-3", gotAfter)
	}
}

// TestRemoveTrack_ProxiesToPlexAndReturnsNoContent covers the delete-one-
// track action reaching Plex's items endpoint with the right method/path.
func TestRemoveTrack_ProxiesToPlexAndReturnsNoContent(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	var gotMethod, gotPath string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)
	if _, err := db.CreatePlaylistRow(sqlDB, user.ID, "rk-1", "My Playlist", "spotify", ""); err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, "DELETE", "/playlists/{plexId}/tracks/{trackId}", h.removeTrack)
	rec := authedRequest(t, sqlDB, router, user, "DELETE", "/playlists/rk-1/tracks/item-9", "", "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if gotMethod != http.MethodDelete || gotPath != "/playlists/rk-1/items/item-9" {
		t.Errorf("Plex request = %s %s, want DELETE /playlists/rk-1/items/item-9", gotMethod, gotPath)
	}
	// The tracking row must survive removing a single track from it - only
	// deletePlaylist/bulkDelete remove the tracking row itself.
	if row, _ := db.GetPlaylistByPlexID(sqlDB, user.ID, "rk-1"); row == nil {
		t.Error("tracking row should still exist after removing one track")
	}
}

// TestClone_CopiesTracksIntoANewPlaylistNamedCopy covers the multi-step Plex
// round trip clone needs: load the source playlist's tracks, resolve the
// server's machine identifier, find the source playlist's display name, then
// create a new playlist with "(Copy)" appended and the same tracks.
func TestClone_CopiesTracksIntoANewPlaylistNamedCopy(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	var gotCreateQuery string
	var gotAddItemsQuery string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/playlists/rk-1/items":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MediaContainer": map[string]any{
					"Metadata": []map[string]any{{"ratingKey": "500", "title": "Song One"}},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MediaContainer": map[string]any{"machineIdentifier": "machine-abc"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/playlists":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MediaContainer": map[string]any{
					"Metadata": []map[string]any{{"ratingKey": "rk-1", "title": "Original"}},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/playlists":
			gotCreateQuery = r.URL.RawQuery
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MediaContainer": map[string]any{
					"Metadata": []map[string]any{{"ratingKey": "999", "title": "Original (Copy)"}},
				},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/playlists/999/items":
			gotAddItemsQuery = r.URL.RawQuery
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected Plex request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, "POST", "/playlists/{plexId}/clone", h.clone)
	rec := authedRequest(t, sqlDB, router, user, "POST", "/playlists/rk-1/clone", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/" {
		t.Errorf("HX-Redirect = %q, want /", got)
	}
	if gotCreateQuery == "" || !strings.Contains(gotCreateQuery, "title=Original+%28Copy%29") {
		t.Errorf("create query = %q, want a title of %q", gotCreateQuery, "Original (Copy)")
	}
	if gotAddItemsQuery == "" || !strings.Contains(gotAddItemsQuery, "500") {
		t.Errorf("add-items query = %q, want it to reference ratingKey 500", gotAddItemsQuery)
	}
}
