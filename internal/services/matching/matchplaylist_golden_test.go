package matching

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drevilish/playlist-lab/internal/services/plex"
)

// fakeTrack is the minimal Plex "Metadata" item shape both the artist
// lookup (type=8) and the artist's full track list (allLeaves, type=10)
// responses share.
type fakeTrack struct {
	RatingKey        string `json:"ratingKey"`
	Title            string `json:"title"`
	GrandparentTitle string `json:"grandparentTitle,omitempty"`
	ParentTitle      string `json:"parentTitle,omitempty"`
}

func writeMetadata(w http.ResponseWriter, items []fakeTrack) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"MediaContainer": map[string]any{"Metadata": items},
	})
}

// TestMatchPlaylist_GoldenCase is an end-to-end regression test for the
// pipeline every import/mix/schedule path shares (MatchPlaylist ->
// FindBestMatch -> FindPlexCandidates's artist-first search ->
// PickEffectiveVariant -> ScorePlexCandidate), which had no top-level test
// at all before this - only its individual pieces were unit-tested. Mimics
// a real Plex library: one artist ("Daft Punk") whose catalog contains both
// the correct plain-studio track and a decoy live version sharing enough of
// the title to also pass the search's literal-substring pre-filter, so a
// real regression in the live-penalty scoring (not just its isolated unit
// test) would show up here as the wrong track winning.
func TestMatchPlaylist_GoldenCase(t *testing.T) {
	catalog := []fakeTrack{
		{RatingKey: "101", Title: "Get Lucky", GrandparentTitle: "Daft Punk", ParentTitle: "Random Access Memories"},
		// Listed before the correct studio track on purpose (order must not
		// matter): verified against the real implementation that 102 wins
		// with a comfortable, non-tied score margin, so this isn't relying
		// on iteration order to land on the right answer.
		{RatingKey: "103", Title: "Harder, Better, Faster, Stronger (Live)", GrandparentTitle: "Daft Punk", ParentTitle: "Alive 2007"},
		{RatingKey: "102", Title: "Harder, Better, Faster, Stronger", GrandparentTitle: "Daft Punk", ParentTitle: "Discovery"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/library/sections/1/all", func(w http.ResponseWriter, r *http.Request) {
		// The artist lookup (type=8): one matching artist row is enough for
		// searchByArtistFirst to move on to fetching its full catalog.
		writeMetadata(w, []fakeTrack{{RatingKey: "artist-1", Title: "Daft Punk"}})
	})
	mux.HandleFunc("/library/metadata/artist-1/allLeaves", func(w http.ResponseWriter, r *http.Request) {
		writeMetadata(w, catalog)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := plex.NewClient(srv.URL, "token", "client-id", "Playlist Lab")
	settings := DefaultSettings()

	tracks := []Track{
		// Featured artist + parenthetical must be stripped before the title
		// cleanly matches the plain Plex track.
		{Title: "Get Lucky (feat. Pharrell Williams)", Artist: "Daft Punk", Album: "Random Access Memories"},
		// A plain title against a catalog containing both the real studio
		// track and a title-superset live decoy.
		{Title: "Harder, Better, Faster, Stronger", Artist: "Daft Punk", Album: "Discovery"},
	}

	matched, err := MatchPlaylist(tracks, client, "1", settings, nil, nil, nil)
	if err != nil {
		t.Fatalf("MatchPlaylist: %v", err)
	}
	if len(matched) != 2 {
		t.Fatalf("got %d matched tracks, want 2", len(matched))
	}

	if !matched[0].Matched || matched[0].PlexRatingKey != "101" {
		t.Errorf("track 1 = %+v, want a match on ratingKey 101 (Get Lucky)", matched[0])
	}
	if !matched[1].Matched || matched[1].PlexRatingKey != "102" {
		t.Errorf("track 2 = %+v, want a match on ratingKey 102 (the studio version, not the 103 live decoy)", matched[1])
	}
}
