package db

import "testing"

func TestPlaylistCRUD(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok", "")

	pl, err := CreatePlaylistRow(sqlDB, u.ID, "plex_pl_123", "My Playlist", "spotify", "https://spotify.com/playlist/123")
	if err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}
	if pl.Name != "My Playlist" || pl.Source != "spotify" {
		t.Fatalf("unexpected playlist: %+v", pl)
	}

	got, err := GetPlaylistByID(sqlDB, pl.ID)
	if err != nil || got == nil {
		t.Fatalf("GetPlaylistByID: %v got=%v", err, got)
	}
	if got.Name != "My Playlist" {
		t.Fatalf("unexpected name: %s", got.Name)
	}

	if err := UpdatePlaylistPlexID(sqlDB, pl.ID, "real_id_456"); err != nil {
		t.Fatalf("UpdatePlaylistPlexID: %v", err)
	}
	got, _ = GetPlaylistByID(sqlDB, pl.ID)
	if got.PlexPlaylistID != "real_id_456" {
		t.Fatalf("expected updated plex id, got %s", got.PlexPlaylistID)
	}

	if err := DeletePlaylistRow(sqlDB, pl.ID); err != nil {
		t.Fatalf("DeletePlaylistRow: %v", err)
	}
	got, err = GetPlaylistByID(sqlDB, pl.ID)
	if err != nil {
		t.Fatalf("GetPlaylistByID after delete: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil playlist after delete")
	}
}

// TestGetUserPlaylistsIsolation matches database.test.ts's
// "getUserPlaylists should not return other users playlists".
func TestGetUserPlaylistsIsolation(t *testing.T) {
	sqlDB := newTestDB(t)
	u1, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	u2, _ := CreateUser(sqlDB, "plex2", "u2", "tok2", "")

	if _, err := CreatePlaylistRow(sqlDB, u1.ID, "pl1", "Playlist 1", "spotify", ""); err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}
	if _, err := CreatePlaylistRow(sqlDB, u2.ID, "pl2", "Playlist 2", "deezer", ""); err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}

	u1Playlists, err := GetUserPlaylists(sqlDB, u1.ID)
	if err != nil {
		t.Fatalf("GetUserPlaylists: %v", err)
	}
	if len(u1Playlists) != 1 || u1Playlists[0].Name != "Playlist 1" {
		t.Fatalf("expected only user1's playlist, got %+v", u1Playlists)
	}
	for _, p := range u1Playlists {
		if p.UserID != u1.ID {
			t.Fatalf("leaked playlist belonging to user %d into user %d's results", p.UserID, u1.ID)
		}
	}
}

// TestGetPlaylistByPlexIDIsolation matches database.test.ts's test for
// colliding plex_playlist_id values across users with independent Plex
// servers - a real security invariant: this query must be scoped by user_id.
func TestGetPlaylistByPlexIDIsolation(t *testing.T) {
	sqlDB := newTestDB(t)
	u1, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	u2, _ := CreateUser(sqlDB, "plex2", "u2", "tok2", "")

	own, err := CreatePlaylistRow(sqlDB, u1.ID, "100", "My Playlist", "spotify", "")
	if err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}
	other, err := CreatePlaylistRow(sqlDB, u2.ID, "100", "Other User Playlist", "spotify", "")
	if err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}

	found, err := GetPlaylistByPlexID(sqlDB, u1.ID, "100")
	if err != nil || found == nil {
		t.Fatalf("GetPlaylistByPlexID: %v got=%v", err, found)
	}
	if found.ID != own.ID || found.UserID != u1.ID {
		t.Fatalf("expected own playlist %+v, got %+v", own, found)
	}

	foundOther, err := GetPlaylistByPlexID(sqlDB, u2.ID, "100")
	if err != nil || foundOther == nil {
		t.Fatalf("GetPlaylistByPlexID for user2: %v got=%v", err, foundOther)
	}
	if foundOther.ID != other.ID || foundOther.UserID != u2.ID {
		t.Fatalf("expected other user's playlist %+v, got %+v", other, foundOther)
	}
}

func TestGetPlaylistByPlexIDNotBelongingToUser(t *testing.T) {
	sqlDB := newTestDB(t)
	u1, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	u2, _ := CreateUser(sqlDB, "plex2", "u2", "tok2", "")

	if _, err := CreatePlaylistRow(sqlDB, u1.ID, "100", "My Playlist", "spotify", ""); err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}

	found, err := GetPlaylistByPlexID(sqlDB, u2.ID, "100")
	if err != nil {
		t.Fatalf("GetPlaylistByPlexID: %v", err)
	}
	if found != nil {
		t.Fatalf("expected nil, playlist not belonging to user2 was returned: %+v", found)
	}
}

// TestPlaylistCascadeDelete matches database-schema.property.test.ts's
// "should cascade delete schedules and missing tracks when a playlist is deleted".
func TestPlaylistCascadeDelete(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	pl, _ := CreatePlaylistRow(sqlDB, u.ID, "pl1", "Playlist 1", "spotify", "")

	if _, err := CreateSchedule(sqlDB, u.ID, pl.ID, "playlist_refresh", "daily", "2024-01-01", ""); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if err := AddMissingTracks(sqlDB, u.ID, pl.ID, []NewMissingTrack{
		{Title: "Track 1", Artist: "Artist 1", Position: 0, Source: "spotify"},
	}); err != nil {
		t.Fatalf("AddMissingTracks: %v", err)
	}

	if err := DeletePlaylistRow(sqlDB, pl.ID); err != nil {
		t.Fatalf("DeletePlaylistRow: %v", err)
	}

	var scheduleCount, trackCount int
	sqlDB.QueryRow("SELECT COUNT(*) FROM schedules WHERE playlist_id = ?", pl.ID).Scan(&scheduleCount)
	sqlDB.QueryRow("SELECT COUNT(*) FROM missing_tracks WHERE playlist_id = ?", pl.ID).Scan(&trackCount)
	if scheduleCount != 0 {
		t.Fatalf("expected schedules cascade-deleted with playlist, got %d", scheduleCount)
	}
	if trackCount != 0 {
		t.Fatalf("expected missing_tracks cascade-deleted with playlist, got %d", trackCount)
	}
}

func TestRenamePlaylistRow(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	pl, err := CreatePlaylistRow(sqlDB, u.ID, "rk-1", "Old Name", "spotify", "")
	if err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}
	before := pl.UpdatedAt

	if err := RenamePlaylistRow(sqlDB, pl.ID, "New Name"); err != nil {
		t.Fatalf("RenamePlaylistRow: %v", err)
	}
	got, err := GetPlaylistByID(sqlDB, pl.ID)
	if err != nil || got == nil {
		t.Fatalf("GetPlaylistByID: %v", err)
	}
	if got.Name != "New Name" {
		t.Errorf("name = %q, want %q", got.Name, "New Name")
	}
	if got.UpdatedAt < before {
		t.Errorf("updated_at = %d, want it bumped to at least %d", got.UpdatedAt, before)
	}
}
