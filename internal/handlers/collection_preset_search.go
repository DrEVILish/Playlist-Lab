// collection_preset_search.go extends Browse Presets (collection_presets.go)
// from a fixed, curated list into a live search: "search for or discover...
// premade collections or lists, rather than just having a list of some
// subset" - a hardcoded registry can only ever cover what someone thought
// to add ahead of time, so this searches the real providers directly.
//
// Two real search APIs are wired up, both already backed by a client this
// app has (trakt/tmdb):
//   - Trakt's public list search (GET /search/list) - any public list on
//     Trakt, not just the one curator's award-show lists the static Awards
//     presets hardcode.
//   - TMDb's collection search (GET /search/collection) - any franchise
//     TMDb has a "collection" object for, not just the 4 this app hardcodes
//     under Franchise.
//
// IMDb has no official API (this app's existing IMDb support is a scraper
// for a known list/chart URL, not a search endpoint) and Letterboxd/TVDb
// have no public search API either, so those three stay preset/paste-a-URL
// only - not a gap introduced here, just not solvable without inventing an
// unofficial scrape of a search results page.
package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/tmdb"
	"github.com/drevilish/playlist-lab/internal/services/trakt"
)

// presetSearchResult is the one shape both providers' results render as in
// collection_preset_search_results.html - a name/description/detail line
// plus exactly what New Collection needs to be pre-filled from it.
type presetSearchResult struct {
	Name        string
	Description string
	Detail      string // e.g. "52 items · 10 likes" or "TMDb Collection"
	Provider    string
	Mode        string
	ListID      string
	MediaType   string // "movie" | "" (unknown) - see collectionPrefill.MediaType
}

// searchPresets handles GET /collections/presets/search?q=&source=
// (trakt-list | tmdb-collection), rendering live results into the Browse
// Presets modal's search section.
func (h *CollectionsHandler) searchPresets(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	source := r.URL.Query().Get("source")
	if q == "" {
		h.Tmpl.RenderPartial(w, "partials/collection_preset_search_results.html", map[string]any{"Query": q})
		return
	}

	var results []presetSearchResult
	var errMsg string
	switch source {
	case "tmdb-collection":
		apiKey, _, _ := db.GetAdminConfig(h.DB, configKeyTMDbAPIKey)
		if apiKey == "" {
			errMsg = "No TMDb API key configured (Settings > Administration > TMDb)."
			break
		}
		hits, err := tmdb.NewClient(apiKey).SearchCollections(q)
		if err != nil {
			errMsg = "TMDb search failed: " + err.Error()
			break
		}
		for _, hit := range hits {
			results = append(results, presetSearchResult{
				// TMDb's "collection" object (a franchise grouping) is a
				// movie-only concept in TMDb's own data model - TV has no
				// equivalent - so this is always known-movie, unlike a Trakt
				// list below (could be either, or mixed).
				Name: hit.Name, Detail: "TMDb Collection", Provider: "tmdb", Mode: "collection", ListID: strconv.Itoa(hit.ID), MediaType: "movie",
			})
		}
	default: // "trakt-list"
		apiKey, _, _ := db.GetAdminConfig(h.DB, configKeyTraktAPIKey)
		if apiKey == "" {
			errMsg = "No Trakt Client ID configured (Settings > Administration > Trakt)."
			break
		}
		hits, err := trakt.NewClient(apiKey).SearchLists(q)
		if err != nil {
			errMsg = "Trakt search failed: " + err.Error()
			break
		}
		for _, hit := range hits {
			detail := strconv.Itoa(hit.ItemCount) + " items"
			if hit.Likes > 0 {
				detail += " · " + strconv.Itoa(hit.Likes) + " likes"
			}
			results = append(results, presetSearchResult{
				Name: hit.Name, Description: hit.Description, Detail: detail, Provider: "trakt", Mode: "list", ListID: hit.URL,
			})
		}
	}

	h.Tmpl.RenderPartial(w, "partials/collection_preset_search_results.html", map[string]any{
		"Query": q, "Source": source, "Results": results, "Error": errMsg,
	})
}

// prefillFromRequest builds New Collection's Prefill from either a Browse
// Presets registry pick (?preset=slug, optionally &year=) or a live search
// result's ad-hoc values (?provider=&mode=&listId=&name=) - the two paths
// converge on the exact same collectionPrefill shape the create form
// already knows how to render, so no template change was needed to support
// search results alongside the curated presets.
func prefillFromRequest(r *http.Request) *collectionPrefill {
	q := r.URL.Query()
	if slug := q.Get("preset"); slug != "" {
		if p := findCollectionPreset(slug); p != nil && p.Kind == "list" {
			year := q.Get("year")
			return &collectionPrefill{
				Name: p.resolvedName(year), ExternalProvider: p.Provider, ExternalMode: p.Mode,
				ExternalListID: p.resolvedListID(year), MediaType: p.MediaType,
			}
		}
		return nil
	}
	provider, listID := q.Get("provider"), q.Get("listId")
	if provider == "" || listID == "" {
		return nil
	}
	mode := q.Get("mode")
	if mode == "" {
		mode = "list"
	}
	return &collectionPrefill{
		Name: q.Get("name"), ExternalProvider: provider, ExternalMode: mode, ExternalListID: listID,
		MediaType: q.Get("mediaType"),
	}
}
