package mixes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

func writeMediaContainer(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": body})
}

// TestGenerateWeeklyMix_PicksTopArtistsPopularTracks is the first end-to-end
// test of any mix generator - every other test in this package covered a
// small pure helper (mapBatched, ceilDiv, hasAny), never a real
// Generate*Mix call against a Plex library. Mimics a library where three
// artists have been listened to different amounts (plus some "Various
// Artists" plays that must never count toward an artist's own tally), and
// checks the mix keeps only the top N artists by play count and pulls each
// one's popular tracks from Plex's hub endpoint.
func TestGenerateWeeklyMix_PicksTopArtistsPopularTracks(t *testing.T) {
	// GetRecentTracks re-filters Plex's response client-side by lastViewedAt
	// (unix seconds) against a "days ago" cutoff, so a fixture track needs a
	// recent one to survive that filter at all.
	recentTrack := func(ratingKey, artist string) map[string]any {
		return map[string]any{
			"ratingKey": ratingKey, "title": "Song " + ratingKey, "grandparentTitle": artist,
			"lastViewedAt": time.Now().Add(-1 * time.Hour).Unix(),
		}
	}
	var recent []map[string]any
	for i := 0; i < 10; i++ {
		recent = append(recent, recentTrack(fmt.Sprintf("x%d", i), "Artist X"))
	}
	for i := 0; i < 8; i++ {
		recent = append(recent, recentTrack(fmt.Sprintf("y%d", i), "Artist Y"))
	}
	for i := 0; i < 5; i++ {
		recent = append(recent, recentTrack(fmt.Sprintf("z%d", i), "Artist Z"))
	}
	for i := 0; i < 2; i++ {
		recent = append(recent, recentTrack(fmt.Sprintf("va%d", i), "Various Artists"))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/library/sections/1/all", func(w http.ResponseWriter, r *http.Request) {
		switch title := r.URL.Query().Get("title"); title {
		case "Artist X":
			writeMediaContainer(w, map[string]any{"Metadata": []map[string]any{{"ratingKey": "artist-x", "title": "Artist X"}}})
		case "Artist Y":
			writeMediaContainer(w, map[string]any{"Metadata": []map[string]any{{"ratingKey": "artist-y", "title": "Artist Y"}}})
		case "":
			// The recent-tracks query has no title param, only type=10.
			writeMediaContainer(w, map[string]any{"Metadata": recent})
		default:
			t.Errorf("unexpected artist lookup: %q", title)
			writeMediaContainer(w, map[string]any{"Metadata": []map[string]any{}})
		}
	})
	popularHub := func(prefix string) map[string]any {
		var tracks []json.RawMessage
		for i := 0; i < 3; i++ {
			raw, _ := json.Marshal(map[string]any{"ratingKey": fmt.Sprintf("%s-popular-%d", prefix, i), "title": "Popular"})
			tracks = append(tracks, raw)
		}
		return map[string]any{"Hub": []map[string]any{{"type": "track", "title": "Popular", "Metadata": tracks}}}
	}
	mux.HandleFunc("/hubs/sections/1", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("metadataItemId") {
		case "artist-x":
			writeMediaContainer(w, popularHub("x"))
		case "artist-y":
			writeMediaContainer(w, popularHub("y"))
		default:
			t.Errorf("unexpected hub lookup: %q", r.URL.Query().Get("metadataItemId"))
			writeMediaContainer(w, map[string]any{})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := New()
	result, err := svc.GenerateWeeklyMix(srv.URL, "token", "1", WeeklyMixSettings{TopArtists: 2, TracksPerArtist: 3})
	if err != nil {
		t.Fatalf("GenerateWeeklyMix: %v", err)
	}
	if result.TrackCount != 6 {
		t.Fatalf("TrackCount = %d, want 6 (3 tracks each from the top 2 artists, Artist Z excluded)", result.TrackCount)
	}
	got := append([]string(nil), result.TrackKeys...)
	sort.Strings(got)
	want := []string{"x-popular-0", "x-popular-1", "x-popular-2", "y-popular-0", "y-popular-1", "y-popular-2"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("TrackKeys = %v, want (in some order) %v", result.TrackKeys, want)
		}
	}
}
