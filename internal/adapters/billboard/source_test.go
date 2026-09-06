package billboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FetchTracks hits a hardcoded GitHub raw URL, so these tests swap
// httpClient's Transport to redirect at the fixture server instead of doing
// a live network call, matching billboard-scraper.test.ts's invariants
// without its network dependency (which is exactly why that Node suite is
// skipped in this port - see the task brief).
func withFixture(t *testing.T, body string, status int) {
	t.Helper()
	orig := httpClient
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(func() { srv.Close(); httpClient = orig })
	httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme, req.URL.Host = "http", strings.TrimPrefix(srv.URL, "http://")
		return http.DefaultTransport.RoundTrip(req)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestFetchTracks_ParsesFixture(t *testing.T) {
	withFixture(t, `{"date":"2024-01-06","data":[
		{"song":"Lovin On Me","artist":"Jack Harlow"},
		{"song":"Cruel Summer","artist":"Taylor Swift"}
	]}`, http.StatusOK)

	playlist, tracks, err := NewSource().FetchTracks(context.Background(), "https://www.billboard.com/charts/hot-100/", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if playlist.Name != "Billboard Hot 100 - 2024-01-06" {
		t.Errorf("playlist name = %q", playlist.Name)
	}
	if len(tracks) != 2 || tracks[0].Title != "Lovin On Me" || tracks[0].Artist != "Jack Harlow" {
		t.Fatalf("unexpected tracks: %+v", tracks)
	}
}

func TestFetchTracks_SkipsEntriesMissingSongOrArtist(t *testing.T) {
	withFixture(t, `{"date":"2024-01-06","data":[
		{"song":"","artist":"Nobody"},
		{"song":"Real Song","artist":""},
		{"song":"Kept","artist":"Kept Artist"}
	]}`, http.StatusOK)

	_, tracks, err := NewSource().FetchTracks(context.Background(), "https://www.billboard.com/charts/hot-100/", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tracks) != 1 || tracks[0].Title != "Kept" {
		t.Fatalf("expected only the fully-populated entry to survive, got %+v", tracks)
	}
}

func TestFetchTracks_UnsupportedChartType(t *testing.T) {
	_, _, err := NewSource().FetchTracks(context.Background(), "https://www.billboard.com/charts/billboard-200/", 1)
	if err == nil || !strings.Contains(err.Error(), "not currently supported") {
		t.Fatalf("expected an unsupported-chart error, got %v", err)
	}
}

func TestFetchTracks_InvalidDateSurfacesNotFound(t *testing.T) {
	withFixture(t, `not found`, http.StatusNotFound)

	_, _, err := NewSource().FetchTracks(context.Background(), "https://www.billboard.com/charts/hot-100/1900-01-01/", 1)
	if err == nil {
		t.Fatal("expected an error for a date the data source doesn't have")
	}
}

func TestFetchTracks_InvalidURLRejected(t *testing.T) {
	_, _, err := NewSource().FetchTracks(context.Background(), "not-a-billboard-url", 1)
	if err == nil {
		t.Fatal("expected an error for a URL with no /charts/<type> segment")
	}
}
