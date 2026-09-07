package mixes

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestGenerateTimeCapsule_RespectsMaxPerArtist covers the round-robin
// selection loop's actual business rule: pull tracks across as many
// different artists as possible up to TrackCount, but never more than
// MaxPerArtist from any single one - a library dominated by one prolific
// artist must not turn a "time capsule" into that one artist's greatest
// hits.
func TestGenerateTimeCapsule_RespectsMaxPerArtist(t *testing.T) {
	oldPlay := time.Now().Add(-365 * 24 * time.Hour).Unix() // well past any DaysAgo cutoff below
	staleTrack := func(ratingKey, artist string) map[string]any {
		return map[string]any{"ratingKey": ratingKey, "title": "Song " + ratingKey, "grandparentTitle": artist, "lastViewedAt": oldPlay}
	}
	var stale []map[string]any
	for _, artist := range []string{"Artist A", "Artist B", "Artist C"} {
		for i := 0; i < 5; i++ {
			stale = append(stale, staleTrack(fmt.Sprintf("%s-%d", artist, i), artist))
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/library/sections/1/all", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("viewCount") == "0" {
			writeMediaContainer(w, map[string]any{"Metadata": []map[string]any{}}) // no never-played tracks in this library
			return
		}
		writeMediaContainer(w, map[string]any{"Metadata": stale})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := New()
	result, err := svc.GenerateTimeCapsule(srv.URL, "token", "1", TimeCapsuleSettings{TrackCount: 6, DaysAgo: 90, MaxPerArtist: 2})
	if err != nil {
		t.Fatalf("GenerateTimeCapsule: %v", err)
	}
	if result.TrackCount != 6 {
		t.Fatalf("TrackCount = %d, want 6", result.TrackCount)
	}

	perArtist := map[string]int{}
	for _, key := range result.TrackKeys {
		for _, artist := range []string{"Artist A", "Artist B", "Artist C"} {
			if len(key) > len(artist) && key[:len(artist)] == artist {
				perArtist[artist]++
			}
		}
	}
	for artist, count := range perArtist {
		if count > 2 {
			t.Errorf("%s contributed %d tracks, want at most MaxPerArtist=2", artist, count)
		}
	}
	if len(perArtist) != 3 {
		t.Errorf("expected all 3 artists represented (round-robin, not just the first one drained), got %v", perArtist)
	}
}
