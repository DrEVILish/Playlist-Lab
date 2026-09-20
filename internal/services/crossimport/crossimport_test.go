package crossimport

import (
	"testing"

	"github.com/drevilish/playlist-lab/internal/adapters"
)

// TestSessionUpdate is the one ponytail self-check for this package's
// trickiest bit of non-trivial logic: Update must mutate the session's own
// results in place by index (so a track override/skip from the review page
// sticks), reject an out-of-range index instead of panicking, and
// GetResults must hand back a copy - a caller mutating its slice must never
// bleed back into session state it didn't go through Update for.
func TestSessionUpdate(t *testing.T) {
	s := &Session{Results: []adapters.MatchResult{
		{SourceTrack: adapters.TrackInfo{Title: "a"}, Matched: true},
		{SourceTrack: adapters.TrackInfo{Title: "b"}, Matched: true},
	}}

	if ok := s.Update(0, func(m *adapters.MatchResult) { m.Skipped = true }); !ok {
		t.Fatal("expected Update(0, ...) to succeed")
	}
	if !s.Results[0].Skipped {
		t.Fatal("expected in-place mutation of Results[0]")
	}
	if s.Results[1].Skipped {
		t.Fatal("Update(0, ...) must not touch Results[1]")
	}

	if ok := s.Update(5, func(m *adapters.MatchResult) { m.Skipped = true }); ok {
		t.Fatal("expected out-of-range Update to return false")
	}
	if ok := s.Update(-1, func(m *adapters.MatchResult) { m.Skipped = true }); ok {
		t.Fatal("expected negative Update to return false")
	}

	copyOut := s.GetResults()
	copyOut[1].Skipped = true
	if s.Results[1].Skipped {
		t.Fatal("GetResults must return an independent copy")
	}
}

// TestSessionCancelIsolation ports cross-import-sse.test.ts's "cancel sets
// flag" / "does not affect other sessions" cases: Cancel/IsCancelled must be
// per-session state, not shared across the store.
func TestSessionCancelIsolation(t *testing.T) {
	s1 := &Session{}
	s2 := &Session{}

	if s1.IsCancelled() {
		t.Fatal("new session must not start cancelled")
	}
	s1.Cancel()
	if !s1.IsCancelled() {
		t.Fatal("expected Cancel to set the flag")
	}
	if s2.IsCancelled() {
		t.Fatal("cancelling one session must not cancel another")
	}
}

// TestSessionProgressRoundTrip covers SetProgress/GetProgress - the "each new
// event overwrites the state" behaviour the TS test pinned on the raw
// matchProgressState map, ported here to the Progress struct that replaced
// it.
func TestSessionProgressRoundTrip(t *testing.T) {
	s := &Session{}
	s.SetProgress(Progress{Phase: PhaseFetching})
	if got := s.GetProgress(); got.Phase != PhaseFetching {
		t.Fatalf("expected phase %q, got %q", PhaseFetching, got.Phase)
	}

	s.SetProgress(Progress{Phase: PhaseMatching, Current: 10, Total: 50})
	got := s.GetProgress()
	if got.Phase != PhaseMatching || got.Current != 10 || got.Total != 50 {
		t.Fatalf("expected latest progress to overwrite prior state, got %+v", got)
	}
}

// TestSessionSetResultsSetsReviewPhase covers SetResults' side effect: the
// TS suite's "job status transitions from matching to review on successful
// match" - in this port that transition happens as part of SetResults itself
// rather than a separate route step.
func TestSessionSetResultsSetsReviewPhase(t *testing.T) {
	s := &Session{Progress: Progress{Phase: PhaseMatching}}
	s.SetResults(nil)
	if got := s.GetProgress().Phase; got != PhaseReview {
		t.Fatalf("expected SetResults to move phase to %q, got %q", PhaseReview, got)
	}
}

func TestStoreNewAndDelete(t *testing.T) {
	store := NewStore()
	sess := store.New("sid", 42, "youtube", adapters.TargetConfig{})
	if sess.JobID != 42 || sess.TargetID != "youtube" {
		t.Fatalf("unexpected session fields: %+v", sess)
	}
	if _, ok := store.Get("sid"); !ok {
		t.Fatal("expected session to be retrievable after New")
	}
	store.Delete("sid")
	if _, ok := store.Get("sid"); ok {
		t.Fatal("expected session to be gone after Delete")
	}
}
