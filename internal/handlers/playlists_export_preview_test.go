// Covers the two new Playlists-table routes added for the design-system
// rollout (DESIGN.md §8.6/§11.1): CSV export and the row-click track
// preview. Both are otherwise easy to silently break (a template field
// rename, a wrong query path) without a build/run against the real
// embedded templates catching it - see nopTemplates().
package handlers

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestExportCSV_WritesFilteredSortedRows(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/playlists" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MediaContainer": map[string]any{
					"Metadata": []map[string]any{
						{"ratingKey": "rk-1", "title": "Road Trip", "playlistType": "audio", "leafCount": 12, "duration": 2400000},
						// Not an audio playlist - loadPlaylistRows filters it
						// out, same as the page itself.
						{"ratingKey": "rk-2", "title": "Not Audio", "playlistType": "video"},
					},
				},
			})
			return
		}
		t.Errorf("unexpected Plex request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodGet, "/playlists/export", h.exportCSV)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/playlists/export", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv" {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}

	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parsing response as CSV: %v", err)
	}
	wantHeader := []string{"Playlist Name", "Date Added", "Source", "Tracks", "Duration", "Missing Tracks", "Schedule", "Next Run", "Last Run"}
	if len(rows) < 1 || len(rows[0]) != len(wantHeader) {
		t.Fatalf("header row = %v, want %d columns matching %v", rows, len(wantHeader), wantHeader)
	}
	for i, want := range wantHeader {
		if rows[0][i] != want {
			t.Errorf("header[%d] = %q, want %q", i, rows[0][i], want)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("got %d data row(s), want exactly 1 (the video playlist must be excluded): %v", len(rows)-1, rows)
	}
	if rows[1][0] != "Road Trip" || rows[1][3] != "12" || rows[1][4] != "40m" {
		t.Errorf("data row = %v, want Name=Road Trip Tracks=12 Duration=40m", rows[1])
	}
}

func TestPreview_RendersTrackListForPlaylist(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/playlists/rk-1/items" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MediaContainer": map[string]any{
					"Metadata": []map[string]any{
						{"ratingKey": "500", "title": "Get Lucky", "originalTitle": "Daft Punk", "parentTitle": "RAM", "duration": 248000},
					},
				},
			})
			return
		}
		t.Errorf("unexpected Plex request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodGet, "/playlists/{plexId}/preview", h.preview)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/playlists/rk-1/preview", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Get Lucky") || !strings.Contains(body, "Daft Punk") || !strings.Contains(body, "4:08") {
		t.Errorf("preview body missing expected track details, got:\n%s", body)
	}
}

func TestPreview_NoServerSelectedRendersErrorState(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodGet, "/playlists/{plexId}/preview", h.preview)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/playlists/rk-1/preview", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a rendered error partial, not an HTTP error)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Couldn't load") {
		t.Errorf("body = %q, want the couldn't-load message", rec.Body.String())
	}
}
