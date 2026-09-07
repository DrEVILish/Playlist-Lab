package listenbrainz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestSource(t *testing.T, body string, status int) *Source {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Source{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme, req.URL.Host = "http", strings.TrimPrefix(srv.URL, "http://")
		return http.DefaultTransport.RoundTrip(req)
	})}}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestFetchTracks_Success(t *testing.T) {
	src := newTestSource(t, `{
		"playlist": {
			"title": "My Playlist",
			"track": [
				{"title": "Track 1", "creator": "Artist 1"},
				{"title": "Track 2", "creator": "Artist 2"}
			]
		}
	}`, http.StatusOK)

	mbid := "11111111-1111-1111-1111-111111111111"
	playlist, tracks, err := src.FetchTracks(context.Background(), mbid, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if playlist.ID != "listenbrainz-"+mbid || playlist.Name != "My Playlist" {
		t.Errorf("unexpected playlist: %+v", playlist)
	}
	if len(tracks) != 2 || tracks[0].Title != "Track 1" || tracks[0].Artist != "Artist 1" {
		t.Fatalf("unexpected tracks: %+v", tracks)
	}
}

func TestFetchTracks_ExtractsMBIDFromURL(t *testing.T) {
	src := newTestSource(t, `{"playlist": {"title": "T", "track": [{"title":"x","creator":"y"}]}}`, http.StatusOK)

	mbid := "22222222-2222-2222-2222-222222222222"
	playlist, _, err := src.FetchTracks(context.Background(), "https://listenbrainz.org/playlist/"+mbid, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if playlist.ID != "listenbrainz-"+mbid {
		t.Errorf("expected extracted MBID in playlist ID, got %q", playlist.ID)
	}
}

func TestFetchTracks_MissingTrackDataDefaultsToUnknown(t *testing.T) {
	src := newTestSource(t, `{
		"playlist": {"title": "My Playlist", "track": [{"title": null, "creator": null}]}
	}`, http.StatusOK)

	_, tracks, err := src.FetchTracks(context.Background(), "11111111-1111-1111-1111-111111111111", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tracks) != 1 || tracks[0].Title != "Unknown" || tracks[0].Artist != "Unknown" {
		t.Fatalf("expected missing title/creator to default to Unknown, got %+v", tracks)
	}
}

func TestFetchTracks_NotFoundStatusSurfacesError(t *testing.T) {
	src := newTestSource(t, `not found`, http.StatusNotFound)

	_, _, err := src.FetchTracks(context.Background(), "11111111-1111-1111-1111-111111111111", 1)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}

func TestFetchTracks_EmptyPlaylistBodySurfacesError(t *testing.T) {
	// An empty/valid-JSON-but-empty playlist object (no title, no tracks)
	// is treated the same as "not found" - mirrors the Node scraper's
	// invariant that a hollow ListenBrainz response isn't silently accepted.
	src := newTestSource(t, `{"playlist": {}}`, http.StatusOK)

	_, _, err := src.FetchTracks(context.Background(), "11111111-1111-1111-1111-111111111111", 1)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a not-found error for an empty playlist body, got %v", err)
	}
}

func TestFetchTracks_NetworkFailure(t *testing.T) {
	src := &Source{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("network error")
	})}}

	_, _, err := src.FetchTracks(context.Background(), "11111111-1111-1111-1111-111111111111", 1)
	if err == nil {
		t.Fatal("expected an error on network failure")
	}
}
