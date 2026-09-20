package handlers

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
)

// TestBulkDelete_UnmanagedRow covers the real bug reported against the
// Collections page: rows for a real Plex collection with no Playlist Lab
// row (collections.go's unmanagedCollectionViews - most pre-existing Plex
// collections fall in this bucket) never got a bulk-select checkbox at
// all, so "Delete Selected" silently ignored them. bulkDelete now also
// reads a repeated "unmanaged" field (serverID|plexRatingKey) and deletes
// straight from Plex, same as the single-row deleteUnmanaged endpoint.
func TestBulkDelete_UnmanagedRow(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	// bulkDelete's own success path re-renders the list afterward
	// (renderList -> unmanagedCollectionViews), which sweeps every owned
	// server's GetLibraries/GetCollections too - only DELETE calls are the
	// ones under test here, everything else just needs a response that
	// won't blow up JSON decoding (GetLibraries/GetCollections errors are
	// tolerated silently by that sweep either way).
	var deletedPaths []string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletedPaths = append(deletedPaths, r.Method+" "+r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{}}`))
	})
	// AddUserServer's final bool is is_owner - bulkDelete's unmanaged path
	// requires server ownership, the same gate RequirePlexOwner enforces at
	// the route level for every real request.
	server, err := db.AddUserServer(sqlDB, user.ID, "Test Server", "client-1", srv.URL, "1", "Movies", "", true)
	if err != nil {
		t.Fatalf("AddUserServer: %v", err)
	}

	h := &CollectionsHandler{DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodPost, "/collections/bulk-delete", h.bulkDelete)

	body := "unmanaged=" + strconv.FormatInt(server.ID, 10) + "%7C" + "plex-rk-123"
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/collections/bulk-delete", body, "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(deletedPaths) != 1 || deletedPaths[0] != "DELETE /library/collections/plex-rk-123" {
		t.Fatalf("got Plex calls %v, want exactly one DELETE for plex-rk-123", deletedPaths)
	}
}

// TestBulkDelete_UnmanagedRow_RejectsUnownedServer confirms bulkDelete
// can't be used to delete a Plex collection on a server the requesting
// user doesn't own - a raw serverID|plexId pair round-trips through the
// client with no other proof of ownership, so this check is the only thing
// stopping one user from deleting another's collections by guessing ids.
func TestBulkDelete_UnmanagedRow_RejectsUnownedServer(t *testing.T) {
	sqlDB := newTestDB(t)
	owner := newTestUser(t, sqlDB)
	// Not newTestUser again - it derives a user's plex_user_id from t.Name(),
	// identical for both calls within the same (sub)test, which collides on
	// the table's UNIQUE constraint. A second, distinctly-suffixed id here
	// instead.
	attacker, err := db.CreateUser(sqlDB, "plex-attacker", "attacker", "token-attacker", "")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	var deletedPaths []string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletedPaths = append(deletedPaths, r.Method+" "+r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{}}`))
	})
	server, err := db.AddUserServer(sqlDB, owner.ID, "Owner's Server", "client-1", srv.URL, "1", "Movies", "", true)
	if err != nil {
		t.Fatalf("AddUserServer: %v", err)
	}

	h := &CollectionsHandler{DB: sqlDB, Tmpl: nopTemplates(), PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodPost, "/collections/bulk-delete", h.bulkDelete)

	body := "unmanaged=" + strconv.FormatInt(server.ID, 10) + "%7C" + "plex-rk-123"
	rec := authedRequest(t, sqlDB, router, attacker, http.MethodPost, "/collections/bulk-delete", body, "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(deletedPaths) != 0 {
		t.Fatalf("attacker's request reached Plex: %v", deletedPaths)
	}
}
