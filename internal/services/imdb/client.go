// Package imdb reads a public IMDb list's items for Collections' external
// list/chart builder (DESIGN.md §11.11). IMDb has no public API and its
// list CSV-export endpoint is behind an anonymous-request WAF challenge
// (confirmed live: a plain HTTP GET gets an AWS WAF "challenge" response,
// not the CSV), so this reuses the app's existing headless-browser scraper
// (internal/services/browser, already used by the Apple Music/Spotify
// importers) instead of a raw HTTP client. Confirmed live against a real
// public IMDb list that every list page embeds a
// <script type="application/ld+json"> ItemList block with every entry's
// title and IMDb id (in the item's url), all on one page with no
// pagination needed - far more stable than scraping the visible DOM, which
// IMDb re-templates far more often than its SEO structured data.
package imdb

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/drevilish/playlist-lab/internal/services/browser"
	"github.com/drevilish/playlist-lab/internal/services/medialist"
)

var titleIDPattern = regexp.MustCompile(`/title/(tt\d+)/`)

const extractJS = `(() => {
	const el = document.querySelector('script[type="application/ld+json"]');
	return el ? el.textContent : '';
})()`

// GetListItems fetches a public IMDb list's items. listID accepts either a
// bare "ls1234567" id or a full list URL, matching how a user would
// actually copy it out of their browser.
func GetListItems(ctx context.Context, listID string) ([]medialist.Item, error) {
	id := extractListID(listID)
	if id == "" {
		return nil, fmt.Errorf("invalid IMDb list id %q", listID)
	}
	return scrapeItemList(ctx, "IMDb list "+id, "https://www.imdb.com/list/"+id+"/")
}

// GetTop250 fetches IMDb's own Top 250 chart (imdb.com/chart/top/) -
// confirmed live to embed the exact same structured-data shape as a plain
// user list, just at a fixed URL with no list id. Mixes movies and TV
// mini-series with no reliable way to tell them apart from this data, same
// as GetListItems.
func GetTop250(ctx context.Context) ([]medialist.Item, error) {
	return scrapeItemList(ctx, "IMDb Top 250", "https://www.imdb.com/chart/top/")
}

// scrapeItemList is GetListItems/GetTop250's shared scrape+parse core -
// both page shapes carry the same <script type="application/ld+json">
// ItemList block (confirmed live against a real list and the real chart
// page), IMDb doesn't publish media type (movie vs tv) in this structured
// data, so MediaType is left empty on every returned Item - matching
// against the collection's own library type happens by GuidKey alone.
func scrapeItemList(ctx context.Context, label, url string) ([]medialist.Item, error) {
	var raw string
	err := browser.Scrape(ctx, label, browser.DefaultTimeout,
		browser.AcceptLanguage("en-US,en;q=0.9"),
		chromedp.Navigate(url),
		chromedp.Sleep(3*time.Second),
		chromedp.Evaluate(extractJS, &raw),
	)
	if err != nil {
		return nil, fmt.Errorf("%s scrape failed: %w", label, err)
	}
	if raw == "" {
		return nil, fmt.Errorf("%s has no items, or the page structure changed", label)
	}

	var body struct {
		ItemListElement []struct {
			Item struct {
				URL  string `json:"url"`
				Name string `json:"name"`
			} `json:"item"`
		} `json:"itemListElement"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil, fmt.Errorf("%s: unexpected page structure: %w", label, err)
	}

	out := make([]medialist.Item, 0, len(body.ItemListElement))
	for _, el := range body.ItemListElement {
		m := titleIDPattern.FindStringSubmatch(el.Item.URL)
		if m == nil {
			continue
		}
		out = append(out, medialist.Item{GuidKey: "imdb://" + m[1], Title: el.Item.Name})
	}
	return out, nil
}

// extractListID pulls "ls1234567" off either a bare id or a full
// "https://www.imdb.com/list/ls1234567/" style URL.
func extractListID(v string) string {
	v = strings.TrimSpace(v)
	if slash := strings.LastIndex(v, "/list/"); slash != -1 {
		v = v[slash+len("/list/"):]
	}
	if idx := strings.IndexAny(v, "/?#"); idx != -1 {
		v = v[:idx]
	}
	if !strings.HasPrefix(v, "ls") {
		return ""
	}
	return v
}
