// Package handlers: settings.go ports routes/settings.ts to HTMX - the
// per-user matching engine config, mix generation defaults, and AI provider
// (Gemini/Grok) API keys. The Plex server/library picker already has its own
// page (servers.go's setup flow) and Connected Services/public-URL config
// haven't been reached by this pass, so this page only covers the three
// tabs above (see GO_REWRITE.md task notes for that scoping call).
package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/ai"
	"github.com/drevilish/playlist-lab/internal/services/matching"
)

type SettingsHandler struct {
	DB   *sql.DB
	Tmpl *Templates
}

func RegisterSettings(r chi.Router, mw *auth.Middleware, h *SettingsHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/settings", h.page)
		r.Post("/settings/matching", h.saveMatching)
		r.Post("/settings/matching/reset", h.resetMatching)
		r.Post("/settings/mixes", h.saveMixes)
		r.Post("/settings/mixes/reset", h.resetMixes)
		r.Post("/settings/ai", h.saveAI)
		r.Post("/settings/ai/test", h.testAI)
	})
}

// mixDefaults mirrors SettingsPage.tsx's DEFAULT_MIX_SETTINGS shape, stored
// as user_settings.mix_settings JSON (db.SaveMixSettingsJSON/
// GetMixSettingsJSON already exist from Phase 1's mixes work - nothing in
// this Go port reads it back into actual mix generation yet, same gap as
// matching_settings; mixes.go's generate form still takes its own
// per-request overrides with hard-coded fallbacks).
type mixDefaults struct {
	WeeklyMix   struct{ TopArtists, TracksPerArtist int }                                     `json:"weeklyMix"`
	DailyMix    struct{ RecentTracks, RelatedTracks, RediscoveryTracks, RediscoveryDays int } `json:"dailyMix"`
	TimeCapsule struct{ TrackCount, DaysAgo, MaxPerArtist int }                               `json:"timeCapsule"`
	NewMusic    struct{ AlbumCount, TracksPerAlbum int }                                      `json:"newMusic"`
}

func defaultMixDefaults() mixDefaults {
	var d mixDefaults
	d.WeeklyMix.TopArtists, d.WeeklyMix.TracksPerArtist = 10, 5
	d.DailyMix.RecentTracks, d.DailyMix.RelatedTracks, d.DailyMix.RediscoveryTracks, d.DailyMix.RediscoveryDays = 20, 15, 15, 90
	d.TimeCapsule.TrackCount, d.TimeCapsule.DaysAgo, d.TimeCapsule.MaxPerArtist = 50, 365, 3
	d.NewMusic.AlbumCount, d.NewMusic.TracksPerAlbum = 10, 3
	return d
}

func mixDefaultsFromJSON(raw string) mixDefaults {
	d := defaultMixDefaults()
	if raw == "" {
		return d
	}
	_ = json.Unmarshal([]byte(raw), &d)
	return d
}

func (h *SettingsHandler) page(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	matchingJSON, _ := db.GetMatchingSettingsJSON(h.DB, user.ID)
	mixJSON, _ := db.GetMixSettingsJSON(h.DB, user.ID)
	aiSettings, _ := db.GetAISettings(h.DB, user.ID)
	isAdmin, _ := db.IsAdmin(h.DB, user.ID)

	h.Tmpl.RenderPage(w, "settings", map[string]any{
		"User":     user,
		"IsAdmin":  isAdmin,
		"Matching": matching.SettingsFromJSON(matchingJSON),
		"Mixes":    mixDefaultsFromJSON(mixJSON),
		"AI":       aiSettings,
	})
}

func (h *SettingsHandler) saveMatching(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	settings := matching.SettingsFromJSON("") // start from defaults, override below
	if v, err := strconv.ParseFloat(r.FormValue("minMatchScore"), 64); err == nil {
		settings.MinMatchScore = v
	}
	settings.StripParentheses = r.FormValue("stripParentheses") == "on"
	settings.StripBrackets = r.FormValue("stripBrackets") == "on"
	settings.UseFirstArtistOnly = r.FormValue("useFirstArtistOnly") == "on"
	settings.IgnoreFeaturedArtists = r.FormValue("ignoreFeaturedArtists") == "on"

	raw, _ := json.Marshal(settings)
	err := db.SaveMatchingSettingsJSON(h.DB, user.ID, string(raw))
	h.renderAlert(w, "Matching settings saved", err)
}

