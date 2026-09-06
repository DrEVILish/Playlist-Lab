package db

import "testing"

func TestScheduleCRUD(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	pl, _ := CreatePlaylistRow(sqlDB, u.ID, "pl1", "Playlist 1", "spotify", "")

	sched, err := CreateSchedule(sqlDB, u.ID, pl.ID, "playlist_refresh", "daily", "2024-01-01", "")
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

	if _, err := CreateSchedule(sqlDB, u1.ID, pl1.ID, "mix_generation", "weekly", "2024-01-01", ""); err != nil {
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
	sched, err := CreateSchedule(sqlDB, u.ID, pl.ID, "mix_generation", "daily", "2024-01-01", "")
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
