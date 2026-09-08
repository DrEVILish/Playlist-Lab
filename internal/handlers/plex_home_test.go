package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

func newImportHandlerForPlexHome(sqlDB *sql.DB) *ImportHandler {
	return &ImportHandler{
		DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab"),
		Notifications: notifications.NewStore(),
	}
}

func TestPlexHomeUsers_RendersUserButtons(t *testing.T) {
	plexTv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"users":[{"uuid":"u-1","title":"Kid Profile","username":"kid"}]}`))
	}))
	defer plexTv.Close()
	restore := auth.SetPlexAPIBaseForTest(plexTv.URL, plexTv.URL+"/")
	defer restore()

	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	h := newImportHandlerForPlexHome(sqlDB)
	router := testRouter(sqlDB, http.MethodGet, "/import/plex-home/users", h.plexHomeUsers)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/import/plex-home/users", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Kid Profile") {
		t.Errorf("missing home user button, got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `hx-get="/import/plex-home/users/u-1/playlists"`) {
		t.Errorf("button must target this user's uuid, got: %s", rec.Body.String())
	}
}

func TestPlexHomeUserPlaylists_UsesHomeUserToken(t *testing.T) {
	plexTv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"authToken":"home-user-token"}`))
	}))
	defer plexTv.Close()
	restore := auth.SetPlexAPIBaseForTest(plexTv.URL, plexTv.URL+"/")
	defer restore()

	var gotToken string
	plexServer := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Plex-Token")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"pl-1","title":"Kid's Mix","playlistType":"audio","leafCount":5}]}}`))
	})

	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, plexServer.URL)

	h := newImportHandlerForPlexHome(sqlDB)
	router := testRouter(sqlDB, http.MethodGet, "/import/plex-home/users/{homeUserId}/playlists", h.plexHomeUserPlaylists)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/import/plex-home/users/u-1/playlists", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if gotToken != "home-user-token" {
		t.Errorf("Plex request used token %q, want the home user's own switched token", gotToken)
	}
	if !strings.Contains(rec.Body.String(), "Kid&#39;s Mix") && !strings.Contains(rec.Body.String(), "Kid's Mix") {
		t.Errorf("missing the home user's playlist, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Could not access") {
		t.Errorf("should not report the admin-token fallback when the switch succeeded, got: %s", rec.Body.String())
	}
}

func TestPlexHomeUserPlaylists_FallsBackToAdminTokenOnSwitchFailure(t *testing.T) {
	plexTv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer plexTv.Close()
	restore := auth.SetPlexAPIBaseForTest(plexTv.URL, plexTv.URL+"/")
	defer restore()

	plexServer := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"Metadata":[]}}`))
	})

	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, plexServer.URL)

	h := newImportHandlerForPlexHome(sqlDB)
	router := testRouter(sqlDB, http.MethodGet, "/import/plex-home/users/{homeUserId}/playlists", h.plexHomeUserPlaylists)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/import/plex-home/users/u-1/playlists", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (falls back rather than erroring); body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Could not access") {
		t.Errorf("want the admin-token fallback notice when the switch fails, got: %s", rec.Body.String())
	}
}

func TestPlexHomeCopyPlaylist_RequiresSourceHomeUserID(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, "http://127.0.0.1:1")

	h := newImportHandlerForPlexHome(sqlDB)
	router := testRouter(sqlDB, http.MethodPost, "/import/plex-home/playlists/{playlistId}/copy", h.plexHomeCopyPlaylist)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/plex-home/playlists/pl-1/copy", "", "application/x-www-form-urlencoded")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a missing sourceHomeUserId", rec.Code)
	}
}

func TestPlexHomeCopyPlaylist_CreatesPlaylistFromSourceTracks(t *testing.T) {
	plexTv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"authToken":"home-user-token"}`))
	}))
	defer plexTv.Close()
	restore := auth.SetPlexAPIBaseForTest(plexTv.URL, plexTv.URL+"/")
	defer restore()

	var createdTitle string
	plexServer := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/":
			w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine-1"}}`))
		case strings.HasSuffix(r.URL.Path, "/items") && r.Method == http.MethodGet:
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"t1","title":"Song"}]}}`))
		case r.URL.Path == "/playlists" && r.Method == http.MethodGet:
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"pl-1","title":"Kid's Mix","composite":"/library/metadata/pl-1/composite/1"}]}}`))
		case r.URL.Path == "/playlists" && r.Method == http.MethodPost:
			createdTitle = r.URL.Query().Get("title")
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"new-1","title":"` + createdTitle + `"}]}}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, plexServer.URL)

	store := notifications.NewStore()
	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab"), Notifications: store}
	router := testRouter(sqlDB, http.MethodPost, "/import/plex-home/playlists/{playlistId}/copy", h.plexHomeCopyPlaylist)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/plex-home/playlists/pl-1/copy",
		"sourceHomeUserId=u-1", "application/x-www-form-urlencoded")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if createdTitle != "Kid's Mix" {
		t.Errorf("created playlist title = %q, want the source playlist's own name as the default", createdTitle)
	}
	list := store.List(user.ID)
	if len(list) != 1 || list[0].Status != notifications.StatusSuccess {
		t.Fatalf("want one success notification, got %+v", list)
	}
}
