// Package aria ports scrapeAriaChartWithBrowser (browser-scrapers.ts:853):
// ARIA's chart pages have no public JSON API, so reading one needs a real
// rendered DOM, same as internal/adapters/apple. Backs the "aria"
// chart-URL import (import.ts's handleImport 'aria' case) - not the
// country-chart browsing charts.ts handles for deezer/apple/spotify (ARIA
// has none there; scrapers.AriaCharts is a deliberate no-op for that path).
//
// ponytail: the Node original also intercepts the page's own XHR/fetch
// responses looking for chart JSON before falling back to DOM scraping -
// chromedp's network-event API makes that meaningfully more code for a
// scrape that already has a working DOM fallback, so only the DOM path is
// ported here. If ARIA's DOM structure changes and JSON interception turns
// out to be the more stable path, that's the upgrade.
package aria

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/services/browser"
)

const serviceName = "aria"

type Source struct{}

func NewSource() *Source { return &Source{} }

func (s *Source) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "ARIA Charts", Icon: "aria"}
}

type scrapedResult struct {
	Name   string `json:"name"`
	Tracks []struct {
		Title  string `json:"title"`
		Artist string `json:"artist"`
	} `json:"tracks"`
}

// scrapeJS is chromedp.Evaluate()'d against the rendered page - a direct
// port of the DOM-scraping fallback branch of scrapeAriaChartWithBrowser
// (browser-scrapers.ts:934-1035): find elements whose text is exactly a
// chart position 1-100, walk up to the row containing "last week"/"peak"/
// "weeks in" stats, then split the row's remaining text lines into
// title/artist.
const scrapeJS = `(() => {
  const chartName = document.querySelector('h1')?.textContent?.trim() || 'ARIA Chart';
  const allElements = Array.from(document.querySelectorAll('*'));
  const chartRows = [];
  for (const el of allElements) {
    const text = el.textContent?.trim() || '';
    if (/^\d+$/.test(text)) {
      const num = parseInt(text, 10);
      if (num >= 1 && num <= 100) {
        const children = el.children;
        if (children.length === 0 || (children.length === 1 && (children[0].textContent?.trim() || '') === text)) {
          let row = el.parentElement;
          for (let depth = 0; depth < 6 && row; depth++) {
            const rowText = row.innerText || row.textContent || '';
            if (/last week|peak|weeks? in/i.test(rowText)) {
              const alreadyCaptured = chartRows.some(r => r.element === row || r.element.contains(row) || row.contains(r.element));
              if (!alreadyCaptured) chartRows.push({ position: num, element: row });
              break;
            }
            row = row.parentElement;
          }
        }
      }
    }
  }
  chartRows.sort((a, b) => a.position - b.position);
  const seen = new Set();
  const uniqueRows = chartRows.filter(r => {
    if (seen.has(r.position)) return false;
    seen.add(r.position);
    return true;
  });
  const tracks = [];
  for (const { element } of uniqueRows) {
    const rowText = element.innerText || element.textContent || '';
    const lines = rowText.split('\n').map(l => l.trim()).filter(l => l.length > 0);
    const meaningful = lines.filter(l => {
      if (/^\d+$/.test(l)) return false;
      if (/^\d+\s*(last week|peak|weeks? in)/i.test(l)) return false;
      if (/^(last week|peak|weeks? in|new)/i.test(l)) return false;
      if (/^-$/i.test(l)) return false;
      if (l.length < 2 || l.length > 150) return false;
      return true;
    });
    if (meaningful.length >= 2) {
      const title = meaningful[0];
      let artist = meaningful[1].split(/\s*[,&]\s*/)[0].replace(/\s*feat\.?\s*.*/i, '').trim();
      if (title && artist) tracks.push({ title, artist });
    } else if (meaningful.length === 1) {
      const parts = meaningful[0].split(/\s*[-–—]\s*/);
      if (parts.length >= 2) tracks.push({ title: parts[0].trim(), artist: parts[1].trim() });
    }
  }
  return JSON.stringify({ name: chartName, tracks });
})()`

func (s *Source) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	var raw string
	err := browser.Scrape(ctx, "ARIA Browser", 30*time.Second,
		chromedp.Navigate(playlistURLOrID),
		chromedp.Sleep(5*time.Second),
		chromedp.Evaluate(scrapeJS, &raw),
	)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("failed to scrape ARIA chart: %w", err)
	}

	var result scrapedResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("failed to parse ARIA chart page: %w", err)
	}
	if err := browser.AssertTracksScraped(len(result.Tracks), "[ARIA Browser]"); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	tracks := make([]adapters.TrackInfo, len(result.Tracks))
	for i, t := range result.Tracks {
		tracks[i] = adapters.TrackInfo{Title: t.Title, Artist: t.Artist}
	}

	chartID := chartIDFromURL(playlistURLOrID)
	playlist := adapters.PlaylistInfo{ID: "aria-" + chartID, Name: result.Name, TrackCount: len(tracks)}
	return playlist, tracks, nil
}

var pathTrimPattern = regexp.MustCompile(`^-+|-+$`)

// chartIDFromURL ports the `new URL(url).pathname.replace(/\//g,'-')...`
// one-liner in scrapeAriaChartWithBrowser.
func chartIDFromURL(rawURL string) string {
	path := rawURL
	if idx := strings.Index(rawURL, "://"); idx >= 0 {
		rest := rawURL[idx+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			path = rest[slash:]
		} else {
			path = "/"
		}
	}
	if q := strings.IndexByte(path, '?'); q >= 0 {
		path = path[:q]
	}
	id := strings.ReplaceAll(path, "/", "-")
	return pathTrimPattern.ReplaceAllString(id, "")
}
