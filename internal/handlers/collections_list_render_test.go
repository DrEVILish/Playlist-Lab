package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
)

// TestCollectionsList_RendersSortAndFilterColumns is a smoke test for the
// Collections table redesign (multi-select Server/Library filters, a
// provider-aware searchable Builder filter, and new sortable Last Run/Next
// Run/Last Updated/Date Added columns): the real GET /collections
// handler must render end to end without panicking (a bad template field
// reference on the new columns, or a nil method on collectionsQueryState,
// would panic mid-render) and produce the new markup.
func TestCollectionsList_RendersSortAndFilterColumns(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			// libraryNamesForUser resolves the collection's stored
			// library_id "1" to a friendly display name via this call -
			// without it, LibraryDisplay falls back to the raw library_type
			// ("movie"), and the fLibrary="Movies" filter assertion below
			// would be testing the wrong string.
			w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","title":"Movies","type":"movie"}]}}`))
			return
		}
		w.Write([]byte(`{"MediaContainer":{}}`))
	})
	server, err := db.AddUserServer(sqlDB, user.ID, "Home", "client-1", srv.URL, "1", "Movies", "", true)
	if err != nil {
		t.Fatalf("AddUserServer: %v", err)
	}

	coll, err := db.CreateCollection(sqlDB, user.ID, server.ID, "1", "movie", "Action Movies", "", "smart",
		`[{"field":"genre","operator":"is","value":"Action"}]`, "[]", "sync")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if _, err := db.CreateSchedule(sqlDB, user.ID, 0, coll.ID, "collection_refresh", "weekly", "2026-01-01", ""); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	h := &CollectionsHandler{DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodGet, "/collections", func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("HX-Request", "true")
		r.Header.Set("HX-Target", "div#collections-list")
		h.list(w, r)
	})

	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/collections", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Action Movies", "Last Run", "Next Run", "Last Updated", "Date Added",
		"IMDb", "TMDb", "th-filter-search", "External List/Chart (any source)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("render missing %q", want)
		}
	}

	// Sorting by a new column must not panic or drop the row.
	for _, key := range []string{"missing", "lastRun", "nextRun", "updatedAt", "dateAdded", "name"} {
		rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/collections?sort="+key+"&dir=desc", "", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Action Movies") {
			t.Errorf("sort=%s: status=%d, row missing from body", key, rec.Code)
		}
	}

	// Multi-select Server/Library filtering: selecting the collection's own
	// library must keep it visible; selecting an unrelated one must hide it.
	rec = authedRequest(t, sqlDB, router, user, http.MethodGet, "/collections?fLibrary=Movies", "", "")
	if !strings.Contains(rec.Body.String(), "Action Movies") {
		t.Error("fLibrary=Movies incorrectly filtered out a Movies-library row")
	}
	rec = authedRequest(t, sqlDB, router, user, http.MethodGet, "/collections?fLibrary=TV", "", "")
	if strings.Contains(rec.Body.String(), "Action Movies") {
		t.Error("fLibrary=TV incorrectly kept a Movies-library row")
	}

	// Provider filter: this row is a Smart collection, not external_list,
	// so filtering to IMDb must exclude it.
	rec = authedRequest(t, sqlDB, router, user, http.MethodGet, "/collections?fProvider=imdb", "", "")
	if strings.Contains(rec.Body.String(), "Action Movies") {
		t.Error("fProvider=imdb incorrectly kept a Smart-builder row")
	}

	// A list returned after a write uses the current view's filters, not
	// the write endpoint's own query parameters.
	write := testRouter(sqlDB, http.MethodPost, "/collections/bulk-delete", func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("HX-Current-URL", "https://playlist-lab.example/collections?fLibrary=TV")
		h.renderList(w, r)
	})
	rec = authedRequest(t, sqlDB, write, user, http.MethodPost, "/collections/bulk-delete", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "No collections match these filters.") {
		t.Errorf("write response did not preserve current filter: status %d", rec.Code)
	}
}

// A filter URL is a page when opened directly and a fragment when HTMX
// targets the list. The page's initial fetch includes every filter value.
func TestCollectionsList_DeepLink(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &CollectionsHandler{DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab")}
	path := "/collections?fLibrary=Movies&fLibrary=TV&sort=name"
	router := testRouter(sqlDB, http.MethodGet, "/collections", h.list)

	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, path, "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `hx-get="/collections?fLibrary=Movies&amp;fLibrary=TV&amp;sort=name"`) {
		t.Error("direct page did not retain the filters in its initial list request")
	}

	fragment := testRouter(sqlDB, http.MethodGet, "/collections", func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("HX-Request", "true")
		r.Header.Set("HX-Target", "div#collections-list")
		h.list(w, r)
	})
	rec = authedRequest(t, sqlDB, fragment, user, http.MethodGet, path, "", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<!doctype html>") || !strings.Contains(rec.Body.String(), "No collections match these filters.") {
		t.Errorf("HTMX target did not get the filtered fragment: status %d", rec.Code)
	}
}

func TestCollectionsList_ExistingFragmentBookmarkRedirectsToPage(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &CollectionsHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, http.MethodGet, "/collections/list", h.listFragment)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/collections/list?fLibrary=Movies&fLibrary=TV&sort=name", "", "")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/collections?fLibrary=Movies&fLibrary=TV&sort=name" {
		t.Errorf("legacy bookmark: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
}
