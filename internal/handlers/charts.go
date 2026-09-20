// Package handlers: charts.go ports routes/charts.ts's popular-playlist
// lookup, plus the Import page features layered on top of it in v1.x's
// ImportPage.tsx that never got an htmx counterpart in this rewrite: the
// chart-browsing card grid, search-by-name (Deezer/Spotify only - the only
// two sources with a public/app-level search API to reuse here), and saved
// Spotify usernames with profile-playlist browsing. All four render the
// same shared partials/import_playlist_cards.html grid and drop straight
// into the existing POST /import/preview form (import.html) to actually
// import a card - no separate import codepath needed.
package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/adapters/spotify"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/scrapers"
)

// ChartsHandler needs DB+config access (unlike the other sources here) to
// resolve a Spotify Client Credentials token - see
// spotify.GetClientCredentialsToken.
type ChartsHandler struct {
	DB                  *sql.DB
	Tmpl                *Templates
	SessionSecret       string
	SpotifyClientID     string
	SpotifyClientSecret string
	Notifications       *notifications.Store
}

func RegisterCharts(r chi.Router, mw *auth.Middleware, h *ChartsHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/api/charts/{source}/{country}", h.chartsHandler)

		r.Get("/import/charts", h.chartsFragment)
		r.Get("/import/search", h.searchFragment)

		r.Get("/import/spotify-users", h.listSpotifyUsers)
		r.Post("/import/spotify-users", h.addSpotifyUser)
		r.Delete("/import/spotify-users/{id}", h.deleteSpotifyUser)
		r.Get("/import/spotify-users/{spotifyUserId}/playlists", h.browseSpotifyUser)
	})
}

type chartPlaylist struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Count       int    `json:"count,omitempty"`
}

// popularPlaylists dispatches to the right scraper/adapter for source,
// shared by chartsHandler (JSON) and chartsFragment (HTML).
func (h *ChartsHandler) popularPlaylists(ctx context.Context, userID int64, source, country string) ([]scrapers.PopularPlaylist, error) {
	switch source {
	case "deezer":
		return scrapers.DeezerPopularPlaylists(country), nil
	case "apple":
		return scrapers.SearchAppleMusicPlaylists(country), nil
	case "spotify":
		token, err := spotify.GetClientCredentialsToken(ctx, h.DB, h.SessionSecret, userID, h.SpotifyClientID, h.SpotifyClientSecret)
		if err != nil || token == "" {
			return nil, nil // no credentials configured - empty list, not a hard error
		}
		return scrapers.SpotifyPopularPlaylists(country, token), nil
	case "youtube":
		// searchYouTubeMusicPlaylists (ytmusic-api) has no Go port yet -
		// same R1-shaped "no mature pure-Go equivalent" gap as the
		// InnerTube adapter, not attempted in this phase's time-box.
		return nil, nil
	default:
		return nil, errUnsupportedChartSource
	}
}

var errUnsupportedChartSource = errors.New("unsupported source")

func (h *ChartsHandler) chartsHandler(w http.ResponseWriter, r *http.Request) {
	source := chi.URLParam(r, "source")
	country := chi.URLParam(r, "country")
	user := auth.CurrentUser(r)

	popular, err := h.popularPlaylists(r.Context(), user.ID, source, country)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Unsupported source: "+source)
		return
	}
	playlists := make([]chartPlaylist, len(popular))
	for i, p := range popular {
		playlists[i] = chartPlaylist{Name: p.Name, URL: p.URL, Description: p.Description, Count: p.Count}
	}
	writeJSON(w, http.StatusOK, map[string]any{"playlists": playlists})
}

// chartsFragment is chartsHandler's htmx counterpart: same lookup, rendered
// as the shared card grid instead of JSON. Only deezer/spotify are wired
// into the Import page's UI (apple/youtube aren't registered import
// sources in this build - see import.go's importSources doc), but the
// lookup itself stays source-agnostic in case that changes.
func (h *ChartsHandler) chartsFragment(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	source := r.URL.Query().Get("source")
	country := r.URL.Query().Get("country")
	if country == "" {
		country, _ = db.GetCountry(h.DB, user.ID)
	} else {
		_ = db.SaveCountry(h.DB, user.ID, country)
	}

	popular, err := h.popularPlaylists(r.Context(), user.ID, source, country)
	if err != nil {
		h.renderCards(w, source, nil, "Unsupported source.")
		return
	}
	h.renderCards(w, source, popular, "No charts available for this source right now.")
}

