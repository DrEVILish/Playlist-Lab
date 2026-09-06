// mix_templates.go ports routes/mix-templates.ts to HTMX. The TS route's
// elaborate sanitize/validate/migrate/parse pipeline exists to make a
// browser-submitted arbitrary JSON blob safe to trust; this port keeps that
// same contract (name + mixType + a JSON configuration object containing at
// least trackCount) but skips re-deriving its own bespoke per-mix-type
// schema-migration system - schemaVersion is still stamped on write so a
// future migration has somewhere to hook in, but there is only one version
// so far, on both sides.
package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/mixes"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

const mixTemplateSchemaVersion = 1

var validMixTemplateTypes = map[string]bool{
	"artist": true, "album": true, "genre": true, "mood": true, "decade": true, "custom": true,
}

type MixTemplatesHandler struct {
	DB            *sql.DB
	PlexAuth      *auth.PlexClient
	Tmpl          *Templates
	Notifications *notifications.Store
	Queue         *actionqueue.Queue
	Mixes         *mixes.Service
}

func RegisterMixTemplates(r chi.Router, mw *auth.Middleware, h *MixTemplatesHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/mix-templates", h.list)
		r.Get("/mix-templates/list", h.listFragment)
		r.Post("/mix-templates", h.create)
		r.Get("/mix-templates/new", h.newForm)
		r.Get("/mix-templates/{id}", h.edit)
		r.Put("/mix-templates/{id}", h.update)
		r.Delete("/mix-templates/{id}", h.delete)
		r.Post("/mix-templates/{id}/generate", h.generate)
	})
}

// TemplateView adapts db.MixTemplate for the list/card template: TemplateList.tsx
// derives a "N tracks • N artists • ..." metadata line and a relative
// "used X ago"/"created X ago" label from the raw config JSON and unix
// timestamps client-side; since Go templates can't index into a
// map[string]any as fluently, that derivation is done once here instead of
// re-implementing getTemplateMetadata/formatDate in template syntax.
type TemplateView struct {
	db.MixTemplate
	Metadata  string
	DateLabel string

	// Fields the edit form (ports EditTemplateModal.tsx) needs typed access
	// to; Go templates can't index into the raw configuration JSON, so it's
	// unmarshaled once here rather than duplicating this per-field parsing
	// in template syntax.
	TrackCount            int
	SortBy                string
	Genres                []string
	Moods                 []string
	Decades               []float64
	AllowDuplicateArtists bool
	AllowDuplicateAlbums  bool
	MaxTracksPerArtist    int
	MaxTracksPerAlbum     int
	IncludeGenres         []string
	ExcludeGenres         []string
	MinRating             float64
}

func buildTemplateView(t db.MixTemplate) TemplateView {
	var cfg map[string]any
	_ = json.Unmarshal([]byte(t.Configuration), &cfg)

	parts := []string{fmt.Sprintf("%d tracks", int(cfgFloat(cfg, "trackCount", 0)))}
	if n := len(cfgStrings(cfg, "artistIds")); n > 0 {
		parts = append(parts, fmt.Sprintf("%d artists", n))
	}
	if n := len(cfgStrings(cfg, "albumIds")); n > 0 {
		parts = append(parts, fmt.Sprintf("%d albums", n))
	}
	if n := len(cfgStrings(cfg, "genres")); n > 0 {
		parts = append(parts, fmt.Sprintf("%d genres", n))
	}

	var dateLabel string
	if t.LastUsedAt.Valid {
		dateLabel = "Last used " + relativeTime(t.LastUsedAt.Int64)
	} else {
		dateLabel = "Created " + relativeTime(t.CreatedAt)
	}

	rules, _ := cfg["customRules"].(map[string]any)

	return TemplateView{
		MixTemplate:           t,
		Metadata:              strings.Join(parts, " • "),
		DateLabel:             dateLabel,
		TrackCount:            int(cfgFloat(cfg, "trackCount", 50)),
		SortBy:                cfgString(cfg, "sortBy", "random"),
		Genres:                cfgStrings(cfg, "genres"),
		Moods:                 cfgStrings(cfg, "moods"),
		Decades:               cfgFloats(cfg, "decades"),
		AllowDuplicateArtists: cfgBool(cfg, "allowDuplicateArtists"),
		AllowDuplicateAlbums:  cfgBool(cfg, "allowDuplicateAlbums"),
		MaxTracksPerArtist:    int(cfgFloat(cfg, "maxTracksPerArtist", 0)),
		MaxTracksPerAlbum:     int(cfgFloat(cfg, "maxTracksPerAlbum", 0)),
		IncludeGenres:         cfgStrings(rules, "includeGenres"),
		ExcludeGenres:         cfgStrings(rules, "excludeGenres"),
		MinRating:             cfgFloat(rules, "minRating", 0),
	}
}

