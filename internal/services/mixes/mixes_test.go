package mixes

import (
	"testing"

	"github.com/drevilish/playlist-lab/internal/services/plex"
)

// TestMapBatchedPreservesOrder is the one ponytail self-check for this
// package's trickiest bit of non-trivial logic: mapBatched must return
// results in input order even though it runs each batch concurrently.
func TestMapBatchedPreservesOrder(t *testing.T) {
	in := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	out := mapBatched(in, 3, func(i int) int { return i * 2 })
	for i, v := range out {
		if v != i*2 {
			t.Fatalf("mapBatched[%d] = %d, want %d", i, v, i*2)
		}
	}
}

func TestCeilDiv(t *testing.T) {
	cases := [][3]int{{10, 3, 4}, {9, 3, 3}, {1, 5, 1}, {0, 5, 0}}
	for _, c := range cases {
		if got := ceilDiv(c[0], c[1]); got != c[2] {
			t.Errorf("ceilDiv(%d,%d) = %d, want %d", c[0], c[1], got, c[2])
		}
	}
}

func TestHasAnyIsCaseInsensitive(t *testing.T) {
	tags := []plex.Tag{{Tag: "Rock"}, {Tag: "Jazz"}}
	if !hasAny(tags, lowerSet([]string{"ROCK"})) {
		t.Fatal("expected case-insensitive match on Rock")
	}
	if hasAny(tags, lowerSet([]string{"pop"})) {
		t.Fatal("expected no match on Pop")
	}
}
