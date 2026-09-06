package importsvc

import (
	"database/sql"
	"testing"

	"github.com/drevilish/playlist-lab/internal/db"
)

// toRememberedMatches has to unwrap db.ManualMatch's sql.NullString Album
// into RememberedMatch's plain string - a NULL album (never manually
// re-matched with one) must become "" rather than panicking or leaking the
// sql.NullString's zero value, since RememberedMatchKey directly
// string-joins the field.
func TestToRememberedMatches(t *testing.T) {
	cases := []struct {
		name string
		in   db.ManualMatch
		want string
	}{
		{"valid album", db.ManualMatch{Title: "T", Artist: "A", Album: sql.NullString{String: "B", Valid: true}, PlexRatingKey: "k1"}, "B"},
		{"null album", db.ManualMatch{Title: "T", Artist: "A", Album: sql.NullString{Valid: false}, PlexRatingKey: "k2"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := toRememberedMatches([]db.ManualMatch{c.in})
			if len(out) != 1 {
				t.Fatalf("expected 1 result, got %d", len(out))
			}
			if out[0].Album != c.want {
				t.Errorf("Album = %q, want %q", out[0].Album, c.want)
			}
			if out[0].Title != c.in.Title || out[0].PlexRatingKey != c.in.PlexRatingKey {
				t.Errorf("fields not preserved: got %+v", out[0])
			}
		})
	}
}
