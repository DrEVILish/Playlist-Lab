package handlers

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/db"
)

// TestCollectionFormRenders is a smoke test for the redesigned Collection
// modal (frontend-design pass): both create and edit mode must render
// without panicking (a template execution error - a bad field name, a nil
// dereference on a value only present in one mode - would panic mid-render
// rather than fail gracefully) and carry the new section/spec-plate markup
// the redesign introduced.
func TestCollectionFormRenders(t *testing.T) {
	tmpl := nopTemplates()

	t.Run("create", func(t *testing.T) {
		rec := httptest.NewRecorder()
		tmpl.RenderPartial(rec, "partials/collection_form.html", map[string]any{
			"ServerGroups": []serverLibraryGroup{{ServerName: "Home", Libraries: []libraryPickerOption{{Value: "1|1|movie|Movies", Name: "Movies (movie)"}}}},
		})
		body := rec.Body.String()
		if rec.Code != 200 {
			t.Fatalf("status %d, body: %s", rec.Code, body)
		}
		for _, want := range []string{"New Collection", "Membership", "collection-form-section", "Behavior", "Sort Title"} {
			if !strings.Contains(body, want) {
				t.Errorf("create-mode render missing %q", want)
			}
		}
		if strings.Contains(body, "collection-spec-plate") {
			t.Error("create mode has nothing fixed-at-creation to show yet, spec plate shouldn't render")
		}
	})

	t.Run("edit smart", func(t *testing.T) {
		coll := db.Collection{
			ID: 1, Name: "Action Movies", BuilderType: "smart", SyncMode: "sync",
			LibraryType: "movie", Rules: sql.NullString{Valid: true, String: `[{"field":"genre","operator":"is","value":"Action"}]`},
			SortTitle: sql.NullString{Valid: true, String: "+1_Action"},
		}
		rec := httptest.NewRecorder()
		tmpl.RenderPartial(rec, "partials/collection_form.html", map[string]any{
			"Collection": CollectionView{Collection: coll, ServerName: "Home", LibraryDisplay: "Movies", RulesSummary: "genre=Action"},
		})
		body := rec.Body.String()
		if rec.Code != 200 {
			t.Fatalf("status %d, body: %s", rec.Code, body)
		}
		// html/template correctly HTML-entity-escapes "+" in an attribute
		// value (&#43;) - a browser renders that back to a literal "+", so
		// this checks for the escaped form rather than a raw "+".
		for _, want := range []string{"Edit Collection", "collection-spec-plate", ">Home<", ">Movies<", "Rules", "&#43;1_Action", "Danger Zone"} {
			if !strings.Contains(body, want) {
				t.Errorf("edit-mode render missing %q", want)
			}
		}
	})
}

// TestDynamicCollectionFormRenders mirrors the above for the Dynamic
// Collections modal - the biggest structural change there (the All/Include/
// Exclude scope picker replacing two always-visible textareas) is template
// logic with real branches (ParsedInclude/ParsedExclude presence picks the
// initial radio), exactly the kind of thing worth one runnable check.
func TestDynamicCollectionFormRenders(t *testing.T) {
	tmpl := nopTemplates()

	t.Run("create", func(t *testing.T) {
		rec := httptest.NewRecorder()
		tmpl.RenderPartial(rec, "partials/dynamic_collection_form.html", map[string]any{
			"ServerGroups":     []serverLibraryGroup{{ServerName: "Home", Libraries: []libraryPickerOption{{Value: "1|1|movie|Movies", Name: "Movies (movie)"}}}},
			"FacetTypes":       facetTypeLabels,
			"PrefillFacetType": "", "PrefillName": "",
		})
		body := rec.Body.String()
		if rec.Code != 200 {
			t.Fatalf("status %d, body: %s", rec.Code, body)
		}
		for _, want := range []string{"New Dynamic Collection Set", "Which values get a collection", "All values", `value="all" checked`, "Preview:", "Action"} {
			if !strings.Contains(body, want) {
				t.Errorf("create-mode render missing %q", want)
			}
		}
	})

	t.Run("edit with include list picks the Only-these scope", func(t *testing.T) {
		dyn := db.DynamicCollection{
			ID: 1, Name: "Genres", FacetType: "genre", TitleFormat: "Top <<key_name>>",
			SyncMode: "sync", IncludeKeys: sql.NullString{Valid: true, String: `["Action","Comedy"]`},
		}
		rec := httptest.NewRecorder()
		tmpl.RenderPartial(rec, "partials/dynamic_collection_form.html", map[string]any{
			"Set": DynamicCollectionView{DynamicCollection: dyn, ServerName: "Home", LibraryDisplay: "Movies", TypeLabel: "Genre"},
		})
		body := rec.Body.String()
		if rec.Code != 200 {
			t.Fatalf("status %d, body: %s", rec.Code, body)
		}
		for _, want := range []string{"Edit Dynamic Collection Set", ">Home<", ">Movies<", `value="include" checked`, "Top Action", "Danger Zone"} {
			if !strings.Contains(body, want) {
				t.Errorf("edit-mode render missing %q", want)
			}
		}
		if strings.Contains(body, `value="all" checked`) {
			t.Error("a set with an include list should not default the scope picker back to All values")
		}
	})
}
