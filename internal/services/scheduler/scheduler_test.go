package scheduler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

func TestIsDue(t *testing.T) {
	now := time.Date(2026, 9, 5, 14, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		s    db.Schedule
		want bool
	}{
		{
			name: "first run, start date in future",
			s:    db.Schedule{StartDate: "2026-09-10", Frequency: "daily"},
			want: false,
		},
		{
			name: "first run, start date today, no run_time",
			s:    db.Schedule{StartDate: "2026-09-05", Frequency: "daily"},
			want: true,
		},
		{
			name: "first run, run_time already passed today",
			s:    db.Schedule{StartDate: "2026-09-01", Frequency: "daily", Config: sql.NullString{String: `{"run_time":"10:00"}`, Valid: true}},
			want: true,
		},
		{
			name: "first run, run_time later today",
			s:    db.Schedule{StartDate: "2026-09-01", Frequency: "daily", Config: sql.NullString{String: `{"run_time":"18:00"}`, Valid: true}},
			want: false,
		},
		{
			name: "daily frequency not yet met",
			s: db.Schedule{
				Frequency: "daily",
				LastRun:   sql.NullInt64{Int64: now.Add(-12 * time.Hour).Unix(), Valid: true},
			},
			want: false,
		},
		{
			name: "daily frequency met, no run_time",
			s: db.Schedule{
				Frequency: "daily",
				LastRun:   sql.NullInt64{Int64: now.Add(-25 * time.Hour).Unix(), Valid: true},
			},
			want: true,
		},
		{
			name: "weekly frequency met but run_time not reached yet today",
			s: db.Schedule{
				Frequency: "weekly",
				LastRun:   sql.NullInt64{Int64: now.Add(-8 * 24 * time.Hour).Unix(), Valid: true},
				Config:    sql.NullString{String: `{"run_time":"20:00"}`, Valid: true},
			},
			want: false,
		},
		{
			name: "unknown frequency",
			s: db.Schedule{
				Frequency: "hourly",
				LastRun:   sql.NullInt64{Int64: now.Add(-25 * time.Hour).Unix(), Valid: true},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDue(tt.s, now); got != tt.want {
				t.Errorf("IsDue() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestResolveTargetPlaylistID covers schedule-checker-job.test.ts's
// resolveTargetPlaylistID equivalent: a refresh/mix-generation run must
// write into the playlist it already tracks by ratingKey, only falling back
// to a Plex name search when that ratingKey is missing or a "pending-..."
// placeholder left by an interrupted first import.
func TestResolveTargetPlaylistID(t *testing.T) {
	var searched bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		searched = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"MediaContainer": map[string]any{
				"Metadata": []map[string]any{
					{"ratingKey": "found-123", "title": "My Playlist"},
				},
			},
		})
	}))
	defer srv.Close()
	client := plex.NewClient(srv.URL, "token", "client-id", "Playlist Lab")

	t.Run("uses the tracked ratingKey without hitting Plex", func(t *testing.T) {
		searched = false
		got, err := resolveTargetPlaylistID(client, "already-tracked-456", "My Playlist")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "already-tracked-456" {
			t.Fatalf("got %q, want the tracked ratingKey unchanged", got)
		}
		if searched {
			t.Fatal("must not search Plex when a real ratingKey is already tracked")
		}
	})

	t.Run("falls back to a name search for a pending placeholder", func(t *testing.T) {
		searched = false
		got, err := resolveTargetPlaylistID(client, "pending-abc", "My Playlist")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "found-123" {
			t.Fatalf("got %q, want the ratingKey found by name search", got)
		}
		if !searched {
			t.Fatal("expected a Plex playlist search for a pending placeholder")
		}
	})

	t.Run("falls back to a name search for an empty tracked id", func(t *testing.T) {
		searched = false
		got, err := resolveTargetPlaylistID(client, "", "My Playlist")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "found-123" {
			t.Fatalf("got %q, want the ratingKey found by name search", got)
		}
	})

	t.Run("returns empty when no playlist in Plex matches the name", func(t *testing.T) {
		got, err := resolveTargetPlaylistID(client, "", "Some Other Name")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Fatalf("got %q, want empty (no match -> caller creates a new playlist)", got)
		}
	})
}

