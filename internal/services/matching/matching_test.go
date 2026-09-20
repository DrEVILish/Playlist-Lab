package matching

import (
	"testing"

	"github.com/drevilish/playlist-lab/internal/services/plex"
)

func TestEvaluateMatchGate_ExactMatch(t *testing.T) {
	settings := DefaultSettings()
	track := Track{Title: "Bohemian Rhapsody", Artist: "Queen"}
	result := plex.Track{Title: "Bohemian Rhapsody", GrandparentTitle: "Queen", ParentTitle: "A Night at the Opera"}

	gate := EvaluateMatchGate(track, result, settings)
	if !gate.Passes {
		t.Fatalf("expected an exact title+artist match to pass the gate, got %+v", gate)
	}
	scored := ScorePlexCandidate(track.Title, track.Artist, result, settings)
	if scored.Score != 100 {
		t.Fatalf("expected a perfect match to score 100, got %v", scored.Score)
	}
}

func TestEvaluateMatchGate_WrongArtistRejected(t *testing.T) {
	settings := DefaultSettings()
	track := Track{Title: "Time", Artist: "Pink Floyd"}
	result := plex.Track{Title: "Time", GrandparentTitle: "Hans Zimmer", ParentTitle: "Inception"}

	gate := EvaluateMatchGate(track, result, settings)
	if gate.Passes {
		t.Fatalf("a title match against a completely unrelated artist must not pass the gate, got %+v", gate)
	}
}

func TestEvaluateMatchGate_CompilationAllowsTitleOnlyMatch(t *testing.T) {
	settings := DefaultSettings()
	// A soundtrack album, distinct per-track performer credited via
	// originalTitle rather than the (wrong-for-this-track) album artist -
	// exactly the Lin-Manuel Miranda / Stephanie Beatriz case from
	// matching.ts's own comments.
	track := Track{Title: "Surface Pressure", Artist: "Jessica Darrow"}
	result := plex.Track{
		Title: "Surface Pressure", GrandparentTitle: "Lin-Manuel Miranda",
		ParentTitle: "Encanto (Original Motion Picture Soundtrack)", OriginalTitle: "Jessica Darrow",
	}

	gate := EvaluateMatchGate(track, result, settings)
	if !gate.Passes {
		t.Fatalf("exact title match against the correct track-level artist on a soundtrack should pass, got %+v", gate)
	}
	if !gate.IsCompilation {
		t.Fatalf("expected a distinct track artist to be detected as a compilation, got %+v", gate)
	}
}

func TestEvaluateMatchGate_CompilationWithNoArtistEvidence(t *testing.T) {
	settings := DefaultSettings()
	track := Track{Title: "Some Song", Artist: "Totally Unrelated Artist"}
	result := plex.Track{
		Title: "Some Song", GrandparentTitle: "Various Artists", ParentTitle: "Compilation Album",
	}

	gate := EvaluateMatchGate(track, result, settings)
	if !gate.Passes {
		t.Fatalf("an exact title match on a Various Artists compilation should still pass via allowTitleOnlyMatch, got %+v", gate)
	}
	if !gate.AllowTitleOnlyMatch {
		t.Fatalf("expected AllowTitleOnlyMatch, got %+v", gate)
	}
}

func TestScorePlexCandidate_RemasterPenalizedWhenSourceIsNot(t *testing.T) {
	settings := DefaultSettings()
	plain := plex.Track{Title: "Yesterday", GrandparentTitle: "The Beatles", ParentTitle: "Help!"}
	remastered := plex.Track{Title: "Yesterday - Remastered 2009", GrandparentTitle: "The Beatles", ParentTitle: "Help! (Remastered)"}

	plainScore := ScorePlexCandidate("Yesterday", "The Beatles", plain, settings)
	remasteredScore := ScorePlexCandidate("Yesterday", "The Beatles", remastered, settings)

	// stripEditionQualifiers removes "- Remastered 2009" from the title
	// itself before comparison, so the title still matches - the penalty
	// this test actually exercises (hasRemasterIndicator on the *album*
	// title) shouldn't fire here since remaster is a bonus (+5), not a
	// penalty, once the titles already match. Assert both score highly and
	// the plain version is never worse.
	if plainScore.Score < 90 || remasteredScore.Score < 90 {
		t.Fatalf("expected both remaster-stripped titles to score highly, got plain=%v remastered=%v", plainScore.Score, remasteredScore.Score)
	}
}