// searchFragment backs the "search by name" box, Deezer/Spotify only (see
// package doc) - both have a public/app-level playlist-name search API
// this reuses directly, no per-user OAuth needed.
func (h *ChartsHandler) searchFragment(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	source := r.URL.Query().Get("source")
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		h.renderCards(w, source, nil, "Type something to search.")
		return
	}

	var results []scrapers.PopularPlaylist
	var err error
	switch source {
	case "deezer":
		results = scrapers.SearchDeezerPlaylists(query)
	case "spotify":
		results, err = spotify.SearchPlaylists(r.Context(), h.DB, h.SessionSecret, user.ID, h.SpotifyClientID, h.SpotifyClientSecret, query)
	default:
		h.renderCards(w, source, nil, "Search isn't available for this source.")
		return
	}
	if err != nil {
		h.renderCards(w, source, nil, "Search failed: "+err.Error())
		return
	}
	h.renderCards(w, source, results, "No playlists found.")
}

func (h *ChartsHandler) renderCards(w http.ResponseWriter, source string, playlists []scrapers.PopularPlaylist, emptyMessage string) {
	h.Tmpl.RenderPartial(w, "partials/import_playlist_cards.html", map[string]any{
		"Source": source, "Playlists": playlists, "EmptyMessage": emptyMessage,
	})
}

func (h *ChartsHandler) listSpotifyUsers(w http.ResponseWriter, r *http.Request) {
	h.renderSpotifyUserList(w, r)
}

func (h *ChartsHandler) renderSpotifyUserList(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	users, err := db.GetSavedSpotifyUsers(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.Tmpl.RenderPartial(w, "partials/import_spotify_users.html", map[string]any{"Users": users})
}

// spotifyProfileURL matches a pasted Spotify profile URL so a user can
// paste either that or a bare username, same as v1.x's addSavedSpotifyUser.
var spotifyProfileURL = regexp.MustCompile(`open\.spotify\.com/user/([a-zA-Z0-9_-]+)`)

func (h *ChartsHandler) addSpotifyUser(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	_ = r.ParseForm()
	spotifyUserID := strings.TrimSpace(r.FormValue("spotifyUserId"))
	if m := spotifyProfileURL.FindStringSubmatch(spotifyUserID); m != nil {
		spotifyUserID = m[1]
	}
	spotifyUserID = strings.TrimPrefix(spotifyUserID, "@")
	if spotifyUserID == "" {
		http.Error(w, "spotifyUserId is required", http.StatusBadRequest)
		return
	}
	if _, err := db.AddSavedSpotifyUser(h.DB, user.ID, spotifyUserID, spotifyUserID); err != nil {
		slog.Error("failed to save Spotify user", "error", err, "userId", user.ID)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Save Spotify user", err.Error(), notifications.StatusError, nil)
		http.Error(w, "failed to save Spotify user", http.StatusInternalServerError)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Save Spotify user", spotifyUserID, notifications.StatusSuccess, nil)
	h.renderSpotifyUserList(w, r)
}

func (h *ChartsHandler) deleteSpotifyUser(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := db.DeleteSavedSpotifyUser(h.DB, user.ID, id); err != nil {
		slog.Error("failed to remove Spotify user", "error", err, "userId", user.ID, "id", id)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Remove Spotify user", err.Error(), notifications.StatusError, nil)
		http.Error(w, "failed to remove Spotify user", http.StatusInternalServerError)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Remove Spotify user", "Removed", notifications.StatusSuccess, nil)
	h.renderSpotifyUserList(w, r)
}

// browseSpotifyUser lists a saved username's public playlists as the
// shared card grid, so importing one is the same single-click form as
// charts/search results.
func (h *ChartsHandler) browseSpotifyUser(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	spotifyUserID := chi.URLParam(r, "spotifyUserId")
	playlists, err := spotify.GetUserPlaylists(r.Context(), h.DB, h.SessionSecret, user.ID, h.SpotifyClientID, h.SpotifyClientSecret, spotifyUserID)
	if err != nil {
		h.renderCards(w, "spotify", nil, "Failed to load this user's playlists: "+err.Error())
		return
	}
	h.renderCards(w, "spotify", playlists, "This user has no public playlists.")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
