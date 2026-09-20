package scrapers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// DeezerCharts/SearchAppleMusicPlaylists hit hardcoded real API hosts, so
// these tests swap the package's httpClient Transport to redirect every
// request at a single local fixture server, keyed by request path.
func withFixture(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	orig := httpClient
	srv := httptest.NewServer(handler)
	t.Cleanup(func() { srv.Close(); httpClient = orig })
	httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme, req.URL.Host = "http", strings.TrimPrefix(srv.URL, "http://")
		return http.DefaultTransport.RoundTrip(req)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDeezerCharts_Global(t *testing.T) {
	withFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data": [{"title": "Top Track 1", "artist": {"name": "Artist 1"}, "album": {"title": "Album 1"}}]}`))
	})

	result := DeezerCharts("global")
	if len(result) != 1 {
		t.Fatalf("expected 1 playlist, got %d", len(result))
	}
	if result[0].ID != "deezer-top-global" || result[0].Name != "Top 50 Global" {
		t.Errorf("unexpected playlist: %+v", result[0])
	}
	if len(result[0].Tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(result[0].Tracks))
	}
}

func TestDeezerCharts_CountrySpecific(t *testing.T) {
	withFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/chart/"):
			w.Write([]byte(`{"data": [{"title": "Top Track 1", "artist": {"name": "Artist 1"}, "album": {"title": "Album 1"}}]}`))
		case strings.Contains(r.URL.Path, "/search/playlist"):
			w.Write([]byte(`{"data": [{"id": 456, "title": "Top 50 United States"}]}`))
		case strings.Contains(r.URL.Path, "/playlist/456"):
			w.Write([]byte(`{"data": [{"title": "US Track 1", "artist": {"name": "US Artist 1"}, "album": {"title": "US Album 1"}}]}`))
		default:
			t.Errorf("unexpected request path %s", r.URL.Path)
		}
	})

	result := DeezerCharts("us")
	if len(result) != 2 {
		t.Fatalf("expected 2 playlists (global + country), got %d", len(result))
	}
	if result[1].ID != "deezer-top-us" || result[1].Name != "Top 50 United States" {
		t.Errorf("unexpected country playlist: %+v", result[1])
	}
}

func TestDeezerCharts_ErrorsReturnEmptySlice(t *testing.T) {
	withFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	result := DeezerCharts("global")
	if len(result) != 0 {
		t.Fatalf("expected an empty slice on error, got %+v", result)
	}
}

func TestSearchAppleMusicPlaylists_Success(t *testing.T) {
	withFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"feed": {"results": [{"name": "Top 100", "url": "https://example.com/p", "artistName": "Various Artists"}]}}`))
	})

	result := SearchAppleMusicPlaylists("us")
	if len(result) != 1 || result[0].Name != "Top 100" || result[0].Description != "Various Artists" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestSearchAppleMusicPlaylists_ErrorReturnsNil(t *testing.T) {
	withFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	result := SearchAppleMusicPlaylists("us")
	if result != nil {
		t.Fatalf("expected nil on error, got %+v", result)
	}
}

func TestAriaCharts_IsANoOp(t *testing.T) {
	// AriaCharts is intentionally a no-op - individual ARIA chart pages are
	// fetched one at a time via adapters/aria's browser-based FetchTracks
	// instead, per the doc comment on the function.
	if result := AriaCharts(); result != nil {
		t.Fatalf("expected AriaCharts to always return nil, got %+v", result)
	}
}
