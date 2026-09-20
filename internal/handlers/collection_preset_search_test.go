package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSearchPresets_NoAPIKeyConfigured confirms the search UI degrades to a
// clear, actionable message rather than a raw error/panic when the admin
// hasn't configured the relevant provider's key yet.
func TestSearchPresets_NoAPIKeyConfigured(t *testing.T) {
	sqlDB := newTestDB(t)
	h := &CollectionsHandler{DB: sqlDB, Tmpl: nopTemplates()}

	for _, tc := range []struct {
		source, wantSubstr string
	}{
		{"trakt-list", "No Trakt Client ID configured"},
		{"tmdb-collection", "No TMDb API key configured"},
	} {
		rec := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/collections/presets/search?q=oscars&source="+tc.source, nil)
		h.searchPresets(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("source=%s: status = %d", tc.source, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), tc.wantSubstr) {
			t.Errorf("source=%s: body missing %q:\n%s", tc.source, tc.wantSubstr, rec.Body.String())
		}
	}
}

func TestSearchPresets_EmptyQueryShowsDefaultHint(t *testing.T) {
	sqlDB := newTestDB(t)
	h := &CollectionsHandler{DB: sqlDB, Tmpl: nopTemplates()}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/collections/presets/search?q=", nil)
	h.searchPresets(rec, req)
	if !strings.Contains(rec.Body.String(), "Search Trakt's public lists") {
		t.Errorf("empty query should show the default hint, got:\n%s", rec.Body.String())
	}
}

// TestPrefillFromRequest covers both paths a New Collection prefill can
// come from: a registry preset (?preset=) and a live search result's
// ad-hoc values (?provider=&mode=&listId=&name=).
func TestPrefillFromRequest(t *testing.T) {
	t.Run("preset slug with year", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/collections/new?preset=oscars&year=2020", nil)
		p := prefillFromRequest(req)
		if p == nil || p.Name != "Academy Awards (Oscars) 2020" || p.ExternalProvider != "trakt" || p.ExternalListID != "https://trakt.tv/users/pjcob/lists/2020-oscars" {
			t.Errorf("unexpected prefill: %+v", p)
		}
	})

	t.Run("ad-hoc search result", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/collections/new?provider=tmdb&mode=collection&listId=1241&name=Harry+Potter+Collection", nil)
		p := prefillFromRequest(req)
		if p == nil || p.Name != "Harry Potter Collection" || p.ExternalProvider != "tmdb" || p.ExternalMode != "collection" || p.ExternalListID != "1241" {
			t.Errorf("unexpected prefill: %+v", p)
		}
	})

	t.Run("no relevant params", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/collections/new", nil)
		if p := prefillFromRequest(req); p != nil {
			t.Errorf("expected nil prefill, got %+v", p)
		}
	})

	t.Run("unknown preset slug", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/collections/new?preset=does-not-exist", nil)
		if p := prefillFromRequest(req); p != nil {
			t.Errorf("expected nil prefill for unknown slug, got %+v", p)
		}
	})
}
