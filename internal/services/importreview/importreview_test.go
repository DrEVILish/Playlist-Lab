package importreview

import (
	"testing"

	"github.com/drevilish/playlist-lab/internal/services/matching"
)

func TestFromMatchedTracks_FlattensMatchedThenUnmatched(t *testing.T) {
	matched := []matching.MatchedTrack{{Title: "A", Matched: true, PlexRatingKey: "1"}}
	unmatched := []matching.MatchedTrack{{Title: "B", Matched: false}}

	got := FromMatchedTracks(matched, unmatched)
	if len(got) != 2 {
		t.Fatalf("got %d tracks, want 2", len(got))
	}
	if got[0].Title != "A" || !got[0].Matched {
		t.Errorf("track 0 = %+v, want the matched track first", got[0])
	}
	if got[1].Title != "B" || got[1].Matched {
		t.Errorf("track 1 = %+v, want the unmatched track second", got[1])
	}
}

func TestSession_UpdateEditsOneTrackWithoutAffectingOthers(t *testing.T) {
	store := NewStore()
	sess := store.New("deezer", "123", "My Mix", "", 1)
	sess.SetTracks([]Track{{Title: "A"}, {Title: "B"}})

	ok := sess.Update(1, func(tr *Track) {
		tr.Matched = true
		tr.PlexRatingKey = "rk-9"
	})
	if !ok {
		t.Fatal("Update returned false for a valid index")
	}

	tracks := sess.Tracks()
	if tracks[0].Matched {
		t.Error("track 0 was mutated by an update targeting track 1")
	}
	if !tracks[1].Matched || tracks[1].PlexRatingKey != "rk-9" {
		t.Errorf("track 1 = %+v, want the edit applied", tracks[1])
	}
}

func TestSession_UpdateOutOfRangeReturnsFalse(t *testing.T) {
	sess := &Session{}
	sess.SetTracks([]Track{{Title: "A"}})
	if sess.Update(5, func(tr *Track) { tr.Matched = true }) {
		t.Error("Update returned true for an out-of-range index")
	}
	if sess.Update(-1, func(tr *Track) { tr.Matched = true }) {
		t.Error("Update returned true for a negative index")
	}
}

func TestSession_Counts(t *testing.T) {
	sess := &Session{}
	sess.SetTracks([]Track{
		{Matched: true},
		{Matched: true},
		{Matched: false},
		{Matched: true, Skipped: true}, // skipped outranks matched in the tally
	})
	total, matched, unmatched, skipped := sess.Counts()
	if total != 4 || matched != 2 || unmatched != 1 || skipped != 1 {
		t.Errorf("Counts() = (%d,%d,%d,%d), want (4,2,1,1)", total, matched, unmatched, skipped)
	}
}

func TestStore_NewGetDelete(t *testing.T) {
	store := NewStore()
	sess := store.New("deezer", "123", "My Mix", "https://example.com/cover.jpg", 1)

	got, ok := store.Get(sess.ID, 1)
	if !ok || got != sess {
		t.Fatal("Get did not return the session New created")
	}
	if got.CoverURL() != "https://example.com/cover.jpg" {
		t.Errorf("CoverURL = %q, want it preserved from New", got.CoverURL())
	}

	store.Delete(sess.ID)
	if _, ok := store.Get(sess.ID, 1); ok {
		t.Error("session still retrievable after Delete")
	}
}

func TestStore_GetRejectsWrongUser(t *testing.T) {
	store := NewStore()
	sess := store.New("deezer", "123", "My Mix", "", 1)

	if _, ok := store.Get(sess.ID, 2); ok {
		t.Error("Get returned a session belonging to a different user")
	}
	if got, ok := store.Get(sess.ID, 1); !ok || got != sess {
		t.Error("Get should still succeed for the owning user")
	}
}
