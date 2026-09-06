package db

import "testing"

func TestUserCRUD(t *testing.T) {
	sqlDB := newTestDB(t)

	u, err := CreateUser(sqlDB, "plex123", "testuser", "token123", "thumb.jpg")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.PlexUserID != "plex123" || u.PlexUsername != "testuser" || u.PlexToken != "token123" {
		t.Fatalf("unexpected user: %+v", u)
	}
	if !u.IsEnabled {
		t.Fatal("new user should be enabled by default")
	}

	got, err := GetUserByPlexID(sqlDB, "plex123")
	if err != nil {
		t.Fatalf("GetUserByPlexID: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("expected id %d, got %d", u.ID, got.ID)
	}

	if _, err := GetUserByPlexID(sqlDB, "nonexistent"); err != ErrUserNotFound {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}

	if err := UpdateUserToken(sqlDB, u.ID, "newtoken456"); err != nil {
		t.Fatalf("UpdateUserToken: %v", err)
	}
	got, _ = GetUserByID(sqlDB, u.ID)
	if got.PlexToken != "newtoken456" {
		t.Fatalf("expected updated token, got %s", got.PlexToken)
	}

	if err := UpdateUserProfile(sqlDB, u.ID, "newname", "newthumb.jpg"); err != nil {
		t.Fatalf("UpdateUserProfile: %v", err)
	}
	got, _ = GetUserByID(sqlDB, u.ID)
	if got.PlexUsername != "newname" || got.PlexThumb.String != "newthumb.jpg" {
		t.Fatalf("unexpected profile after update: %+v", got)
	}

	if err := DisableUser(sqlDB, u.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	got, _ = GetUserByID(sqlDB, u.ID)
	if got.IsEnabled {
		t.Fatal("expected user to be disabled")
	}
	if err := EnableUser(sqlDB, u.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	got, _ = GetUserByID(sqlDB, u.ID)
	if !got.IsEnabled {
		t.Fatal("expected user to be re-enabled")
	}

	if err := DeleteUser(sqlDB, u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := GetUserByID(sqlDB, u.ID); err != ErrUserNotFound {
		t.Fatalf("expected ErrUserNotFound after delete, got %v", err)
	}
}

func TestAdminUsers(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok", "")

	isAdmin, err := IsAdmin(sqlDB, u.ID)
	if err != nil || isAdmin {
		t.Fatalf("expected not admin, got %v err=%v", isAdmin, err)
	}

	if err := AddAdmin(sqlDB, u.ID); err != nil {
		t.Fatalf("AddAdmin: %v", err)
	}
	isAdmin, _ = IsAdmin(sqlDB, u.ID)
	if !isAdmin {
		t.Fatal("expected admin after AddAdmin")
	}

	ids, err := GetAdminUserIDs(sqlDB)
	if err != nil || len(ids) != 1 || ids[0] != u.ID {
		t.Fatalf("unexpected admin ids: %v err=%v", ids, err)
	}

	if err := RemoveAdmin(sqlDB, u.ID); err != nil {
		t.Fatalf("RemoveAdmin: %v", err)
	}
	isAdmin, _ = IsAdmin(sqlDB, u.ID)
	if isAdmin {
		t.Fatal("expected not admin after RemoveAdmin")
	}
}

// TestUserCascadeDelete verifies schema.sql's ON DELETE CASCADE actually
// wipes every table that references users.id, matching
// database-schema.property.test.ts's Property 25.
func TestUserCascadeDelete(t *testing.T) {
	sqlDB := newTestDB(t)

	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok", "")
	pl, err := CreatePlaylistRow(sqlDB, u.ID, "pl1", "My Playlist", "spotify", "")
	if err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}
	if _, err := CreateSchedule(sqlDB, u.ID, pl.ID, "playlist_refresh", "daily", "2024-01-01", ""); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if err := AddMissingTracks(sqlDB, u.ID, pl.ID, []NewMissingTrack{{Title: "t", Artist: "a", Position: 0, Source: "spotify"}}); err != nil {
		t.Fatalf("AddMissingTracks: %v", err)
	}
	if err := SaveMatchingSettingsJSON(sqlDB, u.ID, "{}"); err != nil {
		t.Fatalf("SaveMatchingSettingsJSON: %v", err)
	}

	if err := DeleteUser(sqlDB, u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	if playlists, _ := GetUserPlaylists(sqlDB, u.ID); len(playlists) != 0 {
		t.Fatalf("expected playlists cascade-deleted, got %d", len(playlists))
	}
	if schedules, _ := GetUserSchedules(sqlDB, u.ID); len(schedules) != 0 {
		t.Fatalf("expected schedules cascade-deleted, got %d", len(schedules))
	}
	if tracks, _ := GetUserMissingTracks(sqlDB, u.ID); len(tracks) != 0 {
		t.Fatalf("expected missing_tracks cascade-deleted, got %d", len(tracks))
	}
	var settingsCount int
	sqlDB.QueryRow("SELECT COUNT(*) FROM user_settings WHERE user_id = ?", u.ID).Scan(&settingsCount)
	if settingsCount != 0 {
		t.Fatalf("expected user_settings cascade-deleted, got %d", settingsCount)
	}
}
