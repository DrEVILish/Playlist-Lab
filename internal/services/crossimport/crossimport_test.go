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