func cfgBool(cfg map[string]any, key string) bool {
	if cfg == nil {
		return false
	}
	b, _ := cfg[key].(bool)
	return b
}

// relativeTime ports TemplateList.tsx's formatDate (Today/Yesterday/N days/
// weeks/months/years ago) from a unix-seconds timestamp.
func relativeTime(unix int64) string {
	days := int(time.Since(time.Unix(unix, 0)).Hours() / 24)
	switch {
	case days <= 0:
		return "Today"
	case days == 1:
		return "Yesterday"
	case days < 7:
		return fmt.Sprintf("%d days ago", days)
	case days < 30:
		return fmt.Sprintf("%d weeks ago", days/7)
	case days < 365:
		return fmt.Sprintf("%d months ago", days/30)
	default:
		return fmt.Sprintf("%d years ago", days/365)
	}
}

func buildTemplateViews(templates []db.MixTemplate) []TemplateView {
	views := make([]TemplateView, len(templates))
	for i, t := range templates {
		views[i] = buildTemplateView(t)
	}
	return views
}

func (h *MixTemplatesHandler) renderList(w http.ResponseWriter, userID int64) {
	templates, err := db.GetMixTemplates(h.DB, userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.Tmpl.RenderPartial(w, "partials/mix_templates_list.html", map[string]any{"Templates": buildTemplateViews(templates)})
}

func (h *MixTemplatesHandler) list(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	h.Tmpl.RenderPage(w, "mix_templates", map[string]any{"User": user})
}

// listFragment backs the page's hx-trigger="load" fetch of the saved-mixes
// grid - pages and partials are separate template sets (see
// Templates.LoadTemplates), so the page shell can't {{template}} a partial
// directly; it loads it via HTMX instead, same pattern as cross_import.html
// and missing.html use for their list sections.
func (h *MixTemplatesHandler) listFragment(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	h.renderList(w, user.ID)
}

// parseConfiguration validates the browser-submitted configuration JSON:
// must be a JSON object with a positive numeric trackCount, matching
// validateConfiguration()'s baseline check in the TS route (the type-
// specific array-field checks are left to whichever mix-type branch of
// MixesHandler.run ends up empty-handed at generate time, same as the TS
// generator functions' own "no artists/albums found" errors).
func parseConfiguration(raw string) (map[string]any, error) {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, errBadConfig
	}
	tc, ok := cfg["trackCount"].(float64)
	if !ok || tc <= 0 {
		return nil, errBadConfig
	}
	if _, ok := cfg["schemaVersion"]; !ok {
		cfg["schemaVersion"] = mixTemplateSchemaVersion
	}
	return cfg, nil
}

var errBadConfig = &configError{"Configuration must be a JSON object with a positive numeric trackCount"}

type configError struct{ msg string }

func (e *configError) Error() string { return e.msg }

