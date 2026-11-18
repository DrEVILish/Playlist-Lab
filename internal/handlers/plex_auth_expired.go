package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/drevilish/playlist-lab/internal/services/plex"
)

// isPlexAuthError reports whether Plex rejected the account's token (both
// the server client's AuthError and plex.tv's "invalid or expired Plex
// token"). Nothing that talks to Plex works until the user signs in again.
func isPlexAuthError(err error) bool {
	if err == nil {
		return false
	}
	var ae *plex.AuthError
	return errors.As(err, &ae) || strings.Contains(strings.ToLower(err.Error()), "expired plex token")
}

// reauthURL is the login page in its "your Plex sign-in expired" mode.
const reauthURL = "/login?expired=1"

// redirectToReauth sends the user to sign in again, rather than showing a
// raw error they can't act on (and that once led to deleting their servers
// trying to fix it).
func redirectToReauth(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", reauthURL)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, reauthURL, http.StatusSeeOther)
}
