package handlers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/drevilish/playlist-lab/internal/auth"
)

// TestMixesPage_DoesNotFetchGenresOrMoods pins the fix for the Generate
// modal's slow load: templates/mixes.html never reads Genres/Moods (only
// mix_advanced_form.html does, via advancedForm()), so page() must not
// block modal-open on a Plex genre/mood round-trip. The fake server fails
// any /genre or /mood request, so a regression that reintroduces the call
// here would fail this test via a bad response, not just run slow.
func TestMixesPage_DoesNotFetchGenresOrMoods(t *testing.T) {
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/genre") || strings.HasSuffix(r.URL.Path, "/mood") {
			t.Errorf("page() must not fetch %s - templates/mixes.html doesn't use Genres/Moods", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine-1"}}`))
	})
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	h := &MixesHandler{DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("c", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodGet, "/mixes", h.page)

	start := time.Now()
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/mixes", "", "")
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if elapsed > 2*time.Second {
		t.Errorf("page() took %v, want well under the 5s genre/mood fetch deadline - it shouldn't be fetching them at all", elapsed)
	}
}
