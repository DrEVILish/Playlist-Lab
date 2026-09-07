package matching

import (
	"strings"
	"testing"
)

// TestExplainNoMatch ports match-diagnostics.test.ts. ExplainNoMatch has no
// callers yet (flagged dead code by a ponytail audit pass) - these are a
// pure-function correctness check ahead of any decision on whether to wire
// it in or delete it, exercising it directly rather than skipping it for
// lack of a caller.
func TestExplainNoMatch(t *testing.T) {
	rejected := func(score float64) *RejectedCandidate {
		return &RejectedCandidate{
			RatingKey:   "1",
			PlexTitle:   "Livin' on a Prayer",
			AlbumArtist: "Various Artists",
			TrackArtist: "Bon Jovi Tribute Band",
			Album:       "Rock Anthems",
			Score:       score,
			Reason:      `source artist "Bon Jovi" matched neither (albumArtist=false, trackArtist=false, anyCredit=false)`,
		}
	}

	t.Run("no candidates at all", func(t *testing.T) {
		got := ExplainNoMatch(MatchAttempt{}, nil, 80)
		if !strings.Contains(got, "No candidates") {
			t.Fatalf("expected %q to mention 'No candidates'", got)
		}
	})

	t.Run("gate rejected a candidate that scored above the minimum", func(t *testing.T) {
		attempt := MatchAttempt{CandidateCount: 5, ArtistRejected: 5, BestRejected: rejected(100)}
		got := ExplainNoMatch(attempt, nil, 80)

		// The whole point: pre-empt "but Manual Match says 100%".
		for _, want := range []string{"Manual Match WILL show this as a match", "artist gate", "100%", rejected(100).Reason} {
			if !strings.Contains(got, want) {
				t.Fatalf("expected explanation to contain %q, got: %s", want, got)
			}
		}
	})

	t.Run("gate rejected a candidate that also scored too low", func(t *testing.T) {
		attempt := MatchAttempt{CandidateCount: 5, ArtistRejected: 5, BestRejected: rejected(40)}
		got := ExplainNoMatch(attempt, nil, 80)

		if strings.Contains(got, "Manual Match WILL show this as a match") {
			t.Fatalf("must not claim Manual Match agrees when the rejected candidate also scored too low: %s", got)
		}
		if !strings.Contains(got, "below the 80% minimum") {
			t.Fatalf("expected a score-shortfall message, got: %s", got)
		}
	})

	t.Run("candidate passed the gate but scored under the minimum", func(t *testing.T) {
		match := &Match{Score: 61.4, PlexTitle: "Livin' on a Prayer", PlexArtist: "Bon Jovi"}
		got := ExplainNoMatch(MatchAttempt{CandidateCount: 3}, match, 80)

		if !strings.Contains(got, "61%") || !strings.Contains(got, "below the 80% minimum") {
			t.Fatalf("expected a 61%% / below-minimum shortfall message, got: %s", got)
		}
	})

	t.Run("candidates found but none had a matching title", func(t *testing.T) {
		attempt := MatchAttempt{CandidateCount: 7, TitleRejected: 7}
		got := ExplainNoMatch(attempt, nil, 80)
		want := "7 candidate(s) found but none had a matching title."
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}
