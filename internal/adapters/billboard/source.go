// Package billboard ports scrapeBillboardChart (browser-scrapers.ts:1083,
// despite the file it lives in - it's plain HTTP, not a browser scrape:
// Billboard.com itself blocks automation, so the original reads a
// community-maintained GitHub mirror of the Hot 100 instead). Backs the
// "billboard" chart-URL import (import.ts's handleImport 'billboard' case).
package billboard

import (
	"encoding/json"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/drevilish/playlist-lab/internal/adapters"
)

const serviceName = "billboard"

var httpClient = &http.Client{Timeout: 15 * time.Second}

type Source struct{}

func NewSource() *Source { return &Source{} }

func (s *Source) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "Billboard", Icon: "billboard"}
}

var (
	chartTypePattern = regexp.MustCompile(`/charts/([^/]+)`)
	datePattern      = regexp.MustCompile(`/(\d{4}-\d{2}-\d{2})/?$`)
)

// FetchTracks ports scrapeBillboardChart: only the Hot 100 chart is
// available (that's all the GitHub data source publishes), optionally for a
// specific Saturday date embedded in the URL.
func (s *Source) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	chartMatch := chartTypePattern.FindStringSubmatch(playlistURLOrID)
	if chartMatch == nil {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("invalid Billboard chart URL")
	}
	if chartMatch[1] != "hot-100" {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf(
			"Billboard %s chart is not currently supported. Only the Hot 100 chart is available at this time.", chartMatch[1])
	}

	dataURL := "https://raw.githubusercontent.com/mhollingshead/billboard-hot-100/main/recent.json"
	if dateMatch := datePattern.FindStringSubmatch(playlistURLOrID); dateMatch != nil {
		dataURL = "https://raw.githubusercontent.com/mhollingshead/billboard-hot-100/main/date/" + dateMatch[1] + ".json"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dataURL, nil)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	resp, err := httpClient.Do(req)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("failed to connect to Billboard data source: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf(
			"Billboard chart not found for the specified date. Charts are dated for Saturdays and may not be available for the current week yet.")
	}
	if resp.StatusCode != http.StatusOK {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("unexpected status %d from Billboard data source", resp.StatusCode)
	}

	var chart struct {
		Date string `json:"date"`
		Data []struct {
			Song   string `json:"song"`
			Artist string `json:"artist"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chart); err != nil || chart.Data == nil {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("invalid response from Billboard data source")
	}

	tracks := make([]adapters.TrackInfo, 0, len(chart.Data))
	for _, e := range chart.Data {
		if e.Song != "" && e.Artist != "" {
			tracks = append(tracks, adapters.TrackInfo{Title: e.Song, Artist: e.Artist})
		}
	}

	playlist := adapters.PlaylistInfo{
		ID:         "billboard-hot-100-" + chart.Date,
		Name:       "Billboard Hot 100 - " + chart.Date,
		TrackCount: len(tracks),
	}
	return playlist, tracks, nil
}