func (h *MixTemplatesHandler) create(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	name := strings.TrimSpace(r.FormValue("name"))
	mixType := r.FormValue("mixType")
	description := r.FormValue("description")

	if name == "" || len(name) > 255 {
		http.Error(w, "Template name is required (max 255 characters)", http.StatusBadRequest)
		return
	}
	if !validMixTemplateTypes[mixType] {
		http.Error(w, "Invalid mix type", http.StatusBadRequest)
		return
	}
	cfg, err := parseConfiguration(r.FormValue("configuration"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfgJSON, _ := json.Marshal(cfg)

	var desc *string
	if description != "" {
		desc = &description
	}
	if _, err := db.CreateMixTemplate(h.DB, user.ID, name, desc, mixType, string(cfgJSON)); err != nil {
		http.Error(w, "Failed to create template", http.StatusInternalServerError)
		return
	}
	h.renderList(w, user.ID)
}

// newForm renders the empty "New Template" modal fragment (same
// modal-overlay/modal-content pattern as partials/mix_quick_settings_form.html),
// ports SaveTemplateModal.tsx - which only takes name+description because its
// caller already has a custom-mix config in hand. This standalone page has no
// such context, so the mixType+config fields SaveTemplateModal doesn't show
// are kept here, same as the previous version of this page.
func (h *MixTemplatesHandler) newForm(w http.ResponseWriter, r *http.Request) {
	h.Tmpl.RenderPartial(w, "partials/mix_template_new_form.html", nil)
}

func (h *MixTemplatesHandler) edit(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id := mustAtoi(chi.URLParam(r, "id"))
	tpl, err := db.GetMixTemplateByID(h.DB, id)
	if err != nil || tpl == nil || tpl.UserID != user.ID {
		http.Error(w, "Template not found", http.StatusNotFound)
		return
	}
	h.Tmpl.RenderPartial(w, "partials/mix_template_form.html", map[string]any{"Template": buildTemplateView(*tpl)})
}

func (h *MixTemplatesHandler) update(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id := mustAtoi(chi.URLParam(r, "id"))
	tpl, err := db.GetMixTemplateByID(h.DB, id)
	if err != nil || tpl == nil {
		http.Error(w, "Template not found", http.StatusNotFound)
		return
	}
	if tpl.UserID != user.ID {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	update := db.MixTemplateUpdate{}
	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		if len(name) > 255 {
			http.Error(w, "Template name must be 255 characters or less", http.StatusBadRequest)
			return
		}
		update.Name = &name
	}
	if r.Form.Has("description") {
		desc := r.FormValue("description")
		update.Description = &desc
	}
	if r.Form.Has("configuration") {
		cfg, err := parseConfiguration(r.FormValue("configuration"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfgJSON, _ := json.Marshal(cfg)
		s := string(cfgJSON)
		update.Configuration = &s
	} else if r.Form.Has("trackCount") {
		// The edit form (ports EditTemplateModal.tsx) submits granular
		// fields rather than a raw JSON blob; merge them onto the existing
		// configuration so fields the form doesn't expose (e.g. artistIds/
		// albumIds) survive the edit.
		var cfg map[string]any
		_ = json.Unmarshal([]byte(tpl.Configuration), &cfg)
		if cfg == nil {
			cfg = map[string]any{}
		}
		if tc, err := strconv.Atoi(r.FormValue("trackCount")); err == nil {
			cfg["trackCount"] = float64(tc)
		}
		cfg["sortBy"] = r.FormValue("sortBy")
		if r.Form.Has("genres") {
			cfg["genres"] = commaListToAny(r.FormValue("genres"))
		}
		if r.Form.Has("moods") {
			cfg["moods"] = commaListToAny(r.FormValue("moods"))
		}
		if r.Form.Has("decades") {
			cfg["decades"] = commaListToFloats(r.FormValue("decades"))
		}
		cfg["allowDuplicateArtists"] = r.Form.Has("allowDuplicateArtists")
		cfg["allowDuplicateAlbums"] = r.Form.Has("allowDuplicateAlbums")
		if v := r.FormValue("maxTracksPerArtist"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				cfg["maxTracksPerArtist"] = float64(n)
			}
		} else {
			delete(cfg, "maxTracksPerArtist")
		}
		if v := r.FormValue("maxTracksPerAlbum"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				cfg["maxTracksPerAlbum"] = float64(n)
			}
		} else {
			delete(cfg, "maxTracksPerAlbum")
		}
		if r.Form.Has("includeGenres") || r.Form.Has("excludeGenres") || r.Form.Has("minRating") {
			rules, _ := cfg["customRules"].(map[string]any)
			if rules == nil {
				rules = map[string]any{}
			}
			if r.Form.Has("includeGenres") {
				rules["includeGenres"] = commaListToAny(r.FormValue("includeGenres"))
			}
			if r.Form.Has("excludeGenres") {
				rules["excludeGenres"] = commaListToAny(r.FormValue("excludeGenres"))
			}
			if v := r.FormValue("minRating"); v != "" {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					rules["minRating"] = f
				}
			}
			cfg["customRules"] = rules
		}
		if tc, ok := cfg["trackCount"].(float64); !ok || tc <= 0 {
			http.Error(w, errBadConfig.Error(), http.StatusBadRequest)
			return
		}
		cfgJSON, _ := json.Marshal(cfg)
		s := string(cfgJSON)
		update.Configuration = &s
	}

	if err := db.UpdateMixTemplate(h.DB, id, update); err != nil {
		http.Error(w, "Failed to update template", http.StatusInternalServerError)
		return
	}
	h.renderList(w, user.ID)
}

func (h *MixTemplatesHandler) delete(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id := mustAtoi(chi.URLParam(r, "id"))
	tpl, err := db.GetMixTemplateByID(h.DB, id)
	if err != nil || tpl == nil || tpl.UserID != user.ID {
		http.Error(w, "Template not found", http.StatusNotFound)
		return
	}
	if err := db.DeleteMixTemplate(h.DB, id); err != nil {
		http.Error(w, "Failed to delete template", http.StatusInternalServerError)
		return
	}
	h.renderList(w, user.ID)
}

// generate ports generateMixFromTemplate (mix-templates.ts:415-800):
// dispatches on the template's mix_type to build the right track list
// (custom uses generateCustomMix's full filter surface via customRules;
// artist/album pull tracks directly by rating key; genre/mood/decade reuse
// generateCustomMix with the matching filter fields), then creates the
// resulting Plex playlist and bumps the template's usage stats. Runs
// through the actionqueue/notifications pattern same as MixesHandler.run.
func (h *MixTemplatesHandler) generate(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id := mustAtoi(chi.URLParam(r, "id"))
	tpl, err := db.GetMixTemplateByID(h.DB, id)
	if err != nil || tpl == nil || tpl.UserID != user.ID {
		http.Error(w, "Template not found", http.StatusNotFound)
		return
	}

	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "No music library selected. Please select a library first.", http.StatusBadRequest)
		return
	}

	var cfg map[string]any
	if err := json.Unmarshal([]byte(tpl.Configuration), &cfg); err != nil {
		http.Error(w, "Template configuration is invalid", http.StatusBadRequest)
		return
	}

	playlistName := r.FormValue("playlistName")
	if playlistName == "" {
		playlistName = tpl.Name
	}

	jobID, _ := h.Queue.Enqueue(user.ID, "Generating "+tpl.Name, notifications.TypeMix, func(notificationID string) error {
		return h.runTemplate(user.ID, userServer, tpl, cfg, playlistName, notificationID)
	})

	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{
		"Notifications": h.Notifications.List(user.ID),
		"JobID":         jobID,
	})
}

