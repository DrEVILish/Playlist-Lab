package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
)

func TestSelectServer_UsesPlexDiscovery(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	const address = "https://plex.example.com:32400"
	plexTV := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/resources" || r.Header.Get("X-Plex-Token") != user.PlexToken {
			http.Error(w, "discovery request expected", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode([]auth.PlexServer{{
			Name: "My Server", ClientIdentifier: "server-id", Provides: "server",
			Owned: true, AccessToken: "server-token",
			Connections: []auth.PlexConnection{{URI: address, Local: true}},
		}})
	}))
	t.Cleanup(plexTV.Close)
	t.Cleanup(auth.SetPlexAPIBaseForTest(plexTV.URL, plexTV.URL))

	h := &ServersHandler{DB: sqlDB, Plex: auth.NewPlexClient("test-client", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodPost, "/servers/select", h.selectServer)
	data := url.Values{"server": {"server-id"}, "library": {"5|Music"}}
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/servers/select",
		data.Encode(), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("selection failed (%d): %s", rec.Code, rec.Body.String())
	}
	saved, err := db.GetUserMusicServer(sqlDB, user.ID)
	if err != nil || saved == nil {
		t.Fatalf("missing selected server: %v", err)
	}
	if saved.ServerURL != address || saved.ServerName != "My Server" || !saved.IsOwner ||
		saved.AccessToken.String != "server-token" || saved.LibraryID.String != "5" {
		t.Fatal("stored server did not match discovery and the selected library")
	}

	data.Set("server", "removed-server")
	rec = authedRequest(t, sqlDB, router, user, http.MethodPost, "/servers/select",
		data.Encode(), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "select a server") {
		t.Fatalf("selection should reject a removed server, got %d: %s", rec.Code, rec.Body.String())
	}
}
