package db

import "testing"

func TestPlaylistShareAndIsolation(t *testing.T) {
	sqlDB := newTestDB(t)
	owner, _ := CreateUser(sqlDB, "plex1", "owner", "tok1", "")
	recipient, _ := CreateUser(sqlDB, "plex2", "recipient", "tok2", "")
	other, _ := CreateUser(sqlDB, "plex3", "other", "tok3", "")

	pl, _ := CreatePlaylistRow(sqlDB, owner.ID, "pl1", "Shared Playlist", "spotify", "")

	if err := RecordPlaylistShare(sqlDB, pl.ID, owner.ID, recipient.ID, "pl1", "Shared Playlist"); err != nil {
		t.Fatalf("RecordPlaylistShare: %v", err)
	}

	shared, err := GetPlaylistsSharedWithUser(sqlDB, recipient.ID)
	if err != nil || len(shared) != 1 {
		t.Fatalf("expected 1 shared playlist, got %v err=%v", shared, err)
	}
	if shared[0].SharedByUsername != "owner" {
		t.Fatalf("unexpected sharer: %+v", shared[0])
	}

	// Isolation: a user not shared with should see nothing.
	otherShared, err := GetPlaylistsSharedWithUser(sqlDB, other.ID)
	if err != nil || len(otherShared) != 0 {
		t.Fatalf("expected 0 shares for unrelated user, got %v err=%v", otherShared, err)
	}

	// Re-sharing (INSERT OR REPLACE on unique(playlist_id, shared_with_user_id))
	// should update, not duplicate.
	if err := RecordPlaylistShare(sqlDB, pl.ID, owner.ID, recipient.ID, "pl1", "Renamed Playlist"); err != nil {
		t.Fatalf("RecordPlaylistShare (re-share): %v", err)
	}
	shared, _ = GetPlaylistsSharedWithUser(sqlDB, recipient.ID)
	if len(shared) != 1 || shared[0].PlaylistName != "Renamed Playlist" {
		t.Fatalf("expected re-share to update in place, got %+v", shared)
	}
}
