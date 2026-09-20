package db

import "testing"

func TestScheduleCRUD(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	pl, _ := CreatePlaylistRow(sqlDB, u.ID, "pl1", "Playlist 1", "spotify", "")

	sched, err := CreateSchedule(sqlDB, u.ID, pl.ID, 0, "playlist_refresh", "daily", "2024-01-01", "")
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if sched.ScheduleType != "playlist_refresh" || sched.Frequency != "daily" {
		t.Fatalf("unexpected schedule: %+v", sched)
	}

	if err := UpdateScheduleLastRun(sqlDB, sched.ID); err != nil {
		t.Fatalf("UpdateScheduleLastRun: %v", err)
	}
	got, err := GetScheduleByID(sqlDB, sched.ID)
	if err != nil || got == nil {
		t.Fatalf("GetScheduleByID: %v got=%v", err, got)
	}
	if !got.LastRun.Valid {
		t.Fatal("expected last_run to be set")
	}

	if err := DeleteSchedule(sqlDB, sched.ID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	got, err = GetScheduleByID(sqlDB, sched.ID)
	if err != nil {
		t.Fatalf("GetScheduleByID after delete: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil schedule after delete")
	}
}

func TestGetUserSchedulesIsolation(t *testing.T) {
	sqlDB := newTestDB(t)
	u1, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	u2, _ := CreateUser(sqlDB, "plex2", "u2", "tok2", "")
	pl1, _ := CreatePlaylistRow(sqlDB, u1.ID, "pl1", "P1", "spotify", "")

	if _, err := CreateSchedule(sqlDB, u1.ID, pl1.ID, 0, "mix_generation", "weekly", "2024-01-01", ""); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	u1Schedules, err := GetUserSchedules(sqlDB, u1.ID)
	if err != nil || len(u1Schedules) != 1 {
		t.Fatalf("expected 1 schedule for user1, got %v err=%v", u1Schedules, err)
	}
	u2Schedules, err := GetUserSchedules(sqlDB, u2.ID)
	if err != nil || len(u2Schedules) != 0 {
		t.Fatalf("expected 0 schedules leaked to user2, got %v err=%v", u2Schedules, err)
	}
}

// TestGetDueSchedulesDisabledUser matches database.test.ts's
// "getDueSchedules should not return schedules belonging to a disabled user".
func TestGetDueSchedulesDisabledUser(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	pl, _ := CreatePlaylistRow(sqlDB, u.ID, "pl1", "P1", "spotify", "")
	sched, err := CreateSchedule(sqlDB, u.ID, pl.ID, 0, "mix_generation", "daily", "2024-01-01", "")
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	containsID := func(schedules []Schedule, id int64) bool {
		for _, s := range schedules {
			if s.ID == id {
				return true
			}
		}
		return false
	}

	due, err := GetDueSchedules(sqlDB)
	if err != nil {
		t.Fatalf("GetDueSchedules: %v", err)
	}
	if !containsID(due, sched.ID) {
		t.Fatal("expected schedule to be due while user is enabled")
	}

	if err := DisableUser(sqlDB, u.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	due, err = GetDueSchedules(sqlDB)
	if err != nil {
		t.Fatalf("GetDueSchedules: %v", err)
	}
	if containsID(due, sched.ID) {
		t.Fatal("disabled user's schedule must not be due")
	}

	if err := EnableUser(sqlDB, u.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	due, err = GetDueSchedules(sqlDB)
	if err != nil {
		t.Fatalf("GetDueSchedules: %v", err)
	}
	if !containsID(due, sched.ID) {
		t.Fatal("expected schedule due again after re-enabling user")
	}
}

// GetAllSchedules is a cross-user view GetUserSchedules never needed to be -
// it must return every user's schedules, not just the caller's, and must
// carry the username/playlist name the admin Schedules tab renders without
// a second round trip per row.
func TestGetAllSchedules(t *testing.T) {
	sqlDB := newTestDB(t)
	u1, _ := CreateUser(sqlDB, "plex1", "alice", "tok1", "")
	u2, _ := CreateUser(sqlDB, "plex2", "bob", "tok2", "")
	pl, _ := CreatePlaylistRow(sqlDB, u1.ID, "pl1", "Alice's Playlist", "spotify", "")

	s1, err := CreateSchedule(sqlDB, u1.ID, pl.ID, 0, "playlist_refresh", "daily", "2024-01-01", "")
	if err != nil {
		t.Fatalf("CreateSchedule (alice): %v", err)
	}
	// mix_generation schedules have no playlist yet - GetAllSchedules must
	// tolerate that rather than erroring on the LEFT JOIN.
	s2, err := CreateSchedule(sqlDB, u2.ID, 0, 0, "mix_generation", "weekly", "2024-01-01", "")
	if err != nil {
		t.Fatalf("CreateSchedule (bob): %v", err)
	}

	all, err := GetAllSchedules(sqlDB)
	if err != nil {
		t.Fatalf("GetAllSchedules: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d schedules, want 2 across both users", len(all))
	}

	byID := map[int64]AdminSchedule{}
	for _, s := range all {
		byID[s.ID] = s
	}

	got1, ok := byID[s1.ID]
	if !ok {
		t.Fatalf("alice's schedule (id %d) missing from GetAllSchedules", s1.ID)
	}
	if got1.Username != "alice" {
		t.Errorf("username = %q, want alice", got1.Username)
	}
	if !got1.PlaylistName.Valid || got1.PlaylistName.String != "Alice's Playlist" {
		t.Errorf("playlist name = %+v, want Alice's Playlist", got1.PlaylistName)
	}

	got2, ok := byID[s2.ID]
	if !ok {
		t.Fatalf("bob's schedule (id %d) missing from GetAllSchedules", s2.ID)
	}
	if got2.Username != "bob" {
		t.Errorf("username = %q, want bob", got2.Username)
	}
	if got2.PlaylistName.Valid {
		t.Errorf("playlist name = %+v, want NULL for a playlist-less mix_generation schedule", got2.PlaylistName)
	}
}
