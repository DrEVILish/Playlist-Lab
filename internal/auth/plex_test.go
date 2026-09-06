package auth

import (
	"io"
	"net/http"
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