func TestDiffCollectionMembership(t *testing.T) {
	current := []plex.Track{{RatingKey: "1"}, {RatingKey: "2"}, {RatingKey: "3"}}

	t.Run("sync mode adds new and removes stale", func(t *testing.T) {
		toAdd, toRemove := diffCollectionMembership(current, []string{"2", "3", "4"}, "sync")
		if len(toAdd) != 1 || toAdd[0] != "4" {
			t.Fatalf("toAdd = %v, want [4]", toAdd)
		}
		if len(toRemove) != 1 || toRemove[0] != "1" {
			t.Fatalf("toRemove = %v, want [1]", toRemove)
		}
	})

	t.Run("add_only never removes", func(t *testing.T) {
		toAdd, toRemove := diffCollectionMembership(current, []string{"4"}, "add_only")
		if len(toAdd) != 1 || toAdd[0] != "4" {
			t.Fatalf("toAdd = %v, want [4]", toAdd)
		}
		if len(toRemove) != 0 {
			t.Fatalf("toRemove = %v, want none (add_only)", toRemove)
		}
	})

	t.Run("already in sync makes no changes", func(t *testing.T) {
		toAdd, toRemove := diffCollectionMembership(current, []string{"1", "2", "3"}, "sync")
		if len(toAdd) != 0 || len(toRemove) != 0 {
			t.Fatalf("toAdd = %v, toRemove = %v, want both empty", toAdd, toRemove)
		}
	})
}

func TestDailyScheduleKeepsConfiguredTime(t *testing.T) {
	scheduledAt := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	s := db.Schedule{
		Frequency: "daily",
		StartDate: "2026-11-18",
		Config:    sql.NullString{String: `{"run_time":"09:00"}`, Valid: true},
		LastRun:   sql.NullInt64{Int64: scheduledAt.Add(15 * time.Second).Unix(), Valid: true},
	}
	nextDay := scheduledAt.AddDate(0, 0, 1)
	if got, ok := NextRun(s, nextDay); !ok || !got.Equal(nextDay) || !IsDue(s, nextDay) {
		t.Fatalf("expected due at %v, got next run %v", nextDay, got)
	}
}

func TestReplacePlaylistKeepsOriginalOnFailedAdd(t *testing.T) {
	var deleted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"original","playlistItemID":7}]}}`))
		case http.MethodPut:
			http.Error(w, "Plex temporarily unavailable", http.StatusBadGateway)
		case http.MethodDelete:
			deleted.Store(true)
		}
	}))
	defer srv.Close()
	client := plex.NewClient(srv.URL, "token", "client-id", "Playlist Lab")
	err := replacePlaylistTracks(client, "playlist", []string{plex.BuildTrackURI("server", "replacement")})
	if err == nil || deleted.Load() {
		t.Fatalf("failed add should preserve original tracks, deletion=%v, err=%v", deleted.Load(), err)
	}
}

func TestReplacePlaylistRejectsNoTracksWithoutMutation(t *testing.T) {
	var touched atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		touched.Store(true)
		w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"original","playlistItemID":7}]}}`))
	}))
	defer srv.Close()
	client := plex.NewClient(srv.URL, "token", "client-id", "Playlist Lab")
	err := replacePlaylistTracks(client, "playlist", nil)
	if err == nil || touched.Load() {
		t.Fatalf("empty replacement should be rejected, Plex called=%v, err=%v", touched.Load(), err)
	}
}

func TestScheduledPlaylistKeepsOriginalServerAfterDefaultChanges(t *testing.T) {
	sqlDB, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	user, err := db.CreateUser(sqlDB, "test-user", "Listener", "fake-token", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.AddUserServer(sqlDB, user.ID, "First", "server-a", "http://example.invalid", "1", "Music", "", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.AddUserServer(sqlDB, user.ID, "Other", "server-b", "http://example.invalid", "2", "Music", "", false)
	if err != nil {
		t.Fatal(err)
	}
	playlist, err := db.CreatePlaylistRowForServer(sqlDB, user.ID, first.ServerClientID, "42", "First", "spotify", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetDefaultServer(sqlDB, user.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	got, err := playlistServer(Deps{DB: sqlDB}, user, playlist)
	if err != nil || got == nil || got.ID != first.ID {
		t.Fatalf("want original server %d, got %v, %v", first.ID, got, err)
	}
}