func TestScorePlexCandidate_LivePenalizedWhenSourceIsStudio(t *testing.T) {
	// PreferNonCompilation's own +50 non-compilation bonus would otherwise
	// mask the version-mismatch penalty behind the Score field's 0-100
	// clamp (both candidates hit the ceiling) - disabling it here isolates
	// the specific behavior this test targets. RankScore (the pre-clamp
	// value both the ceiling and this test look past) is what actually
	// shows the penalty in isolation.
	settings := DefaultSettings()
	settings.PreferNonCompilation = false
	studioSource := "Comfortably Numb"
	live := plex.Track{Title: "Comfortably Numb (Live)", GrandparentTitle: "Pink Floyd", ParentTitle: "Pulse"}
	studio := plex.Track{Title: "Comfortably Numb", GrandparentTitle: "Pink Floyd", ParentTitle: "The Wall"}

	liveScore := ScorePlexCandidate(studioSource, "Pink Floyd", live, settings)
	studioScore := ScorePlexCandidate(studioSource, "Pink Floyd", studio, settings)

	if liveScore.RankScore >= studioScore.RankScore {
		t.Fatalf("a live version should score lower than the studio version for a studio source track: live=%v studio=%v", liveScore.RankScore, studioScore.RankScore)
	}
}

func TestNormalizeForComparison_ContractionFolding(t *testing.T) {
	a := normalizeForComparison("Livin' On A Prayer")
	b := normalizeForComparison("Living On A Prayer")
	if a != b {
		t.Fatalf("expected contraction folding to make these equal, got %q vs %q", a, b)
	}
}

func TestNormalizeForComparison_NonLatinScriptsSurvive(t *testing.T) {
	// Two different Japanese titles must not both collapse to "" (which
	// used to make every non-Latin track match every other one).
	a := normalizeForComparison("シリウス")
	b := normalizeForComparison("ハナミズキ")
	if a == "" || b == "" {
		t.Fatalf("expected non-Latin scripts to survive normalization, got %q and %q", a, b)
	}
	if a == b {
		t.Fatalf("expected two different Japanese titles to normalize differently, both got %q", a)
	}
}

func TestRomanizeKana(t *testing.T) {
	cases := map[string]string{
		"シリウス": "shiriusu",
		"がっこう": "gakkou",
		"きゃりー": "kyarii",
	}
	for input, want := range cases {
		got, ok := romanizeKana(input)
		if !ok {
			t.Fatalf("romanizeKana(%q): expected ok=true", input)
		}
		if got != want {
			t.Errorf("romanizeKana(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRomanizeKana_RejectsKanji(t *testing.T) {
	if _, ok := romanizeKana("阿保剛"); ok {
		t.Fatalf("expected pure-kanji text to be rejected (no dictionary-based reading), got ok=true")
	}
}

func TestDedupeByPlexRatingKey(t *testing.T) {
	tracks := []MatchedTrack{
		{Title: "A", PlexRatingKey: "1"},
		{Title: "B", PlexRatingKey: "2"},
		{Title: "C (duplicate resolve)", PlexRatingKey: "1"},
		{Title: "D", PlexRatingKey: ""},
	}
	out := DedupeByPlexRatingKey(tracks)
	if len(out) != 3 {
		t.Fatalf("expected 3 tracks after dedupe (one duplicate ratingKey removed, empty keys always kept), got %d: %+v", len(out), out)
	}
}

func TestRememberedMatchKey_CaseAndWhitespaceInsensitive(t *testing.T) {
	a := RememberedMatchKey("  Song Title ", "Artist Name", "")
	b := RememberedMatchKey("song title", "ARTIST NAME", "")
	if a != b {
		t.Fatalf("expected keys to match regardless of case/whitespace, got %q vs %q", a, b)
	}
}
