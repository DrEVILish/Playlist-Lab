package handlers

import "testing"

// TestSortRows_AllColumnsReorder guards every column the Playlists table's
// headers claim to be sortable (see templates/home.html's th.sortable
// cells) - "name", "schedule", "nextRun", and "lastRun" had no <a
// href="SortHref"> wired to their headers at all, so their sort was
// unreachable from the UI despite sortRows itself supporting most of them;
// this only catches a regression in sortRows, not the template wiring.
func TestSortRows_AllColumnsReorder(t *testing.T) {
	rows := []playlistRow{
		{Name: "Zebra", Source: "spotify", TrackCount: 5, Duration: 500, MissingCount: 2, ScheduleID: 1, Frequency: "weekly", NextRunAt: 200, LastRun: 20, CreatedAt: 2},
		{Name: "Alpha", Source: "plex", TrackCount: 10, Duration: 100, MissingCount: 1, ScheduleID: 2, Frequency: "daily", NextRunAt: 100, LastRun: 10, CreatedAt: 1},
	}
	cases := []struct {
		key          string
		wantFirstAsc string // the winning row's Source for "source", its Name otherwise
	}{
		{"name", "Alpha"},
		{"source", "plex"},
		{"tracks", "Zebra"}, // TrackCount 5 < 10
		{"duration", "Alpha"},
		{"missing", "Alpha"},
		{"schedule", "Zebra"}, // ScheduleID 1 < 2
		{"nextRun", "Alpha"},
		{"lastRun", "Alpha"},
		{"dateAdded", "Alpha"},
	}
	for _, c := range cases {
		got := append([]playlistRow(nil), rows...)
		sortRows(got, c.key, "asc")
		first := got[0].Name
		if c.key == "source" {
			first = got[0].Source
		}
		if first != c.wantFirstAsc {
			t.Errorf("sort by %q asc: first row = %q, want %q", c.key, first, c.wantFirstAsc)
		}
	}
}

// TestSortRows_EmptyKeyLeavesOrderAlone documents the deliberate no-op for
// "" (no sort applied yet / unrecognized key), so it isn't accidentally
// turned into a crash or a silent full-table reorder later.
func TestSortRows_EmptyKeyLeavesOrderAlone(t *testing.T) {
	rows := []playlistRow{{Name: "Zebra"}, {Name: "Alpha"}}
	sortRows(rows, "", "asc")
	if rows[0].Name != "Zebra" {
		t.Fatalf("empty sort key reordered rows: got %q first", rows[0].Name)
	}
}
