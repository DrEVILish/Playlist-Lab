package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/services/browser"
)

// fetchTracksByScraping walks Spotify's virtualized list in one tab.
func fetchTracksByScraping(ctx context.Context, playlistID string) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	pageURL := "https://open.spotify.com/playlist/" + url.PathEscape(playlistID)
	var raw string
	err := browser.Scrape(ctx, "Spotify Browser", 3*time.Minute, pageURL, []string{"spotify.com", "scdn.co", "spotifycdn.com"},
		chromedp.WaitVisible(`a[href*="/track/"]`, chromedp.ByQuery),
		chromedp.Evaluate(`window.__plTracks = new Map()`, nil),
		chromedp.ActionFunc(collectPlaylist),
		chromedp.Evaluate(`JSON.stringify(Array.from(window.__plTracks.values()))`, &raw),
	)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	var tracks []adapters.TrackInfo
	if err := json.Unmarshal([]byte(raw), &tracks); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	if err := browser.AssertTracksScraped(len(tracks), "[Spotify Browser]"); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	return adapters.PlaylistInfo{ID: "spotify-" + playlistID, Name: oembedTitle(ctx, pageURL), TrackCount: len(tracks)}, tracks, nil
}

func collectPlaylist(ctx context.Context) error {
	type scrollState struct {
		Count int
		AtEnd bool
	}
	var prev scrollState
	stable := 0
	for stable < 4 {
		var current scrollState
		if err := chromedp.Evaluate(collectTracksJS, &current).Do(ctx); err != nil {
			return err
		}
		if current.AtEnd && current == prev {
			stable++
		} else {
			stable = 0
		}
		prev = current
		if err := chromedp.Sleep(250 * time.Millisecond).Do(ctx); err != nil {
			return fmt.Errorf("Spotify playlist did not finish loading: %w", err)
		}
	}
	return nil
}

const collectTracksJS = `(() => {
  document.querySelectorAll('a[href*="/track/"]').forEach(link => {
    const href = link.getAttribute('href') || '';
    const m = href.match(/\/track\/([A-Za-z0-9]+)/);
    if (!m) return;
    const row = link.closest('[data-testid="tracklist-row"]') || link.closest('[role="row"]') || link.parentElement;
    if (!row) return;
    const id = row.getAttribute('aria-rowindex') || m[1];
    if (window.__plTracks.has(id)) return;
    const title = (link.textContent || '').trim();
    const artist = Array.from(row.querySelectorAll('a[href*="/artist/"]'))
      .map(a => (a.textContent || '').trim()).filter(Boolean).join(', ');
    if (title && artist) window.__plTracks.set(id, { title, artist, album: '' });
  });
  const track = document.querySelector('a[href*="/track/"]');
  let scroller = track?.parentElement;
  while (scroller && (scroller.scrollHeight <= scroller.clientHeight || !/auto|scroll/.test(getComputedStyle(scroller).overflowY))) {
    scroller = scroller.parentElement;
  }
  scroller ||= document.scrollingElement;
  const atEnd = scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 2;
  if (!atEnd) scroller.scrollBy(0, Math.max(100, Math.floor(scroller.clientHeight * .75)));
  return {Count: window.__plTracks.size, AtEnd: atEnd};
})()`

// oembedTitle gets the playlist's display name from Spotify's public,
// unauthenticated oEmbed endpoint - no token needed, so it's tried even
// when every actual API credential path has failed. Falls back to a
// generic name rather than failing the whole import over a missing title.
func oembedTitle(ctx context.Context, pageURL string) string {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://open.spotify.com/oembed?url="+url.QueryEscape(pageURL), nil)
	if err != nil {
		return "Spotify Playlist"
	}
	resp, err := http.DefaultClient.Do(req)
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&data); err != nil || data.Title == "" {
		return "Spotify Playlist"
	}
	return data.Title
}
