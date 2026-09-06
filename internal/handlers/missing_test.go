package handlers

import (
	"database/sql"
	"net/http"
	"testing"

	"github.com/drevilish/playlist-lab/internal/db"
)

func TestFirstArtistName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Daft Punk", "Daft Punk"},
		{"Daft Punk, Pharrell Williams", "Daft Punk"},
		{"Simon & Garfunkel", "Simon"},
		{"Artist A/Artist B", "Artist A"},
		{"  Padded Artist  ", "Padded Artist"},
	}
	for _, tc := range tests {
		if got := firstArtistName(tc.in); got != tc.want {
			t.Errorf("firstArtistName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGroupMissingTracks(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	p1, _ := db.CreatePlaylistRow(sqlDB, user.ID, "rk-1", "Playlist One", "spotify", "")
	p2, _ := db.CreatePlaylistRow(sqlDB, user.ID, "rk-2", "Playlist Two", "deezer", "")

	tracks := []db.MissingTrack{
		{ID: 1, PlaylistID: p1.ID, Title: "Song A", Source: "spotify"},
		{ID: 2, PlaylistID: p2.ID, Title: "Song B", Source: "deezer"},
		{ID: 3, PlaylistID: p1.ID, Title: "Song C", Source: "spotify"},
	}

	groups := groupMissingTracks(sqlDB, tracks)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	// Groups appear in first-seen order (p1 before p2), and each group
	// collects every track for that playlist rather than one-per-group.
	if groups[0].PlaylistID != p1.ID || groups[0].PlaylistName != "Playlist One" || len(groups[0].Tracks) != 2 {
		t.Errorf("group 0 = %+v, want playlist one with 2 tracks", groups[0])
	}
	if groups[1].PlaylistID != p2.ID || groups[1].PlaylistName != "Playlist Two" || len(groups[1].Tracks) != 1 {
		t.Errorf("group 1 = %+v, want playlist two with 1 track", groups[1])
	}
}

func TestGroupMissingTracks_UnknownPlaylistFallsBackToPlaceholderName(t *testing.T) {
	sqlDB := newTestDB(t)
	tracks := []db.MissingTrack{{ID: 1, PlaylistID: 999, Title: "Orphan"}}
	groups := groupMissingTracks(sqlDB, tracks)
	if len(groups) != 1 || groups[0].PlaylistName != "Unknown" {
		t.Errorf("groups = %+v, want single group named Unknown", groups)
	}
}

// TestDeleteTrack_ScopedToOwningUser verifies the per-track action routing
// invariant: a DELETE for a given numeric track id must only ever act on
// that user's own row - findMissingTrack (used by delete/rematch/replace-
// similar/deemix/lidarr) filters GetUserMissingTracks by the authenticated
// user's id before matching on trackID, so a track owned by a different
// user (even with a colliding/omitted id) is treated as not found rather
// than silently operated on.
func TestDeleteTrack_ScopedToOwningUser(t *testing.T) {
	sqlDB := newTestDB(t)
	owner := newTestUser(t, sqlDB)
	other, _ := db.CreateUser(sqlDB, "plex-other", "other", "tok", "")

	playlist, _ := db.CreatePlaylistRow(sqlDB, owner.ID, "rk-1", "Owner Playlist", "spotify", "")
	trackID := addMissingTrack(t, sqlDB, owner.ID, playlist.ID, "Missing Song")

	h := &MissingHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, "DELETE", "/missing/{id}", h.deleteTrack)

	// Another authenticated user must not be able to delete the first
	// user's missing-track row via its numeric id.
	rec := authedRequest(t, sqlDB, router, other, "DELETE", "/missing/"+itoa(trackID), "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user delete: status=%d, want 404", rec.Code)
	}
	remaining, _ := db.GetUserMissingTracks(sqlDB, owner.ID)
	if len(remaining) != 1 {
		t.Fatalf("track was deleted by a non-owning user: remaining=%d", len(remaining))
	}

	// The actual owner deleting the same id must succeed.
	rec = authedRequest(t, sqlDB, router, owner, "DELETE", "/missing/"+itoa(trackID), "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner delete: status=%d body=%s", rec.Code, rec.Body.String())
	}
	remaining, _ = db.GetUserMissingTracks(sqlDB, owner.ID)
	if len(remaining) != 0 {
		t.Errorf("track not deleted by its owner: remaining=%d", len(remaining))
	}
}

func TestClearPlaylist_ForbiddenForNonOwningUser(t *testing.T) {
	sqlDB := newTestDB(t)
	owner := newTestUser(t, sqlDB)
	other, _ := db.CreateUser(sqlDB, "plex-other2", "other2", "tok", "")

	playlist, _ := db.CreatePlaylistRow(sqlDB, owner.ID, "rk-1", "Owner Playlist", "spotify", "")
	addMissingTrack(t, sqlDB, owner.ID, playlist.ID, "Missing Song")

	h := &MissingHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, "DELETE", "/missing/playlist/{playlistId}", h.clearPlaylist)

	rec := authedRequest(t, sqlDB, router, other, "DELETE", "/missing/playlist/"+itoa(playlist.ID), "", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	remaining, _ := db.GetUserMissingTracks(sqlDB, owner.ID)
	if len(remaining) != 1 {
		t.Errorf("missing tracks cleared despite 403: remaining=%d", len(remaining))
	}
}

func TestDeleteTrack_NotFoundForNonexistentID(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &MissingHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, "DELETE", "/missing/{id}", h.deleteTrack)

	rec := authedRequest(t, sqlDB, router, user, "DELETE", "/missing/999999", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// addMissingTrack inserts one missing_tracks row directly (no exported
// db.AddMissingTrack single-row helper exists outside the batch
// AddMissingTracks path used by imports), returning its id.
func addMissingTrack(t *testing.T, sqlDB *sql.DB, userID, playlistID int64, title string) int64 {
	t.Helper()
	res, err := sqlDB.Exec(
		`INSERT INTO missing_tracks (user_id, playlist_id, title, artist, position, added_at, source)
		 VALUES (?, ?, ?, ?, 0, strftime('%s','now'), 'spotify')`,
		userID, playlistID, title, "Some Artist",
	)
	if err != nil {
		t.Fatalf("insert missing_tracks: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}
