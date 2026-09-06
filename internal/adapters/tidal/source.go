package tidal

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/chromedp/chromedp"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/services/browser"
)

// Source ports scrapeTidalWithBrowser (browser-scrapers.ts:609-702): Tidal's
// web player playlist pages have no public unauthenticated read API, so
// this follows the same chromedp browser-scrape pattern as apple.Source.
type Source struct{}

func NewSource() *Source { return &Source{} }

func (s *Source) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "Tidal", Icon: "tidal"}
}

var tidalPlaylistIDPattern = regexp.MustCompile(`playlist/([a-f0-9-]+)`)

type tidalScrapedPlaylist struct {
	Name   string `json:"name"`
	Tracks []struct {
		Title  string `json:"title"`
		Artist string `json:"artist"`
	} `json:"tracks"`
}

// tidalScrapeJS is a direct port of the page.evaluate() callback body in
// scrapeTidalWithBrowser.
const tidalScrapeJS = `(() => {
  const tracks = [];
  let playlistName = '';

  const titleEl = document.querySelector('h1, [data-test="playlist-title"], [class*="PlaylistTitle"]');
  if (titleEl) playlistName = titleEl.textContent.trim() || '';
  if (!playlistName) {
    playlistName = document.title.replace(' - TIDAL', '').replace(' | TIDAL', '').replace(' - Playlist by', '').trim();
  }

  document.querySelectorAll('[data-test="tracklist-row"], [class*="TrackRow"], [class*="track-row"]').forEach(row => {
    const titleEl2 = row.querySelector('[data-test="table-cell-title"] a, [class*="TrackTitle"] a, a[href*="/track/"]');
    const artistEl = row.querySelector('[data-test="table-cell-artist"] a, [class*="ArtistName"] a, a[href*="/artist/"]');
    if (titleEl2 && artistEl) {
      const title = titleEl2.textContent.trim();
      const artist = artistEl.textContent.trim();
      if (title && artist && !tracks.some(t => t.title === title && t.artist === artist)) {
        tracks.push({ title, artist });
      }
    }
  });

  if (tracks.length === 0) {
    document.querySelectorAll('a[href*="/track/"]').forEach(link => {
      const title = link.textContent.trim();
      if (!title || title.length > 100) return;

      const container = link.closest('div, li, tr, article');
      if (container) {
        const artistLink = container.querySelector('a[href*="/artist/"]');
        if (artistLink) {
          const artist = artistLink.textContent.trim();
          if (artist && !tracks.some(t => t.title === title && t.artist === artist)) {
            tracks.push({ title, artist });
          }
        }
      }
    });
  }

  return JSON.stringify({ name: playlistName || 'Tidal Playlist', tracks });
})()`

func (s *Source) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	var raw string
	err := browser.Scrape(ctx, "Tidal Browser", browser.DefaultTimeout,
		chromedp.Navigate(playlistURLOrID),
		chromedp.WaitVisible(`body`, chromedp.ByQuery),
		chromedp.Evaluate(tidalScrapeJS, &raw),
	)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	var parsed tidalScrapedPlaylist
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	if err := browser.AssertTracksScraped(len(parsed.Tracks), "[Tidal Browser]"); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	tracks := make([]adapters.TrackInfo, 0, len(parsed.Tracks))
	for _, t := range parsed.Tracks {
		tracks = append(tracks, adapters.TrackInfo{Title: t.Title, Artist: t.Artist})
	}

	playlistID := "unknown"
	if m := tidalPlaylistIDPattern.FindStringSubmatch(playlistURLOrID); m != nil {
		playlistID = m[1]
	}

	return adapters.PlaylistInfo{
		ID:         "tidal-" + playlistID,
		Name:       parsed.Name,
		TrackCount: len(tracks),
	}, tracks, nil
}
