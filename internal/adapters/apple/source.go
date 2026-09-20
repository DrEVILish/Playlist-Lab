package apple

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/chromedp/chromedp"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/services/browser"
)

// Source ports scrapeAppleMusicWithBrowser (browser-scrapers.ts:114-396):
// Apple Music's web player has no public unauthenticated playlist-read API,
// so - unlike Deezer/ListenBrainz - reading a public playlist page requires
// a real rendered DOM. This is the browser-scraping half of Phase 5; Target
// (MusicKit/OAuth) already existed from Phase 3.
type Source struct{}

func NewSource() *Source { return &Source{} }

func (s *Source) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "Apple Music", Icon: "apple"}
}

var applePlaylistIDPattern = regexp.MustCompile(`playlist/[^/]+/(pl\.[a-zA-Z0-9-]+)`)

// scrapedPlaylist mirrors the shape page.evaluate() returns in the TS
// version - only the fields this port actually uses.
type scrapedPlaylist struct {
	Name   string `json:"name"`
	Tracks []struct {
		Title  string `json:"title"`
		Artist string `json:"artist"`
	} `json:"tracks"`
	CoverURL string `json:"coverUrl"`
}

// scrapeJS is chromedp.Evaluate()'d against the rendered page - a direct
// port of the page.evaluate() callback body in browser-scrapers.ts, minus
// the debug-only image-count/first-image diagnostics (those only ever fed
// logger.debug calls, no behavior).
const scrapeJS = `(() => {
  const tracks = [];
  let playlistName = document.title.replace(' - Apple Music', '').trim() || 'Apple Music Playlist';
  let coverUrl = '';

  const headerEl = document.querySelector('h1, [class*="playlist-name"], [class*="PlaylistHeader"]');
  if (headerEl) playlistName = headerEl.textContent.trim() || playlistName;

  const pickFromSrcset = (srcset) => {
    if (!srcset || !srcset.includes('mzstatic.com')) return '';
    const parts = srcset.split(',').map(s => s.trim());
    const last = parts[parts.length - 1];
    return last.split(' ')[0].replace(/\/\d+x\d+/, '/600x600');
  };

  for (const picture of document.querySelectorAll('picture')) {
    for (const source of picture.querySelectorAll('source')) {
      coverUrl = pickFromSrcset(source.getAttribute('srcset') || '');
      if (coverUrl) break;
    }
    if (coverUrl) break;
  }
  if (!coverUrl) {
    for (const source of document.querySelectorAll('source')) {
      coverUrl = pickFromSrcset(source.getAttribute('srcset') || '');
      if (coverUrl) break;
    }
  }
  if (!coverUrl) {
    for (const img of document.querySelectorAll('img')) {
      coverUrl = pickFromSrcset(img.getAttribute('srcset') || '');
      if (coverUrl) break;
    }
  }
  if (!coverUrl) {
    for (const img of document.querySelectorAll('img')) {
      const src = img.getAttribute('src') || '';
      if (src.includes('mzstatic.com') && !src.includes('1x1.gif') && !src.includes('placeholder')) {
        coverUrl = src.replace(/\/\d+x\d+/, '/600x600');
        break;
      }
    }
  }

  const rowSelectors = [
    '[data-testid="track-row"]', '.songs-list-row', '.song-name-wrapper',
    '[class*="TrackRow"]', '[class*="song-list"] li', '.tracklist-item', 'div[role="row"]',
  ];
  let items = null;
  for (const sel of rowSelectors) {
    const found = document.querySelectorAll(sel);
    if (found.length > 0) { items = found; break; }
  }

  const titleSelectors = ['[data-testid="track-title"]', '.songs-list-row__song-name', '.song-name', '[class*="TrackTitle"]', '[class*="song-name"]', 'a[href*="/song/"]'];
  const artistSelectors = ['[data-testid="track-subtitle"]', '.songs-list-row__by-line', '.song-artist', '[class*="TrackArtist"]', '[class*="artist"]', 'a[href*="/artist/"]'];

  if (items) {
    items.forEach(item => {
      let title = '', artist = '';
      for (const sel of titleSelectors) {
        const el = item.querySelector(sel);
        if (el && el.textContent.trim()) { title = el.textContent.trim(); break; }
      }
      for (const sel of artistSelectors) {
        const el = item.querySelector(sel);
        if (el && el.textContent.trim()) { artist = el.textContent.trim(); break; }
      }
      if (title && artist) tracks.push({ title, artist });
    });
  }

  return JSON.stringify({ name: playlistName, tracks, coverUrl });
})()`

func (s *Source) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	var raw string
	err := browser.Scrape(ctx, "Apple Music Browser", browser.DefaultTimeout,
		chromedp.Navigate(playlistURLOrID),
		chromedp.WaitVisible(`img`, chromedp.ByQuery),
		chromedp.Evaluate(scrapeJS, &raw),
	)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	var parsed scrapedPlaylist
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	if err := browser.AssertTracksScraped(len(parsed.Tracks), "[Apple Music Browser]"); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	tracks := make([]adapters.TrackInfo, 0, len(parsed.Tracks))
	for _, t := range parsed.Tracks {
		tracks = append(tracks, adapters.TrackInfo{Title: t.Title, Artist: t.Artist})
	}

	playlistID := "unknown"
	if m := applePlaylistIDPattern.FindStringSubmatch(playlistURLOrID); m != nil {
		playlistID = m[1]
	}

	return adapters.PlaylistInfo{
		ID:         "apple-" + playlistID,
		Name:       parsed.Name,
		TrackCount: len(tracks),
		CoverURL:   parsed.CoverURL,
	}, tracks, nil
}
