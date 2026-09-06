package qobuz

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/chromedp/chromedp"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/services/browser"
)

// Source ports scrapeQobuzWithBrowser (browser-scrapers.ts:780-848): Qobuz's
// public playlist pages have no unauthenticated read API either, so this
// follows the same chromedp browser-scrape pattern as apple/tidal/amazon.
type Source struct{}

func NewSource() *Source { return &Source{} }

func (s *Source) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "Qobuz", Icon: "qobuz"}
}

var qobuzPlaylistIDPattern = regexp.MustCompile(`playlist/([0-9]+)`)

type qobuzScrapedPlaylist struct {
	Name   string `json:"name"`
	Tracks []struct {
		Title  string `json:"title"`
		Artist string `json:"artist"`
	} `json:"tracks"`
}

// qobuzScrapeJS is a direct port of the page.evaluate() callback body in
// scrapeQobuzWithBrowser.
const qobuzScrapeJS = `(() => {
  const tracks = [];
  let playlistName = (document.querySelector('h1, .playlist-title, [class*="PlaylistTitle"]') || {}).textContent;
  playlistName = (playlistName || '').trim() || 'Qobuz Playlist';

  document.querySelectorAll('.track-row, [class*="TrackRow"], tr[class*="track"]').forEach(row => {
    const titleEl = row.querySelector('.track-title, [class*="TrackTitle"], td:nth-child(2)');
    const artistEl = row.querySelector('.track-artist, [class*="ArtistName"], td:nth-child(3)');

    if (titleEl) {
      const title = titleEl.textContent.trim();
      const artist = (artistEl && artistEl.textContent.trim()) || 'Unknown';
      if (title && !tracks.some(t => t.title === title && t.artist === artist)) {
        tracks.push({ title, artist });
      }
    }
  });

  return JSON.stringify({ name: playlistName, tracks });
})()`

func (s *Source) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	var raw string
	err := browser.Scrape(ctx, "Qobuz Browser", browser.DefaultTimeout,
		chromedp.Navigate(playlistURLOrID),
		chromedp.WaitVisible(`body`, chromedp.ByQuery),
		chromedp.Evaluate(qobuzScrapeJS, &raw),
	)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	var parsed qobuzScrapedPlaylist
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	if err := browser.AssertTracksScraped(len(parsed.Tracks), "[Qobuz Browser]"); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	tracks := make([]adapters.TrackInfo, 0, len(parsed.Tracks))
	for _, t := range parsed.Tracks {
		tracks = append(tracks, adapters.TrackInfo{Title: t.Title, Artist: t.Artist})
	}

	playlistID := "unknown"
	if m := qobuzPlaylistIDPattern.FindStringSubmatch(playlistURLOrID); m != nil {
		playlistID = m[1]
	}

	return adapters.PlaylistInfo{
		ID:         "qobuz-" + playlistID,
		Name:       parsed.Name,
		TrackCount: len(tracks),
	}, tracks, nil
}
