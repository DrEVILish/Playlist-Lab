package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
)

// TestPresetsGalleryRenders is a smoke test for the Browse Presets modal
// (collection_presets.go/collection_presets.html) - real Kometa-community-
// sourced presets, grouped by category, each pointing at either the New
// Collection or New Dynamic Collection Set form.
func TestPresetsGalleryRenders(t *testing.T) {
	tmpl := nopTemplates()
	rec := httptest.NewRecorder()
	h := &CollectionsHandler{Tmpl: tmpl}
	req, _ := http.NewRequest(http.MethodGet, "/collections/presets", nil)
	h.presetsGallery(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Browse Presets", "Library Breakdown", "Awards", "Holiday", "Franchise",
		"Genres", "Academy Awards (Oscars)", "hjone72/Oscars.yml", "Christmas Movies",
		"Halloween Collection", "preset-year-input", "All Years...",
		"/collections/presets/oscars/years",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("gallery render missing %q", want)
		}
	}
}

// TestPresetYearsFormRenders is a smoke test for the "All Years" bulk-
// create modal itself (collection_preset_years_form.html).
func TestPresetYearsFormRenders(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","title":"Movies","type":"movie"}]}}`))
			return
		}
		w.Write([]byte(`{"MediaContainer":{}}`))
	})
	if _, err := db.AddUserServer(sqlDB, user.ID, "Home", "client-1", srv.URL, "1", "Movies", "", true); err != nil {
		t.Fatalf("AddUserServer: %v", err)
	}
	h := &CollectionsHandler{DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodGet, "/collections/presets/{slug}/years", h.presetYearsForm)

	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/collections/presets/oscars/years", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Academy Awards (Oscars) - All Years", "From year", "To year", "Movies (movie)"} {
		if !strings.Contains(body, want) {
			t.Errorf("years-form render missing %q", want)
		}
	}

	// An unknown/non-year-parameterized preset must 404, not render blank.
	rec = authedRequest(t, sqlDB, router, user, http.MethodGet, "/collections/presets/genres/years", "", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("non-year preset: status = %d, want 404", rec.Code)
	}
}

// TestNewCollectionForm_AppliesListPreset confirms a "list"-kind preset
// (e.g. a Trakt award-by-year pick) reaches New Collection pre-filled with
// the resolved name and list URL - the actual mechanism DESIGN.md's Browse
// Presets flow depends on.
func TestNewCollectionForm_AppliesListPreset(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{}}`))
	})
	if _, err := db.AddUserServer(sqlDB, user.ID, "Home", "client-1", srv.URL, "1", "Movies", "", true); err != nil {
		t.Fatalf("AddUserServer: %v", err)
	}

	h := &CollectionsHandler{DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodGet, "/collections/new", h.newForm)

	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/collections/new?preset=oscars&year=2020", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`value="Academy Awards (Oscars) 2020"`,
		"https://trakt.tv/users/pjcob/lists/2020-oscars",
		`value="external_list" checked`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("prefilled create-mode render missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestNewDynamicCollectionForm_AppliesPreset confirms a "dynamic"-kind
// preset (e.g. Directors) pre-selects the right facet type on New Dynamic
// Collection Set.
func TestNewDynamicCollectionForm_AppliesPreset(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{}}`))
	})
	if _, err := db.AddUserServer(sqlDB, user.ID, "Home", "client-1", srv.URL, "1", "Movies", "", true); err != nil {
		t.Fatalf("AddUserServer: %v", err)
	}

	h := &DynamicCollectionsHandler{DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodGet, "/dynamic-collections/new", h.newForm)

	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/dynamic-collections/new?preset=directors", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="director" selected`) {
		t.Errorf("prefilled create-mode render missing pre-selected director option\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, `value="Directors"`) {
		t.Errorf("prefilled create-mode render missing prefilled name\n--- body ---\n%s", body)
	}
}
