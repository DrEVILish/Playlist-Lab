package deezer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/importsvc"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

// TestDeezerImportRoundTrip is a full one-adapter round trip: fetch a
// playlist from Deezer's public API, match every track against a Plex
// library, and create the resulting playlist - the same
// ImportPlaylist->FinalizeImportResult pair internal/handlers/import.go
// calls, but exercised directly so a break anywhere in that chain (adapter
// parsing, the registry, matching, or Plex playlist creation) fails a test
// instead of only ever showing up by hand. No package before this one
// combined a source adapter, the matching engine, and Plex playlist
// creation in a single test.
func TestDeezerImportRoundTrip(t *testing.T) {
	// Fake Deezer: one playlist, three tracks - two with artists a fake
	// Plex library recognizes, one whose artist isn't in the library at
	// all, so the round trip also proves an unmatched track is preserved
	// (as a missing_tracks row) rather than silently dropped.
	deezerBody := `{
		"id": 999, "title": "My Deezer Mix",
		"tracks": {"data": [
			{"title": "Track A", "artist": {"name": "Artist A"}, "album": {"title": "Album A"}},
			{"title": "Track B", "artist": {"name": "Artist B"}, "album": {"title": "Album B"}},
			{"title": "Track C", "artist": {"name": "Artist Missing"}, "album": {"title": "Album C"}}
		]}
	}`
	src := newTestSource(t, deezerBody, http.StatusOK)

	plexMux := http.NewServeMux()
	writeMeta := func(w http.ResponseWriter, items []map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"Metadata": items}})
	}
	plexMux.HandleFunc("/library/sections/1/all", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("artist.title") {
		case "Artist A":
			writeMeta(w, []map[string]any{{"ratingKey": "artist-a", "title": "Artist A"}})
		case "Artist B":
			writeMeta(w, []map[string]any{{"ratingKey": "artist-b", "title": "Artist B"}})
		default:
			writeMeta(w, nil) // "Artist Missing" (and any type=10 direct-query fallback) - not in this library.
		}
	})
	plexMux.HandleFunc("/library/metadata/artist-a/allLeaves", func(w http.ResponseWriter, r *http.Request) {
		writeMeta(w, []map[string]any{{"ratingKey": "501", "title": "Track A", "grandparentTitle": "Artist A", "parentTitle": "Album A"}})
	})
	plexMux.HandleFunc("/library/metadata/artist-b/allLeaves", func(w http.ResponseWriter, r *http.Request) {
		writeMeta(w, []map[string]any{{"ratingKey": "502", "title": "Track B", "grandparentTitle": "Artist B", "parentTitle": "Album B"}})
	})
	plexMux.HandleFunc("/hubs/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{}})
	})
	var createdPlaylistQuery string
	plexMux.HandleFunc("/playlists", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			createdPlaylistQuery = r.URL.RawQuery
			writeMeta(w, []map[string]any{{"ratingKey": "new-playlist-1", "title": "My Deezer Mix"}})
			return
		}
		writeMeta(w, nil)
	})
	plexMux.HandleFunc("/playlists/new-playlist-1/items", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	plexSrv := httptest.NewServer(plexMux)
	defer plexSrv.Close()

	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer sqlDB.Close()
	user, err := db.CreateUser(sqlDB, "plex-1", "tester", "tok", "")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	registry := adapters.NewRegistry()
	registry.RegisterSource(src)
	client := plex.NewClient(plexSrv.URL, "token", "client-id", "Playlist Lab")

	result, err := importsvc.ImportPlaylist(context.Background(), registry, sqlDB, client, "deezer", "999",
		importsvc.Options{UserID: user.ID, LibraryID: "1"}, nil, func() bool { return false })
	if err != nil {
		t.Fatalf("ImportPlaylist: %v", err)
	}
	if result.MatchedCount != 2 || result.TotalCount != 3 {
		t.Fatalf("result = %+v, want 2 matched of 3 total", result)
	}

	dbPlaylistID, plexRatingKey, err := importsvc.FinalizeImportResult(sqlDB, client, "deezer", "999", user.ID,
		"client-id", "1", result, importsvc.FinalizeOpts{})
	if err != nil {
		t.Fatalf("FinalizeImportResult: %v", err)
	}
	if plexRatingKey != "new-playlist-1" {
		t.Errorf("plexRatingKey = %q, want new-playlist-1", plexRatingKey)
	}
	if createdPlaylistQuery == "" {
		t.Fatal("expected a POST /playlists call to actually create the playlist")
	}

	tracked, err := db.GetPlaylistByID(sqlDB, dbPlaylistID)
	if err != nil || tracked == nil {
		t.Fatalf("expected a tracked playlist row, err=%v", err)
	}
	if tracked.PlexPlaylistID != "new-playlist-1" || tracked.Name != "My Deezer Mix" || tracked.Source != "deezer" {
		t.Errorf("tracked playlist row = %+v, unexpected", tracked)
	}

	missing, err := db.GetUserMissingTracks(sqlDB, user.ID)
	if err != nil {
		t.Fatalf("GetUserMissingTracks: %v", err)
	}
	if len(missing) != 1 || missing[0].Title != "Track C" {
		t.Fatalf("missing tracks = %+v, want exactly Track C", missing)
	}
}
