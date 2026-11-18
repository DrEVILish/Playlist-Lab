package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drevilish/playlist-lab/internal/services/plex"
)

func TestPlexAuthErrorSendsUserToSignInAgain(t *testing.T) {
	if !isPlexAuthError(fmt.Errorf("load: %w", &plex.AuthError{Message: "Invalid or expired Plex token"})) {
		t.Error("wrapped plex.AuthError not recognised")
	}
	if !isPlexAuthError(errors.New("invalid or expired Plex token")) {
		t.Error("plex.tv's plain error not recognised")
	}
	if isPlexAuthError(errors.New("dial tcp: connection refused")) || isPlexAuthError(nil) {
		t.Error("an unreachable server is not an expired sign-in")
	}

	rec := httptest.NewRecorder()
	redirectToReauth(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != reauthURL {
		t.Errorf("page load: got %d %q, want 303 to %s", rec.Code, rec.Header().Get("Location"), reauthURL)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/settings/servers/1/refresh", nil)
	req.Header.Set("HX-Request", "true")
	redirectToReauth(rec, req)
	if rec.Header().Get("HX-Redirect") != reauthURL {
		t.Errorf("htmx request: HX-Redirect = %q, want %s", rec.Header().Get("HX-Redirect"), reauthURL)
	}
}