func (h *MixTemplatesHandler) runTemplate(userID int64, userServer *db.UserServer, tpl *db.MixTemplate, cfg map[string]any, playlistName, notificationID string) error {
	user, err := db.GetUserByID(h.DB, userID)
	if err != nil {
		return err
	}
	serverURL := userServer.ServerURL
	token := plex.ResolveToken(user.PlexToken, userServer.AccessToken.String)
	libraryID := userServer.LibraryID.String
	client := plex.NewClient(serverURL, token, h.PlexAuth.ClientID, "Playlist Lab")

	trackCount := int(cfgFloat(cfg, "trackCount", 50))
	var trackKeys []string

	switch tpl.MixType {
	case "custom":
		rules, _ := cfg["customRules"].(map[string]any)
		result, err := h.Mixes.GenerateCustomMix(serverURL, token, libraryID, mixes.CustomMixSettings{
			TrackCount:          trackCount,
			PlayedInLastDays:    int(cfgFloat(rules, "playedInLastDays", 0)),
			NotPlayedInLastDays: int(cfgFloat(rules, "notPlayedInLastDays", 0)),
			AddedInLastDays:     int(cfgFloat(rules, "addedInLastDays", 0)),
			ReleasedAfterYear:   int(cfgFloat(rules, "yearRangeMin", 0)),
			ReleasedBeforeYear:  int(cfgFloat(rules, "yearRangeMax", 0)),
			Genres:              cfgStrings(rules, "includeGenres"),
			ExcludeGenres:       cfgStrings(rules, "excludeGenres"),
			MinRating:           int(cfgFloat(rules, "minRating", 0)),
			MaxRating:           int(cfgFloat(rules, "maxRating", 0)),
			SortBy:              cfgString(cfg, "sortBy", "random"),
			SortDirection:       cfgString(cfg, "sortDirection", "desc"),
		}, nil)
		if err != nil {
			return err
		}
		trackKeys = result.TrackKeys

	case "artist":
		artistIDs := cfgStrings(cfg, "artistIds")
		if len(artistIDs) == 0 {
			return &configError{"None of the artists in this template exist in your library"}
		}
		perArtistLimit := ceilDivInt(trackCount, len(artistIDs))
		seen := map[string]bool{}
		for _, artistID := range artistIDs {
			tracks, err := client.GetArtistPopularTracks(libraryID, artistID, perArtistLimit)
			if err != nil {
				continue
			}
			for _, t := range tracks {
				if len(trackKeys) >= trackCount {
					break
				}
				if !seen[t.RatingKey] {
					trackKeys = append(trackKeys, t.RatingKey)
					seen[t.RatingKey] = true
				}
			}
		}

	case "album":
		albumIDs := cfgStrings(cfg, "albumIds")
		if len(albumIDs) == 0 {
			return &configError{"None of the albums in this template exist in your library"}
		}
		seen := map[string]bool{}
		for _, albumID := range albumIDs {
			tracks, err := client.GetAlbumTracks(albumID)
			if err != nil {
				continue
			}
			for _, t := range tracks {
				if len(trackKeys) >= trackCount {
					break
				}
				if !seen[t.RatingKey] {
					trackKeys = append(trackKeys, t.RatingKey)
					seen[t.RatingKey] = true
				}
			}
		}

	case "genre", "mood", "decade":
		settings := mixes.CustomMixSettings{TrackCount: trackCount, SortBy: cfgString(cfg, "sortBy", "random"), SortDirection: cfgString(cfg, "sortDirection", "desc")}
		switch tpl.MixType {
		case "genre":
			settings.Genres = cfgStrings(cfg, "genres")
		case "mood":
			settings.Moods = cfgStrings(cfg, "moods")
		case "decade":
			decades := cfgFloats(cfg, "decades")
			for _, d := range decades {
				min, max := int(d), int(d)+9
				settings.YearRanges = append(settings.YearRanges, mixes.YearRange{Min: &min, Max: &max})
			}
		}
		result, err := h.Mixes.GenerateCustomMix(serverURL, token, libraryID, settings, nil)
		if err != nil {
			return err
		}
		trackKeys = result.TrackKeys

	default:
		return &configError{"Unsupported mix type: " + tpl.MixType}
	}

	if len(trackKeys) == 0 {
		return &configError{"No tracks found matching template criteria. The items in this template may no longer exist in your library."}
	}

	trackURIs := make([]string, len(trackKeys))
	for i, key := range trackKeys {
		trackURIs[i] = client.BuildTrackURI(key, userServer.ServerClientID)
	}
	libraryURI := client.BuildLibraryURI(libraryID, userServer.ServerClientID)
	playlist, err := client.CreatePlaylist(playlistName, libraryURI, trackURIs)
	if err != nil {
		return err
	}
	if _, err := db.CreatePlaylistRow(h.DB, userID, playlist.RatingKey, playlistName, "template", ""); err != nil {
		return err
	}
	_ = db.UpdateMixTemplateUsage(h.DB, tpl.ID)

	status := notifications.StatusSuccess
	detail := "Created \"" + playlistName + "\""
	pct := 100
	h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail, Progress: &pct})
	return nil
}

