package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

func TestChartsFragment_UnsupportedSourceRendersEmptyState(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	h := &ChartsHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, http.MethodGet, "/import/charts", h.chartsFragment)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/import/charts?source=nope&country=US", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (empty-state, not an error page)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Unsupported source") {
		t.Errorf("body = %q, want the unsupported-source empty state", rec.Body.String())
	}
}

func TestSearchFragment_EmptyQueryPromptsInsteadOfSearching(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	h := &ChartsHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, http.MethodGet, "/import/search", h.searchFragment)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/import/search?source=deezer&q=", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Type something to search") {
		t.Errorf("body = %q, want the empty-query prompt", rec.Body.String())
	}
}

func TestSearchFragment_UnsupportedSourceRendersEmptyState(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	h := &ChartsHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, http.MethodGet, "/import/search", h.searchFragment)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/import/search?source=apple&q=road+trip", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Search isn&#39;t available") && !strings.Contains(rec.Body.String(), "Search isn't available") {
		t.Errorf("body = %q, want the search-unavailable empty state for a source with no search API", rec.Body.String())
	}
}

func TestSpotifyUsers_AddListDelete(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	h := &ChartsHandler{DB: sqlDB, Tmpl: nopTemplates(), Notifications: notifications.NewStore()}
	addRouter := testRouter(sqlDB, http.MethodPost, "/import/spotify-users", h.addSpotifyUser)

	// Pasting a full profile URL should be reduced to just the username,
	// same as v1.x's addSavedSpotifyUser regex.
	rec := authedRequest(t, sqlDB, addRouter, user, http.MethodPost, "/import/spotify-users",
		"spotifyUserId=https://open.spotify.com/user/realuser123", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "realuser123") {
		t.Errorf("body = %q, want the extracted username listed", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "open.spotify.com") {
		t.Errorf("body = %q, want the profile URL reduced to a bare username, not stored verbatim", rec.Body.String())
	}

	users, err := db.GetSavedSpotifyUsers(sqlDB, user.ID)
	if err != nil || len(users) != 1 || users[0].SpotifyUserID != "realuser123" {
		t.Fatalf("saved users = %+v, err = %v, want exactly one row for realuser123", users, err)
	}

	deleteRouter := testRouter(sqlDB, http.MethodDelete, "/import/spotify-users/{id}", h.deleteSpotifyUser)
	rec = authedRequest(t, sqlDB, deleteRouter, user, http.MethodDelete, "/import/spotify-users/"+itoa(users[0].ID), "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "realuser123") {
		t.Errorf("body = %q, want the deleted user gone from the re-rendered list", rec.Body.String())
	}
}
