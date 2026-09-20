package scheduler

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

func nullJSON(s string) sql.NullString { return sql.NullString{Valid: true, String: s} }

func TestFilterKeys(t *testing.T) {
	keys := []string{"Action", "Comedy", "Horror", "Drama"}

	t.Run("no include/exclude returns everything", func(t *testing.T) {
		got := filterKeys(keys, &db.DynamicCollection{})
		if len(got) != 4 {
			t.Fatalf("got %v, want all 4 keys", got)
		}
	})

	t.Run("include restricts to listed keys, case-insensitively", func(t *testing.T) {
		dyn := &db.DynamicCollection{IncludeKeys: nullJSON(`["action","DRAMA"]`)}
		got := filterKeys(keys, dyn)
		if len(got) != 2 || got[0] != "Action" || got[1] != "Drama" {
			t.Fatalf("got %v, want [Action Drama]", got)
		}
	})

	t.Run("exclude removes listed keys", func(t *testing.T) {
		dyn := &db.DynamicCollection{ExcludeKeys: nullJSON(`["Horror"]`)}
		got := filterKeys(keys, dyn)
		for _, k := range got {
			if k == "Horror" {
				t.Fatalf("Horror should have been excluded, got %v", got)
			}
		}
		if len(got) != 3 {
			t.Fatalf("got %v, want 3 keys", got)
		}
	})

	t.Run("include wins when both are set", func(t *testing.T) {
		dyn := &db.DynamicCollection{IncludeKeys: nullJSON(`["Comedy"]`), ExcludeKeys: nullJSON(`["Comedy"]`)}
		got := filterKeys(keys, dyn)
		if len(got) != 1 || got[0] != "Comedy" {
			t.Fatalf("got %v, want [Comedy] (include should win over exclude)", got)
		}
	})
}

func TestTitleFor(t *testing.T) {
	tests := []struct {
		name, format, key, want string
	}{
		{"default placeholder", "<<key_name>>", "Action", "Action"},
		{"prefixed format", "Top <<key_name>> Movies", "Comedy", "Top Comedy Movies"},
		{"missing placeholder falls back to bare key", "Fixed Name", "Comedy", "Comedy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := titleFor(&db.DynamicCollection{TitleFormat: tt.format}, tt.key)
			if got != tt.want {
				t.Errorf("titleFor(%q, %q) = %q, want %q", tt.format, tt.key, got, tt.want)
			}
		})
	}
}

// TestFetchFacetKeysDecade confirms year values from Plex's own "year"
// facet directory get bucketed into decades correctly (e.g. 1995 and 1990
// -> "1990", 2021 -> "2020") and deduped/sorted - the one piece of
// fetchFacetKeys with real logic in it, everything else is a passthrough to
// an existing plex.Client method.
func TestFetchFacetKeysDecade(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"Directory":[{"title":"1995"},{"title":"1990"},{"title":"2021"}]}}`))
	}))
	defer srv.Close()

	client := plex.NewClient(srv.URL, "token", "client-id", "Playlist Lab")
	dyn := &db.DynamicCollection{FacetType: "decade", LibrarySectionID: "1"}

	got, err := fetchFacetKeys(client, dyn)
	if err != nil {
		t.Fatalf("fetchFacetKeys: %v", err)
	}
	want := []string{"1990", "2020"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
