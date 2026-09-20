// dynamic_collections.go is the WebUI for Dynamic Collection sets
// (DESIGN.md §11.11 / Kometa's "dynamic_collections" feature): one set
// definition expands into many real Collections rows via
// scheduler.GenerateDynamicCollections. Gated the same way Collections
// itself is (RequirePlexOwner) - same per-library-section, server-ownership
// scoped feature, not this app's own instance-admin flag. Reuses
// collections.go's library-picker helpers (serverLibraryGroup,
// encodeLibraryOption/decodeLibraryOption) verbatim since they live in the
// same package and do exactly what a set's own library picker needs too.
package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
	"github.com/drevilish/playlist-lab/internal/services/scheduler"
)

type DynamicCollectionsHandler struct {
	DB            *sql.DB
	PlexAuth      *auth.PlexClient
	Tmpl          *Templates
	Notifications *notifications.Store
	Queue         *actionqueue.Queue
}

func RegisterDynamicCollections(r chi.Router, mw *auth.Middleware, h *DynamicCollectionsHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Use(mw.RequirePlexOwner)
		r.Get("/dynamic-collections", h.list)
		r.Get("/dynamic-collections/list", h.listFragment)
		r.Get("/dynamic-collections/new", h.newForm)
		r.Post("/dynamic-collections", h.create)
		r.Get("/dynamic-collections/{id}", h.edit)
		r.Put("/dynamic-collections/{id}", h.update)
		r.Delete("/dynamic-collections/{id}", h.delete)
		r.Post("/dynamic-collections/{id}/run", h.run)
	})
}

func (h *DynamicCollectionsHandler) client(user *db.User, server *db.UserServer) *plex.Client {
	token := plex.ResolveToken(user.PlexToken, server.AccessToken.String)
	return plex.NewClient(server.ServerURL, token, h.PlexAuth.ClientID, "Playlist Lab")
}

// facetTypeLabels drives both the New/Edit form's <select> and the list's
// Type column - kept as one map so adding a facet type later only means
// touching fetchFacetKeys (scheduler package) and this one map.
var facetTypeLabels = map[string]string{
	"genre":          "Genre",
	"decade":         "Decade",
	"year":           "Year",
	"content_rating": "Content Rating (movie)",
	"studio":         "Studio / Network",
	"actor":          "Actor",
	"director":       "Director",
	"writer":         "Writer",
	"mood":           "Mood (music)",
	"style":          "Style (music)",
}

type DynamicCollectionView struct {
	db.DynamicCollection
	ServerName     string
	LibraryDisplay string
	TypeLabel      string
	GeneratedCount int
}

func (h *DynamicCollectionsHandler) buildView(d db.DynamicCollection, serverName, libraryDisplay string) DynamicCollectionView {
	count := 0
	if generated, err := db.GetGeneratedCollections(h.DB, d.ID); err == nil {
		count = len(generated)
	}
	label := facetTypeLabels[d.FacetType]
	if label == "" {
		label = d.FacetType
	}
	return DynamicCollectionView{DynamicCollection: d, ServerName: serverName, LibraryDisplay: libraryDisplay, TypeLabel: label, GeneratedCount: count}
}

func (h *DynamicCollectionsHandler) list(w http.ResponseWriter, r *http.Request) {
	h.Tmpl.RenderPage(w, r, "dynamic_collections", nil)
}

