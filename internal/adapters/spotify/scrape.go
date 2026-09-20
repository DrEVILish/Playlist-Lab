package spotify

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/services/browser"
)

// fetchTracksByScraping ports fetchTracksViaScraping (spotify-source.ts):
// the fallback FetchTracks below falls to when neither the user's own
// OAuth token nor an app Client Credentials token can read a playlist -
// which used to be a rare case (a private-ish playlist) but, since
// Spotify's Nov 2024 API changes 403 client-credentials tokens on most
// playlist reads for apps not in Extended Quota Mode, is now the common
// case for importing someone else's public playlist by URL. Dropping this
// fallback when the Go rewrite ported only the API happy path is why
// Spotify import regressed from "just works" in the Node version to
// erroring on almost every real playlist.
//
// Spotify's web player virtual-scrolls its track list (only ~20-30 rows
// ever exist in the DOM at once), so a single chromedp.Evaluate can't see
// the whole playlist. v2 scrolled until 8 consecutive scrolls found
// nothing new; that early-exit needs to inspect each step's result before
// deciding on the next, but every browser.Scrape call opens a fresh tab
// (fine for every other adapter's single-shot scrape, not for a
// stateful multi-step one), so the running Map it collected into would
// be lost between calls. A fixed 40 scroll+collect passes in one
// browser.Scrape call keeps everything in the same tab at the cost of a
// few wasted iterations on a short playlist - simpler than threading a
// stop condition through chromedp's action list, and 40*600ms is well
// inside the 90s scrape timeout either way.
func fetchTracksByScraping(ctx context.Context, playlistID string) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	name := oembedTitle(playlistID)

	actions := []chromedp.Action{
		chromedp.Navigate("https://open.spotify.com/playlist/" + playlistID),
		chromedp.WaitVisible(`a[href*="/track/"]`, chromedp.ByQuery),
		chromedp.Sleep(2 * time.Second),
		chromedp.Evaluate(`window.__plTracks = new Map()`, nil),
	}
	// Scroll to the bottom repeatedly first to trigger lazy loading, then
	// back to the top, matching v2's two-phase approach.
	for i := 0; i < 10; i++ {
		actions = append(actions,
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
			chromedp.Sleep(1*time.Second))
	}
	actions = append(actions,
		chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
		chromedp.Sleep(1*time.Second))
	for i := 0; i < 40; i++ {
		actions = append(actions,
			chromedp.Evaluate(collectTracksJS, nil),
			chromedp.Evaluate(`window.scrollBy(0, 600)`, nil),
			chromedp.Sleep(600*time.Millisecond))
	}

	var raw string
	actions = append(actions, chromedp.Evaluate(`JSON.stringify(Array.from(window.__plTracks.values()))`, &raw))

	if err := browser.Scrape(ctx, "Spotify Browser", 3*time.Minute, actions...); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	var tracks []adapters.TrackInfo
	if err := json.Unmarshal([]byte(raw), &tracks); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	if err := browser.AssertTracksScraped(len(tracks), "[Spotify Browser]"); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	return adapters.PlaylistInfo{ID: "spotify-" + playlistID, Name: name, TrackCount: len(tracks)}, tracks, nil
}

// collectTracksJS merges newly-visible rows into window.__plTracks (set up
// before scrolling starts). Ported from fetchTracksViaScraping's per-scroll
// extraction callback.
const collectTracksJS = `(() => {
  document.querySelectorAll('a[href*="/track/"]').forEach(link => {
    const href = link.getAttribute('href') || '';
    const m = href.match(/\/track\/([A-Za-z0-9]+)/);
    if (!m) return;
    const id = m[1];
    if (window.__plTracks.has(id)) return;
    const row = link.closest('[data-testid="tracklist-row"]') || link.closest('[role="row"]') || link.parentElement;
    if (!row) return;
    const title = (link.textContent || '').trim();
    const artist = Array.from(row.querySelectorAll('a[href*="/artist/"]'))
      .map(a => (a.textContent || '').trim()).filter(Boolean).join(', ');
    if (title && artist) window.__plTracks.set(id, { title, artist, album: '' });
  });
})()`

// oembedTitle gets the playlist's display name from Spotify's public,
// unauthenticated oEmbed endpoint - no token needed, so it's tried even
// when every actual API credential path has failed. Falls back to a
// generic name rather than failing the whole import over a missing title.
func oembedTitle(playlistID string) string {
	resp, err := http.Get("https://open.spotify.com/oembed?url=https://open.spotify.com/playlist/" + playlistID)
	if err != nil {
		return "Spotify Playlist"
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "Spotify Playlist"
	}
	var data struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || data.Title == "" {
		return "Spotify Playlist"
	}
	return data.Title
}
