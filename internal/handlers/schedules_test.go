package handlers

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

// TestConfigJSON_DefaultsUpdateModeToReplace ports schedule-update-modes.
// test.ts's "defaults to replace when no mode is stored" - configJSON is
// the pure function that turns a submitted schedule form into the JSON
// blob scheduler.Run later reads updateMode out of.
func TestConfigJSON_DefaultsUpdateModeToReplace(t *testing.T) {
	form := url.Values{"runTime": {"09:00"}}
	req, _ := http.NewRequest(http.MethodPost, "/schedules", nil)
	req.Form = form

	var cfg db.ScheduleConfig
	if err := json.Unmarshal([]byte(configJSON(req)), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.UpdateMode != "replace" {
		t.Errorf("UpdateMode = %q, want replace", cfg.UpdateMode)
	}
	if cfg.RunTime != "09:00" {
		t.Errorf("RunTime = %q, want 09:00", cfg.RunTime)
	}
}

func TestConfigJSON_RespectsExplicitAccumulateMode(t *testing.T) {
	form := url.Values{"updateMode": {"accumulate"}}
	req, _ := http.NewRequest(http.MethodPost, "/schedules", nil)
	req.Form = form

	var cfg db.ScheduleConfig
	json.Unmarshal([]byte(configJSON(req)), &cfg)
	if cfg.UpdateMode != "accumulate" {
		t.Errorf("UpdateMode = %q, want accumulate", cfg.UpdateMode)
	}
}

func TestScheduleCreate_RequiresPlaylistFrequencyAndStartDate(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	playlist, _ := db.CreatePlaylistRow(sqlDB, user.ID, "rk-1", "My Playlist", "spotify", "")

	h := &SchedulesHandler{DB: sqlDB, Tmpl: nopTemplates(), Notifications: notifications.NewStore()}
	router := testRouter(sqlDB, "POST", "/schedules", h.create)

	cases := []struct {
		name string
		form url.Values
	}{
		{"missing playlistId", url.Values{"frequency": {"daily"}, "startDate": {"2020-01-01"}}},
		{"missing frequency", url.Values{"playlistId": {itoa(playlist.ID)}, "startDate": {"2020-01-01"}}},
		{"missing startDate", url.Values{"playlistId": {itoa(playlist.ID)}, "frequency": {"daily"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := authedRequest(t, sqlDB, router, user, "POST", "/schedules", tc.form.Encode(), "application/x-www-form-urlencoded")
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestScheduleCreate_Succeeds(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	playlist, _ := db.CreatePlaylistRow(sqlDB, user.ID, "rk-1", "My Playlist", "spotify", "")

	h := &SchedulesHandler{DB: sqlDB, Tmpl: nopTemplates(), Notifications: notifications.NewStore()}
	router := testRouter(sqlDB, "POST", "/schedules", h.create)

	form := url.Values{"playlistId": {itoa(playlist.ID)}, "frequency": {"daily"}, "startDate": {"2020-01-01"}, "updateMode": {"accumulate"}}
	rec := authedRequest(t, sqlDB, router, user, "POST", "/schedules", form.Encode(), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	sched, err := db.GetScheduleByPlaylistID(sqlDB, playlist.ID)
	if err != nil || sched == nil {
		t.Fatalf("GetScheduleByPlaylistID: %v", err)
	}
	if sched.ParsedConfig().UpdateMode != "accumulate" {
		t.Errorf("UpdateMode = %q, want accumulate", sched.ParsedConfig().UpdateMode)
	}
}

// TestScheduleUpdateDeleteRunOne_ForbiddenForNonOwner exercises
// ownedSchedule's 403 gate that update/delete/runOne all share - a schedule
// id belonging to one user must be untouchable (and unusable) by another
// authenticated user, matching the TS routes' per-route ownership checks.
func TestScheduleUpdateDeleteRunOne_ForbiddenForNonOwner(t *testing.T) {
	sqlDB := newTestDB(t)
	owner := newTestUser(t, sqlDB)
	other, _ := db.CreateUser(sqlDB, "plex-other-sched", "other", "tok", "")
	playlist, _ := db.CreatePlaylistRow(sqlDB, owner.ID, "rk-1", "My Playlist", "spotify", "")
	sched, err := db.CreateSchedule(sqlDB, owner.ID, playlist.ID, 0, "playlist_refresh", "daily", "2020-01-01", `{"updateMode":"replace"}`)
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	h := &SchedulesHandler{DB: sqlDB, Tmpl: nopTemplates(), Notifications: notifications.NewStore()}

	t.Run("update", func(t *testing.T) {
		router := testRouter(sqlDB, "PUT", "/schedules/{id}", h.update)
		form := url.Values{"frequency": {"weekly"}, "startDate": {"2020-01-01"}}
		rec := authedRequest(t, sqlDB, router, other, "PUT", "/schedules/"+itoa(sched.ID), form.Encode(), "application/x-www-form-urlencoded")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 (ownedSchedule hides existence from non-owners)", rec.Code)
		}
	})

	t.Run("delete", func(t *testing.T) {
		router := testRouter(sqlDB, "DELETE", "/schedules/{id}", h.delete)
		rec := authedRequest(t, sqlDB, router, other, "DELETE", "/schedules/"+itoa(sched.ID), "", "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("runOne", func(t *testing.T) {
		router := testRouter(sqlDB, "POST", "/schedules/{id}/run", h.runOne)
		rec := authedRequest(t, sqlDB, router, other, "POST", "/schedules/"+itoa(sched.ID)+"/run", "", "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	// The schedule must still exist untouched for its real owner.
	still, err := db.GetScheduleByID(sqlDB, sched.ID)
	if err != nil || still == nil || still.Frequency != "daily" {
		t.Errorf("schedule mutated despite non-owner 404s: %+v err=%v", still, err)
	}
}