func (h *SettingsHandler) resetMatching(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	raw, _ := json.Marshal(matching.DefaultSettings())
	err := db.SaveMatchingSettingsJSON(h.DB, user.ID, string(raw))
	h.renderAlert(w, "Matching settings reset to defaults", err)
}

func (h *SettingsHandler) saveMixes(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	getInt := func(key string, def int) int {
		if n, err := strconv.Atoi(r.FormValue(key)); err == nil {
			return n
		}
		return def
	}
	d := defaultMixDefaults()
	d.WeeklyMix.TopArtists = getInt("weeklyTopArtists", d.WeeklyMix.TopArtists)
	d.WeeklyMix.TracksPerArtist = getInt("weeklyTracksPerArtist", d.WeeklyMix.TracksPerArtist)
	d.DailyMix.RecentTracks = getInt("dailyRecentTracks", d.DailyMix.RecentTracks)
	d.DailyMix.RelatedTracks = getInt("dailyRelatedTracks", d.DailyMix.RelatedTracks)
	d.DailyMix.RediscoveryTracks = getInt("dailyRediscoveryTracks", d.DailyMix.RediscoveryTracks)
	d.DailyMix.RediscoveryDays = getInt("dailyRediscoveryDays", d.DailyMix.RediscoveryDays)
	d.TimeCapsule.TrackCount = getInt("timeCapsuleTrackCount", d.TimeCapsule.TrackCount)
	d.TimeCapsule.DaysAgo = getInt("timeCapsuleDaysAgo", d.TimeCapsule.DaysAgo)
	d.TimeCapsule.MaxPerArtist = getInt("timeCapsuleMaxPerArtist", d.TimeCapsule.MaxPerArtist)
	d.NewMusic.AlbumCount = getInt("newMusicAlbumCount", d.NewMusic.AlbumCount)
	d.NewMusic.TracksPerAlbum = getInt("newMusicTracksPerAlbum", d.NewMusic.TracksPerAlbum)

	raw, _ := json.Marshal(d)
	err := db.SaveMixSettingsJSON(h.DB, user.ID, string(raw))
	h.renderAlert(w, "Mix defaults saved", err)
}

func (h *SettingsHandler) resetMixes(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	raw, _ := json.Marshal(defaultMixDefaults())
	err := db.SaveMixSettingsJSON(h.DB, user.ID, string(raw))
	h.renderAlert(w, "Mix defaults reset", err)
}

func (h *SettingsHandler) saveAI(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	provider := r.FormValue("aiProvider")
	if provider != "gemini" && provider != "grok" {
		provider = "gemini"
	}
	if err := db.SaveGeminiAPIKey(h.DB, user.ID, r.FormValue("geminiApiKey")); err != nil {
		h.renderAlert(w, "", err)
		return
	}
	if err := db.SaveGrokAPIKey(h.DB, user.ID, r.FormValue("grokApiKey")); err != nil {
		h.renderAlert(w, "", err)
		return
	}
	err := db.SaveAIProvider(h.DB, user.ID, provider)
	h.renderAlert(w, "AI settings saved", err)
}

// testAI ports the Gemini/Grok branch of POST /api/import/ai/test - "does
// this key actually work", checked against whichever key was just typed
// into the form rather than requiring a save first.
func (h *SettingsHandler) testAI(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	provider := r.FormValue("aiProvider")
	var err error
	switch provider {
	case "grok":
		_, err = ai.TestGrok(ctx, r.FormValue("grokApiKey"))
	default:
		_, err = ai.TestGemini(ctx, r.FormValue("geminiApiKey"))
	}
	if err != nil {
		h.renderAlert(w, "", err)
		return
	}
	h.renderAlert(w, "Connection successful", nil)
}

func (h *SettingsHandler) renderAlert(w http.ResponseWriter, successMsg string, err error) {
	data := map[string]any{}
	if err != nil {
		data["Error"] = err.Error()
	} else {
		data["Success"] = successMsg
	}
	h.Tmpl.RenderPartial(w, "partials/settings_alert.html", data)
}
