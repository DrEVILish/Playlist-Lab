package deezer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FetchTracks hits the real api.deezer.com host, so these tests swap the
// Source's httpClient Transport to redirect at a local fixture server,
// matching scrapers.test.ts's scrapeDeezerPlaylist invariants (missing
// artist names, invalid response, network failure) without live network.
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
		"id": 123, "title": "Test Playlist", "description": "Test Description",
		"tracks": {"data": [
			{"title": "Track 1", "artist": {"name": "Artist 1"}, "album": {"title": "Album 1"}},
			{"title": "Track 2", "artist": {"name": "Artist 2"}, "album": {"title": "Album 2"}}
		]}
	}`, http.StatusOK)

	playlist, tracks, err := src.FetchTracks(context.Background(), "123", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if playlist.ID != "deezer-123" || playlist.Name != "Test Playlist" {
		t.Errorf("unexpected playlist: %+v", playlist)
	}
	if len(tracks) != 2 || tracks[0].Title != "Track 1" || tracks[0].Artist != "Artist 1" || tracks[0].Album != "Album 1" {
		t.Fatalf("unexpected tracks: %+v", tracks)
	}
}

func TestFetchTracks_ExtractsIDFromURL(t *testing.T) {
	src := newTestSource(t, `{"id": 123, "title": "T", "tracks": {"data": []}}`, http.StatusOK)

	playlist, _, err := src.FetchTracks(context.Background(), "https://www.deezer.com/playlist/123", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if playlist.ID != "deezer-123" {
		t.Errorf("expected playlist ID to use extracted numeric ID, got %q", playlist.ID)
	}
}

func TestFetchTracks_MissingArtistNameDefaultsToUnknown(t *testing.T) {
	src := newTestSource(t, `{
		"id": 123, "title": "Test Playlist", "description": "",
		"tracks": {"data": [{"title": "Track 1", "artist": {}, "album": {"title": "Album 1"}}]}
	}`, http.StatusOK)

	_, tracks, err := src.FetchTracks(context.Background(), "123", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tracks) != 1 || tracks[0].Artist != "Unknown" {
		t.Fatalf("expected missing artist to default to Unknown, got %+v", tracks)
	}
}

func TestFetchTracks_ErrorFieldSurfacesAPIMessage(t *testing.T) {
	src := newTestSource(t, `{"error": {"message": "no data"}}`, http.StatusOK)

	_, _, err := src.FetchTracks(context.Background(), "123", 1)
	if err == nil || !strings.Contains(err.Error(), "no data") {
		t.Fatalf("expected the Deezer API error message to surface, got %v", err)
	}
}

func TestFetchTracks_Forbidden(t *testing.T) {
	src := newTestSource(t, `{}`, http.StatusForbidden)

	_, _, err := src.FetchTracks(context.Background(), "123", 1)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("expected an unavailable-playlist error, got %v", err)
	}
}

func TestFetchTracks_NetworkFailure(t *testing.T) {
	src := &Source{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("network error")
	})}}

	_, _, err := src.FetchTracks(context.Background(), "123", 1)
	if err == nil || !strings.Contains(err.Error(), "failed to scrape Deezer playlist") {
		t.Fatalf("expected a wrapped scrape-failure error, got %v", err)
	}
}