func (h *DynamicCollectionsHandler) listFragment(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	rows, err := db.GetUserDynamicCollections(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	namesByServer := map[int64]map[string]string{}
	serverNames := map[int64]string{}
	var multiServer bool
	if servers, err := db.GetUserServers(h.DB, user.ID); err == nil {
		owned := 0
		for _, s := range servers {
			if s.IsOwner {
				owned++
			}
		}
		multiServer = owned > 1
	}

	views := make([]DynamicCollectionView, 0, len(rows))
	for _, d := range rows {
		if _, ok := namesByServer[d.ServerID]; !ok {
			server, err := db.GetUserServerByID(h.DB, d.ServerID)
			if err != nil || server == nil {
				namesByServer[d.ServerID] = map[string]string{}
			} else {
				serverNames[d.ServerID] = server.ServerName
				namesByServer[d.ServerID] = h.libraryNamesForUser(user, server)
			}
		}
		libDisplay := d.LibraryType
		if name, ok := namesByServer[d.ServerID][d.LibrarySectionID]; ok {
			libDisplay = name
		}
		views = append(views, h.buildView(d, serverNames[d.ServerID], libDisplay))
	}

	h.Tmpl.RenderPartial(w, "partials/dynamic_collection_list.html", map[string]any{
		"Sets":        views,
		"MultiServer": multiServer,
	})
}

// libraryNamesForUser mirrors collections.go's own helper of the same name
// (best-effort: an unreachable server just falls back to raw ids/types).
func (h *DynamicCollectionsHandler) libraryNamesForUser(user *db.User, server *db.UserServer) map[string]string {
	names := map[string]string{}
	libs, err := h.client(user, server).GetLibraries()
	if err != nil {
		return names
	}
	for _, l := range libs {
		names[l.ID] = l.Name
	}
	return names
}

func (h *DynamicCollectionsHandler) newForm(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	servers, err := db.GetUserServers(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var groups []serverLibraryGroup
	for _, server := range servers {
		if !server.IsOwner {
			continue
		}
		libs, err := h.client(user, &server).GetLibraries()
		if err != nil {
			continue
		}
		options := make([]libraryPickerOption, len(libs))
		for i, l := range libs {
			options[i] = libraryPickerOption{Value: encodeLibraryOption(server.ID, l), Name: l.Name + " (" + l.Type + ")"}
		}
		groups = append(groups, serverLibraryGroup{ServerName: server.ServerName, Libraries: options})
	}

	// A "Browse Presets" pick (collection_presets.go) for a Library
	// Breakdown category just names a facet type + a friendly default name
	// - there's no external service or list URL involved, so pre-filling
	// is a one-field affair compared to the New Collection form's prefill.
	var prefillFacetType, prefillName string
	if slug := r.URL.Query().Get("preset"); slug != "" {
		if p := findCollectionPreset(slug); p != nil && p.Kind == "dynamic" {
			prefillFacetType, prefillName = p.FacetType, p.Name
		}
	}

	h.Tmpl.RenderPartial(w, "partials/dynamic_collection_form.html", map[string]any{
		"ServerGroups": groups, "FacetTypes": facetTypeLabels,
		"PrefillFacetType": prefillFacetType, "PrefillName": prefillName,
	})
}

// parseKeyListForm reads a newline/comma-separated textarea into a JSON
// []string, or "" when blank - matches how a user is most likely to paste a
// handful of genre names in, rather than a repeatable-row picker like
// Collections' own smart rule builder (DESIGN.md §11.11 uses that pattern
// for AND-combined fields; include/exclude here is just a flat set).
func parseKeyListForm(raw string) string {
	var keys []string
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == ',' }) {
		if v := strings.TrimSpace(line); v != "" {
			keys = append(keys, v)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	b, _ := json.Marshal(keys)
	return string(b)
}

func (h *DynamicCollectionsHandler) create(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 255 {
		http.Error(w, "Name is required (max 255 characters)", http.StatusBadRequest)
		return
	}
	lib, ok := decodeLibraryOption(r.FormValue("library"))
	if !ok {
		http.Error(w, "A library is required", http.StatusBadRequest)
		return
	}
	server, err := db.GetUserServerByID(h.DB, lib.ServerID)
	if err != nil || server == nil || server.UserID != user.ID || !server.IsOwner {
		http.Error(w, "Invalid server", http.StatusBadRequest)
		return
	}
	facetType := r.FormValue("facetType")
	if _, ok := facetTypeLabels[facetType]; !ok {
		http.Error(w, "Invalid type", http.StatusBadRequest)
		return
	}
	titleFormat := strings.TrimSpace(r.FormValue("titleFormat"))
	if titleFormat == "" {
		titleFormat = "<<key_name>>"
	}
	syncMode := r.FormValue("syncMode")
	if syncMode != "add_only" {
		syncMode = "sync"
	}
	includeJSON := parseKeyListForm(r.FormValue("include"))
	excludeJSON := ""
	if includeJSON == "" {
		excludeJSON = parseKeyListForm(r.FormValue("exclude"))
	}

	dyn, err := db.CreateDynamicCollection(h.DB, user.ID, server.ID, lib.ID, lib.Type, name, facetType, titleFormat, includeJSON, excludeJSON, syncMode)
	if err != nil {
		slog.Error("failed to create dynamic collection set", "error", err, "userId", user.ID, "name", name)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Create dynamic collection set", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to create set", http.StatusInternalServerError)
		return
	}

	h.enqueueGenerate(user, server, dyn, false)
	h.listFragment(w, r)
}

func (h *DynamicCollectionsHandler) ownedDynamicCollection(r *http.Request, id int64) (*db.DynamicCollection, error) {
	dyn, err := db.GetDynamicCollectionByID(h.DB, id)
	if err != nil || dyn == nil {
		return nil, err
	}
	if dyn.UserID != auth.CurrentUser(r).ID {
		return nil, nil
	}
	return dyn, nil
}

func (h *DynamicCollectionsHandler) edit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	dyn, err := h.ownedDynamicCollection(r, id)
	if err != nil || dyn == nil {
		http.Error(w, "Set not found", http.StatusNotFound)
		return
	}
	server, err := db.GetUserServerByID(h.DB, dyn.ServerID)
	if err != nil || server == nil {
		http.Error(w, "no Plex server configured", http.StatusInternalServerError)
		return
	}
	names := h.libraryNamesForUser(auth.CurrentUser(r), server)
	h.Tmpl.RenderPartial(w, "partials/dynamic_collection_form.html", map[string]any{
		"Set":        h.buildView(*dyn, server.ServerName, names[dyn.LibrarySectionID]),
		"FacetTypes": facetTypeLabels,
	})
}

