package youtubeinnertube

import "testing"

func TestNormalizeArtistName(t *testing.T) {
	cases := map[string]string{
		"The Beatles":      "beatles",
		"Queen - Official": "queen",
		"Artist VEVO":      "artist",
		"Some Music":       "some",
		"A-B_C D":          "abcd",
	}
	for in, want := range cases {
		if got := normalizeArtistName(in); got != want {
			t.Errorf("normalizeArtistName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanTrackTitle(t *testing.T) {
	cases := map[string]string{
		"Bohemian Rhapsody (Official Video)": "Bohemian Rhapsody",
		"Song [4K Remaster]":                 "Song",
		"Track - Remastered":                 "Track",
		"Track - Edit":                       "Track",
	}
	for in, want := range cases {
		if got := cleanTrackTitle(in); got != want {
			t.Errorf("cleanTrackTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSimilarity_IdenticalStringsAreOne(t *testing.T) {
	if got := similarity("bohemian rhapsody", "bohemian rhapsody"); got != 1.0 {
		t.Fatalf("expected 1.0 for identical strings, got %v", got)
	}
}

func TestSimilarity_EmptyLongerIsOne(t *testing.T) {
	// Matches the source's explicit len(longer)==0 special case (both empty).
	if got := similarity("", ""); got != 1.0 {
		t.Fatalf("expected 1.0 when both strings are empty, got %v", got)
	}
}

func TestSimilarity_CompletelyDifferentIsLow(t *testing.T) {
	got := similarity("abc", "xyz")
	if got > 0.5 {
		t.Fatalf("expected a low similarity for disjoint strings, got %v", got)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"kitten", "sitting", 3},
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "", 3},
	}
	for _, tc := range cases {
		if got := levenshtein(tc.a, tc.b); got != tc.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestQualityLabel(t *testing.T) {
	cases := []struct {
		height int
		want   string
	}{
		{2160, "4K"}, {1440, "1440p"}, {1080, "1080p"}, {720, "720p"},
		{480, "480p"}, {360, "360p"}, {240, "240p"}, {100, ""},
	}
	for _, tc := range cases {
		if got := qualityLabel(tc.height); got != tc.want {
			t.Errorf("qualityLabel(%d) = %q, want %q", tc.height, got, tc.want)
		}
	}
}

func TestIsStaticImage(t *testing.T) {
	cases := []struct {
		fps  int
		want bool
	}{
		{0, false}, {1, true}, {5, true}, {6, false}, {30, false},
	}
	for _, tc := range cases {
		if got := isStaticImage(tc.fps); got != tc.want {
			t.Errorf("isStaticImage(%d) = %v, want %v", tc.fps, got, tc.want)
		}
	}
}

// calculateConfidence is the ported calculateYouTubeConfidence() - the
// invariants worth pinning are the same ones the Node suite protected:
// artist-channel exact matches score highest, wrong-artist videos get
// heavily penalized, and quality/penalty bonuses move the score in the
// expected direction without needing every weight nailed down exactly.
func TestCalculateConfidence_ArtistChannelExactTitleScoresHigh(t *testing.T) {
	got := calculateConfidence("Bohemian Rhapsody", "Queen", "Bohemian Rhapsody", "Queen Official", "", false, false, false)
	if got < 0.9 {
		t.Fatalf("expected a near-exact artist-channel match to score high, got %v", got)
	}
}

func TestCalculateConfidence_WrongArtistIsPenalizedHeavily(t *testing.T) {
	match := calculateConfidence("Bohemian Rhapsody", "Queen", "Bohemian Rhapsody", "Queen Official", "", false, false, false)
	wrongArtist := calculateConfidence("Bohemian Rhapsody", "Queen", "Bohemian Rhapsody", "Totally Unrelated Channel", "", false, false, false)
	if wrongArtist >= match {
		t.Fatalf("expected a wrong-artist channel to score lower than the real artist channel: wrong=%v match=%v", wrongArtist, match)
	}
}

func TestCalculateConfidence_LiveIsPenalizedUnlessAllowed(t *testing.T) {
	studio := calculateConfidence("Bohemian Rhapsody", "Queen", "Bohemian Rhapsody", "Queen", "", false, false, false)
	live := calculateConfidence("Bohemian Rhapsody", "Queen", "Bohemian Rhapsody (Live at Wembley)", "Queen", "", false, false, false)
	if live >= studio {
		t.Fatalf("expected a live version to score lower than studio when allowLive=false: live=%v studio=%v", live, studio)
	}

	liveAllowed := calculateConfidence("Bohemian Rhapsody", "Queen", "Bohemian Rhapsody (Live at Wembley)", "Queen", "", true, false, false)
	if liveAllowed <= live {
		t.Fatalf("expected allowLive=true to lift the live penalty: liveAllowed=%v live=%v", liveAllowed, live)
	}
}

func TestCalculateConfidence_GuitarLessonIsPenalized(t *testing.T) {
	real := calculateConfidence("Hotel California", "Eagles", "Hotel California", "Eagles", "", false, false, false)
	lesson := calculateConfidence("Hotel California", "Eagles", "Hotel California Guitar Lesson", "Some Channel", "", false, false, false)
	if lesson >= real {
		t.Fatalf("expected a guitar lesson upload to score lower: lesson=%v real=%v", lesson, real)
	}
}

func TestCalculateConfidence_HigherQualityScoresHigher(t *testing.T) {
	sd := calculateConfidence("Song", "Artist", "Song", "Artist", "480p", false, false, false)
	hd4k := calculateConfidence("Song", "Artist", "Song", "Artist", "4K", false, false, false)
	if hd4k <= sd {
		t.Fatalf("expected 4K to score higher than 480p: 4k=%v sd=%v", hd4k, sd)
	}
}

func TestCalculateConfidence_StaticImagePenalizedUnlessAllowed(t *testing.T) {
	withPenalty := calculateConfidence("Song", "Artist", "Song", "Artist", "", false, true, false)
	withoutPenalty := calculateConfidence("Song", "Artist", "Song", "Artist", "", false, true, true)
	if withoutPenalty <= withPenalty {
		t.Fatalf("expected allowStatic=true to lift the static-image penalty: without=%v with=%v", withoutPenalty, withPenalty)
	}
}
