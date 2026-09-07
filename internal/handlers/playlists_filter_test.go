package handlers

import "testing"

// TestCleanPlaylistName covers cleanPlaylistName, which ports routes/
// playlists.ts's inline "clean up duplicate prefixes" cleanup
// (parts[0] === parts[1].split(' ')[0]) - not playlist-utilities.test.ts
// (that TS file covers the sort/dedupe routes, which have no Go equivalent
// yet; see the port report). This was untested on both sides, so the cases
// below are derived directly from the ported one-liner rather than from an
// existing test: the prefix must equal the remainder's first *word*, not
// just be a prefix of it, so e.g. "All out - All out 60s" does NOT clean up
// (the whole "All out" is not equal to the single word "All").
func TestCleanPlaylistName(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"single-word prefix duplicated as the first word after the separator", "Chill - Chill Vibes", "Chill Vibes"},
		{"multi-word prefix is not equal to just the first word, so it is left alone", "All out - All out 60s", "All out - All out 60s"},
		{"no separator at all", "Deep Focus", "Deep Focus"},
		{"separator present but prefix does not repeat", "Chill - Vibes Only", "Chill - Vibes Only"},
		{"more than one separator is left alone, matching Node's exactly-one-split requirement", "A - A - B", "A - A - B"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanPlaylistName(tt.in); got != tt.want {
				t.Errorf("cleanPlaylistName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFilterRows(t *testing.T) {
	rows := []playlistRow{
		{Name: "Christmas: Pop", Source: "spotify", MissingCount: 3, ScheduleID: 1},
		{Name: "Deep Focus", Source: "spotify", MissingCount: 0, ScheduleID: 0},
		{Name: "80s Hits", Source: "deezer", MissingCount: 7, ScheduleID: 0},
		{Name: "All Music", Source: "plex", MissingCount: 0, ScheduleID: 2},
	}

	tests := []struct {
		name   string
		filter rowFilter
		want   []string
	}{
		{"no filter returns everything", rowFilter{},
			[]string{"Christmas: Pop", "Deep Focus", "80s Hits", "All Music"}},
		{"search is case-insensitive substring", rowFilter{Search: "chRIStmas"},
			[]string{"Christmas: Pop"}},
		{"source is exact", rowFilter{Source: "spotify"},
			[]string{"Christmas: Pop", "Deep Focus"}},
		{"missing=has", rowFilter{Missing: "has"},
			[]string{"Christmas: Pop", "80s Hits"}},
		{"missing=none", rowFilter{Missing: "none"},
			[]string{"Deep Focus", "All Music"}},
		{"schedule=on", rowFilter{Schedule: "on"},
			[]string{"Christmas: Pop", "All Music"}},
		{"schedule=off", rowFilter{Schedule: "off"},
			[]string{"Deep Focus", "80s Hits"}},
		{"filters combine (AND)", rowFilter{Source: "spotify", Missing: "has"},
			[]string{"Christmas: Pop"}},
		{"no matches", rowFilter{Search: "nothing here"}, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterRows(rows, tc.filter)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rows, want %d (%v)", len(got), len(tc.want), tc.want)
			}
			for i, name := range tc.want {
				if got[i].Name != name {
					t.Errorf("row %d = %q, want %q", i, got[i].Name, name)
				}
			}
		})
	}

	// filterRows must not clobber the caller's backing array - it shares the
	// input slice's memory, so appending into it in place would overwrite
	// rows still being iterated.
	if rows[1].Name != "Deep Focus" {
		t.Errorf("filterRows mutated the input slice: rows[1] = %q", rows[1].Name)
	}
}

func TestDistinctSources(t *testing.T) {
	got := distinctSources([]playlistRow{
		{Source: "spotify"}, {Source: "plex"}, {Source: "spotify"}, {Source: ""},
	})
	want := []string{"plex", "spotify"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
