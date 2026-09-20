package scrapers

import "testing"

// TestIsChartTitleMatch covers getDeezerCharts's country-chart-playlist
// predicate: getting this wrong means DailyScraper silently caches the
// wrong country's chart (or none at all) under a country's cache key.
func TestIsChartTitleMatch(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		country   string
		wantMatch bool
	}{
		{"exact case match", "Top 50 Australia", "Australia", true},
		{"case insensitive", "TOP 50 AUSTRALIA", "Australia", true},
		{"missing top keyword", "Australia Hits", "Australia", false},
		{"missing country", "Top 50 Global", "Australia", false},
		{"country substring elsewhere in title", "Top Australia Dance Anthems", "Australia", true},
		{"empty title", "", "Australia", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isChartTitleMatch(tt.title, tt.country); got != tt.wantMatch {
				t.Fatalf("isChartTitleMatch(%q, %q) = %v, want %v", tt.title, tt.country, got, tt.wantMatch)
			}
		})
	}
}
