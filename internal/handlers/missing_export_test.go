// Covers GET /missing/export (DESIGN.md §8.6/§11.6, follow-up to the
// Playlists export in playlists_export_preview_test.go) - the "one runnable
// check" that the CSV header/grouping logic actually matches what
// GetUserMissingTracks returns, not just that it compiles.
package handlers

import (
	"encoding/csv"
	"net/http"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/db"
)

func TestMissingExportCSV_WritesGroupedRows(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	playlist, err := db.CreatePlaylistRow(sqlDB, user.ID, "rk-1", "Road Trip", "spotify", "")
	if err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}
	if err := db.AddMissingTracks(sqlDB, user.ID, playlist.ID, []db.NewMissingTrack{
		{Title: "Song A", Artist: "Artist A", Album: "Album A", Source: "spotify"},
	}); err != nil {
		t.Fatalf("AddMissingTracks: %v", err)
	}

	h := &MissingHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, http.MethodGet, "/missing/export", h.exportCSV)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/missing/export", "", "")

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
	wantHeader := []string{"Playlist", "Title", "Artist", "Album", "Source", "Added"}
	if len(rows) != 2 || len(rows[0]) != len(wantHeader) {
		t.Fatalf("rows = %v, want a header row of %v plus exactly 1 data row", rows, wantHeader)
	}
	for i, want := range wantHeader {
		if rows[0][i] != want {
			t.Errorf("header[%d] = %q, want %q", i, rows[0][i], want)
		}
	}
	want := []string{"Road Trip", "Song A", "Artist A", "Album A", "spotify"}
	for i, w := range want {
		if rows[1][i] != w {
			t.Errorf("data row[%d] = %q, want %q (row=%v)", i, rows[1][i], w, rows[1])
		}
	}
}
