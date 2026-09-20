package aria

import "testing"

// FetchTracks itself needs a real rendered DOM via chromedp - no Chrome is
// available in this sandbox (confirmed in an earlier phase), so the
// browser-scrape path and its fallback-chain error message can't be
// exercised here without faking chromedp, which the task explicitly says
// not to do. chartIDFromURL is the one pure-logic seam pulled out of that
// path (URL -> chart ID derivation), so that's what's covered.
func TestChartIDFromURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"simple path", "https://www.aria.com.au/charts/singles-chart", "charts-singles-chart"},
		{"trailing slash trimmed", "https://www.aria.com.au/charts/singles-chart/", "charts-singles-chart"},
		{"query string stripped", "https://www.aria.com.au/charts/singles-chart?week=1", "charts-singles-chart"},
		{"root path", "https://www.aria.com.au/", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chartIDFromURL(tt.url); got != tt.want {
				t.Errorf("chartIDFromURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}