func (h *DynamicCollectionsHandler) update(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	dyn, err := h.ownedDynamicCollection(r, id)
	if err != nil || dyn == nil {
		http.Error(w, "Set not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	update := db.DynamicCollectionUpdate{}
	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		if len(name) > 255 {
			http.Error(w, "Name must be 255 characters or less", http.StatusBadRequest)
			return
		}
		update.Name = &name
	}
	if r.Form.Has("titleFormat") {
		titleFormat := strings.TrimSpace(r.FormValue("titleFormat"))
		if titleFormat == "" {
			titleFormat = "<<key_name>>"
		}
		update.TitleFormat = &titleFormat
	}
	if syncMode := r.FormValue("syncMode"); syncMode == "sync" || syncMode == "add_only" {
		update.SyncMode = &syncMode
	}
	if r.Form.Has("include") || r.Form.Has("exclude") {
		includeJSON := parseKeyListForm(r.FormValue("include"))
		excludeJSON := ""
		if includeJSON == "" {
			excludeJSON = parseKeyListForm(r.FormValue("exclude"))
		}
		update.IncludeKeys = &includeJSON
		update.ExcludeKeys = &excludeJSON
	}

	if err := db.UpdateDynamicCollection(h.DB, id, update); err != nil {
		slog.Error("failed to update dynamic collection set", "error", err, "setId", id)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Update dynamic collection set", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to update set", http.StatusInternalServerError)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Update dynamic collection set", dyn.Name, notifications.StatusSuccess, nil)
	h.listFragment(w, r)
}

func (h *DynamicCollectionsHandler) delete(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	dyn, err := h.ownedDynamicCollection(r, id)
	if err != nil || dyn == nil {
		http.Error(w, "Set not found", http.StatusNotFound)
		return
	}
	if r.FormValue("deleteInPlex") == "true" {
		if server, err := db.GetUserServerByID(h.DB, dyn.ServerID); err == nil && server != nil {
			if generated, err := db.GetGeneratedCollections(h.DB, dyn.ID); err == nil {
				client := h.client(user, server)
				for _, c := range generated {
					if c.PlexCollectionID.Valid {
						if err := client.DeleteCollection(c.PlexCollectionID.String); err != nil {
							slog.Error("failed to delete generated collection from Plex", "error", err, "setId", id, "plexId", c.PlexCollectionID.String)
						}
					}
				}
			}
		}
	}
	// ON DELETE CASCADE (schema.sql) removes every generated collections
	// row for this set automatically - only the optional Plex-side sweep
	// above needs doing by hand.
	if err := db.DeleteDynamicCollection(h.DB, id); err != nil {
		slog.Error("failed to delete dynamic collection set", "error", err, "setId", id)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Delete dynamic collection set", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to delete set", http.StatusInternalServerError)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Delete dynamic collection set", dyn.Name, notifications.StatusSuccess, nil)
	h.listFragment(w, r)
}

func (h *DynamicCollectionsHandler) run(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	dyn, err := h.ownedDynamicCollection(r, id)
	if err != nil || dyn == nil {
		http.Error(w, "Set not found", http.StatusNotFound)
		return
	}
	server, err := db.GetUserServerByID(h.DB, dyn.ServerID)
	if err != nil || server == nil {
		http.Error(w, "no Plex server configured", http.StatusInternalServerError)
		return
	}
	h.enqueueGenerate(user, server, dyn, false)
	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{
		"Notifications": h.Notifications.List(user.ID),
	})
}

// enqueueGenerate runs GenerateDynamicCollections on the background action
// queue - a full set can mean dozens of Plex round trips (one RefreshCollection
// per facet key), same reasoning collections.go's own run handler already
// applies to a single collection's refresh.
func (h *DynamicCollectionsHandler) enqueueGenerate(user *db.User, server *db.UserServer, dyn *db.DynamicCollection, deleteStaleFromPlex bool) {
	_, _ = h.Queue.Enqueue(user.ID, "Generating "+dyn.Name, notifications.TypeSchedule, func(notificationID string) error {
		client := h.client(user, server)
		created, refreshed, removed, errs := scheduler.GenerateDynamicCollections(h.DB, client, dyn, server, deleteStaleFromPlex)
		status, detail := notifications.StatusSuccess, ""
		switch {
		case len(errs) > 0 && created+refreshed == 0:
			status = notifications.StatusError
			detail = errs[0].Error()
		case len(errs) > 0:
			detail = fmt.Sprintf("Generated %d, refreshed %d, removed %d - %d key(s) failed, see logs", created, refreshed, removed, len(errs))
		default:
			detail = fmt.Sprintf("Generated %d, refreshed %d, removed %d", created, refreshed, removed)
		}
		pct := 100
		h.Notifications.Update(user.ID, notificationID, notifications.Patch{Status: &status, Detail: &detail, Progress: &pct})
		if len(errs) > 0 && created+refreshed == 0 {
			return errs[0]
		}
		return nil
	})
}
