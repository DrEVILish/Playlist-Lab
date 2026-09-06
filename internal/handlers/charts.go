// Package handlers: charts.go ports routes/charts.ts directly - a small
// JSON API returning popular playlists for a source+country, used by the
// import UI's "browse charts" flow. No htmx fragment/template exists for
// this yet (the Go import page only supports deezer/listenbrainz/youtube/
// apple direct-URL import so far, see import.go's doc), so this stays a
// JSON endpoint like the original rather than inventing UI ahead of it.
package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/adapters/spotify"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/services/scrapers"
)

// ChartsHandler needs DB+config access (unlike the other sources here) to
// resolve a Spotify Client Credentials token - see
// spotify.GetClientCredentialsToken.
type ChartsHandler struct {
	DB                  *sql.DB
	SessionSecret       string
	SpotifyClientID     string
	SpotifyClientSecret string
}

func RegisterCharts(r chi.Router, mw *auth.Middleware, h *ChartsHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/api/charts/{source}/{country}", h.chartsHandler)
	})
}

type chartPlaylist struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Count       int    `json:"count,omitempty"`
}

func (h *ChartsHandler) chartsHandler(w http.ResponseWriter, r *http.Request) {
	source := chi.URLParam(r, "source")
	country := chi.URLParam(r, "country")
	user := auth.CurrentUser(r)

	var playlists []chartPlaylist
	switch source {
	case "deezer":
		for _, p := range scrapers.DeezerPopularPlaylists(country) {
			playlists = append(playlists, chartPlaylist{Name: p.Name, URL: p.URL, Description: p.Description, Count: p.Count})
		}
	case "apple":
		for _, p := range scrapers.SearchAppleMusicPlaylists(country) {
			playlists = append(playlists, chartPlaylist{Name: p.Name, URL: p.URL, Description: p.Description, Count: p.Count})
		}
	case "spotify":
		var userID int64
		if user != nil {
			userID = user.ID
		}
		token, err := spotify.GetClientCredentialsToken(h.DB, h.SessionSecret, userID, h.SpotifyClientID, h.SpotifyClientSecret)
		if err != nil || token == "" {
			break // no credentials configured - empty list, not a hard error
		}
		for _, p := range scrapers.SpotifyPopularPlaylists(country, token) {
			playlists = append(playlists, chartPlaylist{Name: p.Name, URL: p.URL, Description: p.Description, Count: p.Count})
		}
	case "youtube":
		// searchYouTubeMusicPlaylists (ytmusic-api) has no Go port yet -
		// same R1-shaped "no mature pure-Go equivalent" gap as the
		// InnerTube adapter, not attempted in this phase's time-box.
		// Returning an empty list (not a 400) matches the original's
		// behavior for a source with zero results, and keeps the UI from
		// treating this as a hard error.
	default:
		writeJSONError(w, http.StatusBadRequest, "Unsupported source: "+source)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"playlists": playlists})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
