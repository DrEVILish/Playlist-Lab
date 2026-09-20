package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

// TestCreatePresetYears_CreatesOneCollectionPerYear is the core of "add a
// set of collections for ALL years of an award ceremony at once": a single
// POST with a year range must produce one external_list Collection row per
// year, each pointed at that year's resolved list URL.
func TestCreatePresetYears_CreatesOneCollectionPerYear(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{}}`))
	})
	server, err := db.AddUserServer(sqlDB, user.ID, "Home", "client-1", srv.URL, "1", "Movies", "", true)
	if err != nil {
		t.Fatalf("AddUserServer: %v", err)
	}

	store := notifications.NewStore()
	h := &CollectionsHandler{
		DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab"),
		Notifications: store, Queue: actionqueue.New(store),
	}
	router := testRouter(sqlDB, http.MethodPost, "/collections/presets/{slug}/years", h.createPresetYears)

	libValue := server.ID
	body := "library=" + itoa(libValue) + "%7C1%7Cmovie%7CMovies&fromYear=2018&toYear=2020"
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/collections/presets/oscars/years", body, "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}

	rows, err := db.GetUserCollections(sqlDB, user.ID)
	if err != nil {
		t.Fatalf("GetUserCollections: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 collections (2018-2020), got %d: %+v", len(rows), rows)
	}
	wantNames := map[string]string{
		"Academy Awards (Oscars) 2018": "https://trakt.tv/users/pjcob/lists/2018-oscars",
		"Academy Awards (Oscars) 2019": "https://trakt.tv/users/pjcob/lists/2019-oscars",
		"Academy Awards (Oscars) 2020": "https://trakt.tv/users/pjcob/lists/2020-oscars",
	}
	for _, row := range rows {
		wantURL, ok := wantNames[row.Name]
		if !ok {
			t.Errorf("unexpected collection name %q", row.Name)
			continue
		}
		src := row.ParsedExternalListSource()
		if src.Provider != "trakt" || src.ListID != wantURL {
			t.Errorf("%s: got provider=%q listID=%q, want provider=trakt listID=%q", row.Name, src.Provider, src.ListID, wantURL)
		}
		delete(wantNames, row.Name)
	}
	if len(wantNames) != 0 {
		t.Errorf("missing expected years: %v", wantNames)
	}
}

// TestCreatePresetYears_SkipsExistingAndRejectsHugeRanges covers the two
// safety behaviors: re-running a batch that overlaps existing collections
// shouldn't duplicate them, and an absurd range (e.g. a typo) is rejected
// rather than silently creating hundreds of rows.
func TestCreatePresetYears_SkipsExistingAndRejectsHugeRanges(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{}}`))
	})
	server, err := db.AddUserServer(sqlDB, user.ID, "Home", "client-1", srv.URL, "1", "Movies", "", true)
	if err != nil {
		t.Fatalf("AddUserServer: %v", err)
	}
	// Pre-existing "Academy Awards (Oscars) 2019" - the batch below should
	// skip it rather than creating a second row with the same name.
	if _, err := db.CreateCollection(sqlDB, user.ID, server.ID, "1", "movie", "Academy Awards (Oscars) 2019", "", "external_list",
		`{"provider":"trakt","mode":"list","listId":"https://trakt.tv/users/pjcob/lists/2019-oscars"}`, "[]", "sync"); err != nil {
		t.Fatalf("seed CreateCollection: %v", err)
	}

	store := notifications.NewStore()
	h := &CollectionsHandler{
		DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab"),
		Notifications: store, Queue: actionqueue.New(store),
	}
	router := testRouter(sqlDB, http.MethodPost, "/collections/presets/{slug}/years", h.createPresetYears)

	body := "library=" + itoa(server.ID) + "%7C1%7Cmovie%7CMovies&fromYear=2018&toYear=2020"
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/collections/presets/oscars/years", body, "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	rows, err := db.GetUserCollections(sqlDB, user.ID)
	if err != nil {
		t.Fatalf("GetUserCollections: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected exactly 3 collections total (1 seeded + 2 new, 2019 not duplicated), got %d: %+v", len(rows), rows)
	}

	// A from/to spanning way more than maxPresetYearRange must be rejected.
	hugeBody := "library=" + itoa(server.ID) + "%7C1%7Cmovie%7CMovies&fromYear=1900&toYear=2030"
	rec = authedRequest(t, sqlDB, router, user, http.MethodPost, "/collections/presets/oscars/years", hugeBody, "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("huge range: status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "keep a single batch") {
		t.Errorf("huge range error message missing guidance: %s", rec.Body.String())
	}
}
