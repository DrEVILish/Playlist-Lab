package auth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func httpBody(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

// DisplayName must fall back through username -> title -> friendlyName ->
// email -> "plex-<id>", matching plexDisplayName() in routes/auth.ts -
// managed (restricted) Home users have no username, only a title.
func TestPlexUser_DisplayName(t *testing.T) {
	cases := []struct {
		name string
		user PlexUser
		want string
	}{
		{"prefers username", PlexUser{ID: 1, Username: "u", Title: "t", Friendly: "f", Email: "e"}, "u"},
		{"falls back to title when no username", PlexUser{ID: 1, Title: "t", Friendly: "f", Email: "e"}, "t"},
		{"falls back to friendlyName", PlexUser{ID: 1, Friendly: "f", Email: "e"}, "f"},
		{"falls back to email", PlexUser{ID: 1, Email: "e"}, "e"},
		{"falls back to plex-<id> when nothing else is set", PlexUser{ID: 7}, "plex-7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.user.DisplayName(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// BestURL prefers local-non-relay, then remote-non-relay, then relay, then
// finally a raw publicAddress guess - same order as services/auth.ts's
// getBestServerUrl. Picking the wrong connection can route the app through
// a relay for a server that's actually reachable directly.
func TestPlexServer_BestURL(t *testing.T) {
	t.Run("prefers local non-relay", func(t *testing.T) {
		s := PlexServer{Connections: []PlexConnection{
			{URI: "relay", Relay: true},
			{URI: "remote", Local: false},
			{URI: "local", Local: true},
		}}
		if got := s.BestURL(); got != "local" {
			t.Fatalf("got %q, want %q", got, "local")
		}
	})

	t.Run("falls back to remote non-relay when no local connection", func(t *testing.T) {
		s := PlexServer{Connections: []PlexConnection{
			{URI: "relay", Relay: true},
			{URI: "remote", Local: false},
		}}
		if got := s.BestURL(); got != "remote" {
			t.Fatalf("got %q, want %q", got, "remote")
		}
	})

	t.Run("falls back to relay when only a relay connection exists", func(t *testing.T) {
		s := PlexServer{Connections: []PlexConnection{{URI: "relay", Relay: true}}}
		if got := s.BestURL(); got != "relay" {
			t.Fatalf("got %q, want %q", got, "relay")
		}
	})

	t.Run("falls back to publicAddress guess when there are no connections", func(t *testing.T) {
		s := PlexServer{PublicAddress: "1.2.3.4"}
		want := "https://1.2.3.4:32400"
		if got := s.BestURL(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

// GetHomeUsers must handle both shapes plex.tv answers with: a bare array
// or {"users": [...]}.
func TestParseFriendIDs(t *testing.T) {
	xml := `<?xml version="1.0"?><MediaContainer><User id="11" title="a"/><User id="22" title="b"/></MediaContainer>`
	ids := parseFriendIDs(strings.NewReader(xml))
	if len(ids) != 2 || ids[0] != 11 || ids[1] != 22 {
		t.Fatalf("got %v, want [11 22]", ids)
	}
}

func TestDecodeUsersArrayOrWrapped(t *testing.T) {
	t.Run("bare array", func(t *testing.T) {
		resp := &http.Response{Body: httpBody(`[{"id":1},{"id":2}]`)}
		ids, err := decodeUsersArrayOrWrapped(resp)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
			t.Fatalf("got %v", ids)
		}
	})

	t.Run("wrapped object", func(t *testing.T) {
		resp := &http.Response{Body: httpBody(`{"users":[{"id":3}]}`)}
		ids, err := decodeUsersArrayOrWrapped(resp)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(ids) != 1 || ids[0] != 3 {
			t.Fatalf("got %v", ids)
		}
	})
}

// GetHomeUsersDetailed must handle both response shapes plex.tv sends
// (decodeUsersArrayOrWrapped's own two branches, above) and prefer uuid
// over the numeric id, and username over title - matching
// routes/plex-home.ts's GET /users mapping exactly.
func TestGetHomeUsersDetailed(t *testing.T) {
	t.Run("bare array, prefers uuid and username", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"uuid":"u-1","id":1,"title":"Kid Profile","username":"kid","thumb":"https://example.com/t.png"}]`))
		}))
		defer srv.Close()

		c := NewPlexClient("test-client-id", "Playlist Lab")
		restore := SetPlexAPIBaseForTest(srv.URL, srv.URL+"/")
		defer restore()

		users, err := c.GetHomeUsersDetailed("admin-token")
		if err != nil {
			t.Fatalf("GetHomeUsersDetailed: %v", err)
		}
		if len(users) != 1 {
			t.Fatalf("got %d users, want 1", len(users))
		}
		got := users[0]
		if got.ID != "u-1" {
			t.Errorf("ID = %q, want the uuid preferred over the numeric id", got.ID)
		}
		if got.Username != "kid" {
			t.Errorf("Username = %q, want the username field", got.Username)
		}
		if got.Thumb != "https://example.com/t.png" {
			t.Errorf("Thumb = %q, want it carried through", got.Thumb)
		}
	})

	t.Run("wrapped object, falls back to numeric id and title", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"users":[{"id":7,"title":"Guest Profile"}]}`))
		}))
		defer srv.Close()

		c := NewPlexClient("test-client-id", "Playlist Lab")
		restore := SetPlexAPIBaseForTest(srv.URL, srv.URL+"/")
		defer restore()

		users, err := c.GetHomeUsersDetailed("admin-token")
		if err != nil {
			t.Fatalf("GetHomeUsersDetailed: %v", err)
		}
		if len(users) != 1 || users[0].ID != "7" {
			t.Fatalf("got %+v, want ID \"7\" (no uuid, falls back to the numeric id)", users)
		}
		if users[0].Username != "Guest Profile" {
			t.Errorf("Username = %q, want it to fall back to title when username is empty", users[0].Username)
		}
	})

	t.Run("unauthorized token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		c := NewPlexClient("test-client-id", "Playlist Lab")
		restore := SetPlexAPIBaseForTest(srv.URL, srv.URL+"/")
		defer restore()

		if _, err := c.GetHomeUsersDetailed("bad-token"); err == nil {
			t.Fatal("want an error for a 401 response, got nil")
		}
	})
}
