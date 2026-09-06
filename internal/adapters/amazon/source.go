package amazon

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/chromedp/chromedp"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/services/browser"
)

// Source ports scrapeAmazonMusicWithBrowser (browser-scrapers.ts:707-775):
// Amazon Music has no public read API either (mirroring the CreatePlaylist
// stub above being write-only-impossible), so reading a public playlist
// page requires a real rendered DOM, following the same chromedp pattern
// as apple.Source/tidal.Source.
type Source struct{}

func NewSource() *Source { return &Source{} }

func (s *Source) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: "amazon", Name: "Amazon Music", Icon: "amazon"}
}

var amazonPlaylistIDPattern = regexp.MustCompile(`playlists/([A-Z0-9]+)`)

type amazonScrapedPlaylist struct {
	Name   string `json:"name"`
	Tracks []struct {
		Title  string `json:"title"`
		Artist string `json:"artist"`
	} `json:"tracks"`
}

// amazonScrapeJS is a direct port of the page.evaluate() callback body in
// scrapeAmazonMusicWithBrowser.
const amazonScrapeJS = `(() => {
  const tracks = [];
  let playlistName = (document.querySelector('h1, [data-testid="playlistHeaderTitle"], .playlistHeaderTitle') || {}).textContent;
  playlistName = (playlistName || '').trim() || 'Amazon Music Playlist';

  document.querySelectorAll('[data-testid="tracklist-row"], .trackListRow, tr[class*="track"]').forEach(row => {
    const titleEl = row.querySelector('[data-testid="track-title"], .trackTitle, td:nth-child(2)');
    const artistEl = row.querySelector('[data-testid="track-artist"], .trackArtist, td:nth-child(3)');

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
	err := browser.Scrape(ctx, "Amazon Music Browser", browser.DefaultTimeout,
		chromedp.Navigate(playlistURLOrID),
		chromedp.WaitVisible(`body`, chromedp.ByQuery),
		chromedp.Evaluate(amazonScrapeJS, &raw),
	)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	var parsed amazonScrapedPlaylist
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	if err := browser.AssertTracksScraped(len(parsed.Tracks), "[Amazon Music Browser]"); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	tracks := make([]adapters.TrackInfo, 0, len(parsed.Tracks))
	for _, t := range parsed.Tracks {
		tracks = append(tracks, adapters.TrackInfo{Title: t.Title, Artist: t.Artist})
	}

	playlistID := "unknown"
	if m := amazonPlaylistIDPattern.FindStringSubmatch(playlistURLOrID); m != nil {
		playlistID = m[1]
	}

	return adapters.PlaylistInfo{
		ID:         "amazon-" + playlistID,
		Name:       parsed.Name,
		TrackCount: len(tracks),
	}, tracks, nil
}
