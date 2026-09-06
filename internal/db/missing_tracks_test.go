package db

import "testing"

func TestAddAndGetMissingTracks(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	pl, _ := CreatePlaylistRow(sqlDB, u.ID, "pl1", "P1", "spotify", "")

	tracks := []NewMissingTrack{
		{Title: "Track A", Artist: "Artist A", Album: "Album A", Position: 0, Source: "spotify"},
		{Title: "Track B", Artist: "Artist B", Position: 1, Source: "spotify"}, // no album -> NULL
	}
	if err := AddMissingTracks(sqlDB, u.ID, pl.ID, tracks); err != nil {
		t.Fatalf("AddMissingTracks: %v", err)
	}

	got, err := GetUserMissingTracks(sqlDB, u.ID)
	if err != nil {
		t.Fatalf("GetUserMissingTracks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(got))
	}
	// NULL-handling check: Album should be invalid (NULL) for the second track.
	var foundNullAlbum bool
	for _, tr := range got {
		if tr.Title == "Track B" && !tr.Album.Valid {
			foundNullAlbum = true
		}
	}
	if !foundNullAlbum {
		t.Fatal("expected Track B's album to be NULL when omitted")
	}

	if err := RemoveMissingTrack(sqlDB, got[0].ID); err != nil {
		t.Fatalf("RemoveMissingTrack: %v", err)
	}
	got, _ = GetUserMissingTracks(sqlDB, u.ID)
	if len(got) != 1 {
		t.Fatalf("expected 1 track after removal, got %d", len(got))
	}
}

func TestAddMissingTracksEmptyIsNoop(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	pl, _ := CreatePlaylistRow(sqlDB, u.ID, "pl1", "P1", "spotify", "")

	if err := AddMissingTracks(sqlDB, u.ID, pl.ID, nil); err != nil {
		t.Fatalf("AddMissingTracks with empty slice: %v", err)
	}
	got, _ := GetUserMissingTracks(sqlDB, u.ID)
	if len(got) != 0 {
		t.Fatalf("expected 0 tracks, got %d", len(got))
	}
}

// TestGetUserMissingTracksIsolation is the data-isolation invariant from
// data-isolation.property.test.ts's "should isolate missing tracks data
// between users".
func TestGetUserMissingTracksIsolation(t *testing.T) {
	sqlDB := newTestDB(t)
	u1, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	u2, _ := CreateUser(sqlDB, "plex2", "u2", "tok2", "")
	pl1, _ := CreatePlaylistRow(sqlDB, u1.ID, "pl1", "P1", "spotify", "")

	if err := AddMissingTracks(sqlDB, u1.ID, pl1.ID, []NewMissingTrack{
		{Title: "T1", Artist: "A1", Position: 0, Source: "spotify"},
	}); err != nil {
		t.Fatalf("AddMissingTracks: %v", err)
	}

	u1Tracks, err := GetUserMissingTracks(sqlDB, u1.ID)
	if err != nil || len(u1Tracks) != 1 {
		t.Fatalf("expected 1 track for user1, got %v err=%v", u1Tracks, err)
	}
	for _, tr := range u1Tracks {
		if tr.UserID != u1.ID {
			t.Fatalf("expected track owned by user1, got user_id=%d", tr.UserID)
		}
	}

	u2Tracks, err := GetUserMissingTracks(sqlDB, u2.ID)
	if err != nil || len(u2Tracks) != 0 {
		t.Fatalf("expected 0 tracks leaked to user2, got %v err=%v", u2Tracks, err)
	}
}

func TestClearPlaylistMissingTracks(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	pl, _ := CreatePlaylistRow(sqlDB, u.ID, "pl1", "P1", "spotify", "")

	if err := AddMissingTracks(sqlDB, u.ID, pl.ID, []NewMissingTrack{
		{Title: "T1", Artist: "A1", Position: 0, Source: "spotify"},
		{Title: "T2", Artist: "A2", Position: 1, Source: "spotify"},
	}); err != nil {
		t.Fatalf("AddMissingTracks: %v", err)
	}

	if err := ClearPlaylistMissingTracks(sqlDB, pl.ID); err != nil {
		t.Fatalf("ClearPlaylistMissingTracks: %v", err)
	}
	got, _ := GetUserMissingTracks(sqlDB, u.ID)
	if len(got) != 0 {
		t.Fatalf("expected 0 tracks after clear, got %d", len(got))
	}
}
