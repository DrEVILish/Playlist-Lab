package plex

import "testing"

// BuildTrackURI/BuildLibraryURI produce the server://<id>/... URIs Plex
// expects on every playlist create/merge/clone/share call - a subtly wrong
// format here breaks the whole app silently (Plex just 400s the mutation).
func TestBuildTrackURI(t *testing.T) {
	got := BuildTrackURI("abc123", "999")
	want := "server://abc123/com.plexapp.plugins.library/library/metadata/999"
	if got != want {
		t.Fatalf("BuildTrackURI() = %q, want %q", got, want)
	}
}

func TestClient_BuildLibraryURI(t *testing.T) {
	c := &Client{ClientID: "fallback-id"}

	t.Run("prefers machine identifier when given", func(t *testing.T) {
		got := c.BuildLibraryURI("5", "machine-id")
		want := "server://machine-id/com.plexapp.plugins.library/library/sections/5"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to client ID when no machine identifier", func(t *testing.T) {
		got := c.BuildLibraryURI("5", "")
		want := "server://fallback-id/com.plexapp.plugins.library/library/sections/5"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestClient_BuildTrackURI(t *testing.T) {
	c := &Client{ClientID: "fallback-id"}

	t.Run("prefers machine identifier when given", func(t *testing.T) {
		got := c.BuildTrackURI("42", "machine-id")
		want := "server://machine-id/com.plexapp.plugins.library/library/metadata/42"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to client ID when no machine identifier", func(t *testing.T) {
		got := c.BuildTrackURI("42", "")
		want := "server://fallback-id/com.plexapp.plugins.library/library/metadata/42"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

// ResolveToken: a server-specific access token (present when the server was
// merely shared with the user) must win over the account token - using the
// account token against a shared server sends the user into an unbreakable
// re-login loop (see the doc comment on ResolveToken in client.go).
func TestResolveToken(t *testing.T) {
	cases := []struct {
		name              string
		userToken         string
		serverAccessToken string
		want              string
	}{
		{"server access token wins when both present", "user-tok", "server-tok", "server-tok"},
		{"falls back to user token when no server token", "user-tok", "", "user-tok"},
		{"empty when neither present", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveToken(tc.userToken, tc.serverAccessToken); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
