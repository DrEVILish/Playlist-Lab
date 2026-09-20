// Package letterboxd reads a public Letterboxd list's items for
// Collections' external list/chart builder (DESIGN.md §11.11). Letterboxd
// has no public API and no id Plex understands natively, so this is a
// two-hop scrape (confirmed live against a real public Letterboxd list and
// a real film page, via the app's existing headless-browser scraper,
// internal/services/browser):
//  1. the list page's poster grid links to each film's own page
//     ("/film/<slug>/") - confirmed live these are plain <a href> links, no
//     data-film-slug attribute (older markup some scrapers assume no
//     longer exists).
//  2. each film's own page embeds a direct link to its TMDb entry
//     (confirmed live: <a href="https://www.themoviedb.org/movie/550/">
//     on Fight Club's page) - that TMDb id is what's actually matched
//     against a Plex library's Guid data, via the same "tmdb://" key
//     tmdb.Client's own items use.
//
// ponytail: this is N+1 page loads (the list page, then one per film), all
// sequential against the app's single shared headless-Chrome instance - a
// 100-item list is on the order of minutes per refresh, not seconds. Bound
// by Limit (small by default) rather than fetched concurrently, since
// browser.Scrape's shared allocator isn't designed for many parallel tabs
// hammering one target site. Upgrade path if this matters: cache each
// film-page resolution by slug (a film's TMDb id never changes) so a
// second collection referencing the same film doesn't re-scrape it.
package letterboxd

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/drevilish/playlist-lab/internal/services/browser"
	"github.com/drevilish/playlist-lab/internal/services/medialist"
)

var (
	filmLinkPattern = regexp.MustCompile(`/film/([a-z0-9-]+)/`)
	tmdbLinkPattern = regexp.MustCompile(`themoviedb\.org/movie/(\d+)`)
)

const listSlugsJS = `(() => {
	const hrefs = Array.from(document.querySelectorAll('a[href*="/film/"]')).map(a => a.getAttribute('href'));
	return JSON.stringify([...new Set(hrefs)]);
})()`

const filmTMDbLinkJS = `(() => {
	const a = document.querySelector('a[href*="themoviedb.org/movie/"]');
	return a ? a.getAttribute('href') : '';
})()`

// GetListItems fetches a public Letterboxd list's items, resolving each
// film to its TMDb id. listID accepts either a full list URL or the
// "<user>/list/<slug>" path, matching how a user would actually copy it
// out of their browser. limit bounds how many films get resolved (see
// package doc on cost) - defaults to 20 when <= 0, same default as TMDb's
// chart builder.
func GetListItems(ctx context.Context, listURL string, limit int) ([]medialist.Item, error) {
	path := extractListPath(listURL)
	if path == "" {
		return nil, fmt.Errorf("invalid Letterboxd list URL %q", listURL)
	}
	if limit <= 0 {
		limit = 20
	}

	// Letterboxd paginates a list at /page/2/, /page/3/, ... - a plain
	// single-page scrape silently truncated to whatever happened to be on
	// page 1 (confirmed live: a large list returned only 14 films that
	// way). Walk pages until enough slugs are gathered or a page comes back
	// empty (past the end); a later page failing to load (e.g. Letterboxd
	// 404s past the real last page) just stops pagination rather than
	// failing the whole request, since page 1 alone already produced usable
	// results by that point.
	var slugs []string
	for page := 1; len(slugs) < limit && page <= 10; page++ {
		pageURL := "https://letterboxd.com/" + path + "/"
		if page > 1 {
			pageURL = "https://letterboxd.com/" + path + "/page/" + strconv.Itoa(page) + "/"
		}
		var slugsJSON string
		err := browser.Scrape(ctx, "Letterboxd list", browser.DefaultTimeout,
			browser.AcceptLanguage("en-US,en;q=0.9"),
			chromedp.Navigate(pageURL),
			chromedp.Sleep(2*time.Second),
			chromedp.Evaluate(listSlugsJS, &slugsJSON),
		)
		if err != nil {
			if page == 1 {
				return nil, fmt.Errorf("Letterboxd list scrape failed: %w", err)
			}
			break
		}
		pageSlugs := parseFilmSlugs(slugsJSON)
		if len(pageSlugs) == 0 {
			break
		}
		slugs = append(slugs, pageSlugs...)
	}
	if len(slugs) == 0 {
		return nil, fmt.Errorf("Letterboxd list %s has no films, or the page structure changed", path)
	}
	if len(slugs) > limit {
		slugs = slugs[:limit]
	}

	out := make([]medialist.Item, 0, len(slugs))
	for _, slug := range slugs {
		var href string
		err := browser.Scrape(ctx, "Letterboxd film", browser.DefaultTimeout,
			chromedp.Navigate("https://letterboxd.com/film/"+slug+"/"),
			chromedp.Sleep(1*time.Second),
			chromedp.Evaluate(filmTMDbLinkJS, &href),
		)
		if err != nil || href == "" {
			continue // no TMDb link found for this film - skip rather than fail the whole list
		}
		m := tmdbLinkPattern.FindStringSubmatch(href)
		if m == nil {
			continue
		}
		out = append(out, medialist.Item{GuidKey: "tmdb://" + m[1], MediaType: "movie", Title: slug})
	}
	return out, nil
}

// parseFilmSlugs turns listSlugsJS's JSON array of "/film/<slug>/" hrefs
// into bare slugs, deduplicated (by the JS side) and order-preserved.
func parseFilmSlugs(hrefsJSON string) []string {
	var hrefs []string
	if err := json.Unmarshal([]byte(hrefsJSON), &hrefs); err != nil {
		return nil
	}
	var slugs []string
	for _, href := range hrefs {
		if m := filmLinkPattern.FindStringSubmatch(href); m != nil {
			slugs = append(slugs, m[1])
		}
	}
	return slugs
}

// extractListPath pulls "<user>/list/<slug>" off a full Letterboxd list
// URL, or passes through a bare path.
func extractListPath(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "https://")
	v = strings.TrimPrefix(v, "http://")
	v = strings.TrimPrefix(v, "letterboxd.com/")
	v = strings.Trim(v, "/")
	if !strings.Contains(v, "/list/") {
		return ""
	}
	return v
}