func cfgFloat(cfg map[string]any, key string, def float64) float64 {
	if cfg == nil {
		return def
	}
	if v, ok := cfg[key].(float64); ok {
		return v
	}
	return def
}

func cfgString(cfg map[string]any, key, def string) string {
	if cfg == nil {
		return def
	}
	if v, ok := cfg[key].(string); ok {
		return v
	}
	return def
}

func cfgStrings(cfg map[string]any, key string) []string {
	if cfg == nil {
		return nil
	}
	raw, ok := cfg[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func cfgFloats(cfg map[string]any, key string) []float64 {
	if cfg == nil {
		return nil
	}
	raw, ok := cfg[key].([]any)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(raw))
	for _, v := range raw {
		if f, ok := v.(float64); ok {
			out = append(out, f)
		}
	}
	return out
}

// commaListToAny/commaListToFloats port the comma-separated-input parsing
// EditTemplateModal.tsx does inline (e.g. genres.split(',').map(trim)) for
// the plain <input> fields the HTMX form uses instead of a tag picker.
func commaListToAny(raw string) []any {
	var out []any
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func commaListToFloats(raw string) []any {
	var out []any
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				out = append(out, float64(n))
			}
		}
	}
	return out
}

func ceilDivInt(a, b int) int {
	if b <= 0 {
		return a
	}
	return (a + b - 1) / b
}

func mustAtoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
