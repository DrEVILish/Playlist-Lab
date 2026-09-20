package plex

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// Mirrors plex-unreachable.test.ts's expectations for the unreachableUntil
// cooldown: healthy by default, opens on a connection-level failure, closes
// again once the cooldown window has passed, and tracks servers
// independently so one dead server doesn't block another.
func TestUnreachableCooldown(t *testing.T) {
	t.Run("healthy server is not marked down", func(t *testing.T) {
		down, _ := checkUnreachable("http://healthy.local:32400")
		if down {
			t.Fatal("expected a server with no recorded failure to be reachable")
		}
	})

	t.Run("a connection failure opens the cooldown", func(t *testing.T) {
		server := "http://dead-a.local:32400"
		markConnectionFailure(server, errors.New("timeout"))
		down, msg := checkUnreachable(server)
		if !down {
			t.Fatal("expected server to be marked unreachable immediately after a connection failure")
		}
		if msg == "" {
			t.Fatal("expected a non-empty unreachable message")
		}
	})

	t.Run("clearUnreachable recovers the server immediately", func(t *testing.T) {
		server := "http://dead-b.local:32400"
		markConnectionFailure(server, errors.New("timeout"))
		if down, _ := checkUnreachable(server); !down {
			t.Fatal("expected server to be down before clearing")
		}
		clearUnreachable(server)
		if down, _ := checkUnreachable(server); down {
			t.Fatal("expected server to be reachable again after clearUnreachable")
		}
	})

	t.Run("cooldown expires after the window", func(t *testing.T) {
		server := "http://dead-c.local:32400"
		unreachableMu.Lock()
		unreachableUntil[server] = time.Now().Add(-1 * time.Second) // already expired
		unreachableMu.Unlock()

		if down, _ := checkUnreachable(server); down {
			t.Fatal("expected an expired cooldown to let the request through")
		}
	})

	t.Run("servers are tracked independently", func(t *testing.T) {
		dead := "http://dead-d.local:32400"
		alive := "http://alive-d.local:32400"
		markConnectionFailure(dead, errors.New("timeout"))

		if down, _ := checkUnreachable(dead); !down {
			t.Fatal("expected dead server to be unreachable")
		}
		if down, _ := checkUnreachable(alive); down {
			t.Fatal("expected an unrelated server to be unaffected by another server's failure")
		}
	})
}

// do() is the integration point: a request to a server already marked
// unreachable must short-circuit with an *UnreachableError* rather than
// making a real HTTP call (which is the whole point - a dead server
// shouldn't cost every caller the full 60s timeout).
func TestClient_Do_ShortCircuitsWhenUnreachable(t *testing.T) {
	server := "http://short-circuit.local:32400"
	c := NewClient(server, "tok", "client-id", "Test")
	markConnectionFailure(server, errors.New("timeout"))
	defer clearUnreachable(server)

	req, err := http.NewRequest(http.MethodGet, server+"/library/sections", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.do(req)
	if err == nil {
		t.Fatal("expected an error for a server marked unreachable")
	}
	var unreachableErr *UnreachableError
	if !errors.As(err, &unreachableErr) {
		t.Fatalf("expected *UnreachableError, got %T: %v", err, err)
	}
}
