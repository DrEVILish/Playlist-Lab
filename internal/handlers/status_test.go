package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
)

func TestStatusPage_ServerSetupAndAdminLogs(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &StatusHandler{DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodGet, "/status", h.page)

	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/status", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `href="/settings#servers"`) {
		t.Fatalf("no-library page should offer server setup: status %d", rec.Code)
	}

	plex := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"Metadata":[]}}`))
	})
	seedUserServer(t, sqlDB, user.ID, plex.URL)
	rec = authedRequest(t, sqlDB, router, user, http.MethodGet, "/status", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Last 100 runs") || strings.Contains(rec.Body.String(), "View logs") {
		t.Fatalf("non-admin should see reports without the logs link: status %d", rec.Code)
	}

	if err := db.AddAdmin(sqlDB, user.ID); err != nil {
		t.Fatal(err)
	}
	rec = authedRequest(t, sqlDB, router, user, http.MethodGet, "/status", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `href="/settings#admin-logs"`) {
		t.Fatalf("admin should see the logs link: status %d", rec.Code)
	}
}
