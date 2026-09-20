package notifications

import (
	"sync"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestAdd_CapsAt50PerUser_DroppingOldest(t *testing.T) {
	s := NewStore()
	var oldest, newest Notification
	for i := 0; i < 51; i++ {
		n := s.Add(1, TypeImport, "t", "d", StatusInProgress, nil)
		if i == 0 {
			oldest = n
		}
		if i == 50 {
			newest = n
		}
	}

	list := s.List(1)
	if len(list) != maxPerUser {
		t.Fatalf("expected the feed capped at %d entries, got %d", maxPerUser, len(list))
	}
	if list[0].ID != newest.ID {
		t.Fatalf("expected the most recent add first, got %+v", list[0])
	}
	for _, n := range list {
		if n.ID == oldest.ID {
			t.Fatalf("expected the oldest entry to have been dropped once the cap was exceeded")
		}
	}
}

func TestUpdate_OmittedFieldKeepsLastKnownValue(t *testing.T) {
	// Pins job-notifications.test.ts: a progress patch that omits a field
	// (e.g. a scraping-phase event with no total to derive a percentage
	// from) must never blow away a previously reported value.
	s := NewStore()
	n := s.Add(1, TypeImport, "Playlist", "Starting...", StatusInProgress, ptr(0))

	s.Update(1, n.ID, Patch{Progress: ptr(40), Detail: ptr("Matching tracks")})
	s.Update(1, n.ID, Patch{Detail: ptr("Loading Spotify playlist...")}) // no Progress field at all

	got := s.List(1)[0]
	if got.Progress == nil || *got.Progress != 40 {
		t.Fatalf("expected progress to remain 40, got %v", got.Progress)
	}
	if got.Detail != "Loading Spotify playlist..." {
		t.Fatalf("expected detail to update, got %q", got.Detail)
	}
}

func TestUpdate_ZeroProgressIsAppliedNotIgnored(t *testing.T) {
	s := NewStore()
	n := s.Add(1, TypeImport, "t", "d", StatusInProgress, ptr(75))
	s.Update(1, n.ID, Patch{Progress: ptr(0)})
	if got := s.List(1)[0].Progress; got == nil || *got != 0 {
		t.Fatalf("expected an explicit zero to be applied, got %v", got)
	}
}

func TestUpdate_UnknownIDIsANoOp(t *testing.T) {
	s := NewStore()
	s.Update(1, "does-not-exist", Patch{Progress: ptr(50)}) // must not panic
}

func TestDismiss_RemovesOnlyThatEntry(t *testing.T) {
	s := NewStore()
	a := s.Add(1, TypeImport, "a", "", StatusSuccess, nil)
	b := s.Add(1, TypeImport, "b", "", StatusSuccess, nil)

	s.Dismiss(1, a.ID)

	list := s.List(1)
	if len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("expected only %q to remain, got %+v", b.ID, list)
	}
}

func TestClear_CompletedOnlyKeepsRunningAndFailedDropsSuccess(t *testing.T) {
	s := NewStore()
	s.Add(2, TypeImport, "Still running", "", StatusInProgress, nil)
	s.Add(2, TypeImport, "Finished ok", "", StatusSuccess, nil)
	s.Add(2, TypeImport, "Failed", "", StatusError, nil)

	s.Clear(2, true)

	titles := map[string]bool{}
	for _, n := range s.List(2) {
		titles[n.Title] = true
	}
	if !titles["Still running"] || !titles["Failed"] || titles["Finished ok"] {
		t.Fatalf("expected running+failed kept and success dropped, got %+v", titles)
	}
	if len(titles) != 2 {
		t.Fatalf("expected exactly 2 remaining entries, got %d", len(titles))
	}
}

func TestClear_AllRemovesSuccessAndErrorButNeverRunning(t *testing.T) {
	s := NewStore()
	s.Add(3, TypeImport, "Still running", "", StatusInProgress, nil)
	s.Add(3, TypeImport, "Finished ok", "", StatusSuccess, nil)
	s.Add(3, TypeImport, "Failed", "", StatusError, nil)

	s.Clear(3, false)

	list := s.List(3)
	if len(list) != 1 || list[0].Title != "Still running" {
		t.Fatalf("expected only the running job to survive 'Clear all', got %+v", list)
	}
}

// This store backs a live SSE feed hit from actionqueue worker goroutines
// (job progress) and HTTP handlers (dismiss/clear) concurrently - a data
// race here would corrupt the per-user slice silently. Run with -race.
func TestConcurrentAddUpdate_NoRace(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n := s.Add(9, TypeImport, "job", "", StatusInProgress, ptr(0))
			s.Update(9, n.ID, Patch{Progress: ptr(i)})
			s.List(9)
		}(i)
	}
	wg.Wait()
}
