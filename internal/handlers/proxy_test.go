package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
)

// Deliberately inlined rather than reusing playlists_test.go's
// newFakePlexServer/seedUserServer helpers: those live in a different test
// file, and duplicating their names here would collide the moment both are
// in the package. Only handlers_test.go's shared harness is relied on.
func proxyTestFixture(t *testing.T, plex http.HandlerFunc) (*sql.DB, *db.User, chi.Router) {
	t.Helper()
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	srv := httptest.NewServer(plex)
	t.Cleanup(srv.Close)
	if _, err := db.AddUserServer(sqlDB, user.ID, "Test Server", "client-1", srv.URL, "1", "Music", "", false); err != nil {
		t.Fatalf("SaveUserServer: %v", err)
	}

	h := &ProxyHandler{DB: sqlDB, PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab")}
	return sqlDB, user, testRouter(sqlDB, http.MethodGet, "/proxy/image", h.image)
}

// The whole point of this handler is that the Plex token stays server-side,
// so that is what this asserts: the image bytes reach the browser, the
// token only ever goes upstream.
func TestImageProxy_RelaysBytesWithoutLeakingToken(t *testing.T) {
	const imageBody = "\x89PNG\r\n\x1a\nfake-cover-bytes"
	var gotPath, gotToken string

	sqlDB, user, router := proxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Plex-Token")
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte(imageBody))
	})

	rec := authedRequest(t, sqlDB, router, user, http.MethodGet,
		"/proxy/image?path=%2Flibrary%2Fmetadata%2F42%2Fcomposite%2F1", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != imageBody {
		t.Errorf("body = %q, want the upstream image bytes", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png forwarded from upstream", got)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "max-age=86400") {
		t.Errorf("Cache-Control = %q, want the 24h cache proxy.ts set", got)
	}
	if gotPath != "/library/metadata/42/composite/1" {
		t.Errorf("upstream path = %q, want the requested composite path", gotPath)
	}
	if gotToken == "" {
		t.Error("no X-Plex-Token sent upstream - the proxy must authenticate to Plex itself")
	}
	if strings.Contains(rec.Body.String(), gotToken) {
		t.Error("the user's Plex token leaked into the response body")
	}
	for name, values := range rec.Result().Header {
		for _, v := range values {
			if strings.Contains(v, gotToken) {
				t.Errorf("the user's Plex token leaked into response header %s", name)
			}
		}
	}
}

// Plex 404s a composite it hasn't generated yet, which is routine rather
// than exceptional; a broken-image icon on those rows would read as a bug
// in this app.
func TestImageProxy_UpstreamFailureServesPlaceholder(t *testing.T) {
	sqlDB, user, router := proxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/proxy/image?path=%2Fmissing%2Fthumb", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a placeholder rather than an error page", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if rec.Body.Len() == 0 {
		t.Error("empty body, want the 1x1 transparent pixel")
	}
}

// No attacker-controlled host may be reachable through this handler - that
// was the SSRF/token-harvesting hole in the Node route it replaces.
func TestImageProxy_RejectsNonRelativePaths(t *testing.T) {
	var upstreamHits int
	sqlDB, user, router := proxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
	})

	for _, path := range []string{
		"http%3A%2F%2Fevil.example%2Fsteal", // absolute URL
		"%2F%2Fevil.example%2Fsteal",        // scheme-relative
		"",                                  // missing
		"library%2Fmetadata%2F1",            // not rooted
	} {
		rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/proxy/image?path="+path, "", "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("path=%q: status = %d, want 400", path, rec.Code)
		}
	}
	if upstreamHits != 0 {
		t.Errorf("made %d upstream request(s) for rejected paths, want 0", upstreamHits)
	}
}

func TestImageProxyURL(t *testing.T) {
	if got := ImageProxyURL(""); got != "" {
		t.Errorf(`ImageProxyURL("") = %q, want "" so templates can skip the <img> entirely`, got)
	}
	got := ImageProxyURL("/library/metadata/42/composite/1")
	if want := "/proxy/image?path=%2Flibrary%2Fmetadata%2F42%2Fcomposite%2F1"; got != want {
		t.Errorf("ImageProxyURL = %q, want %q", got, want)
	}
}
