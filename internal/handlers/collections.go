// collections.go ports Kometa-style Plex Collection management (DESIGN.md
// §11.11): rule-based ("smart") or manual collections across any Plex
// library type, refreshed via the scheduler package's shared
// RefreshCollection core. Gated to the real Plex server owner (see
// auth.Middleware.RequirePlexOwner) - not this app's own admin flag - since
// a Plex Collection is a per-library-section concept and this feature is
// scoped to the account that actually owns the server, per the user's
// explicit direction.
//
// Unlike the ~40 music-only features elsewhere in this app (which resolve
// to a user's single default server via db.GetUserMusicServer - see
// internal/db/servers.go), a collection's server is a real, per-collection
// choice: a user may link several Plex servers (e.g. one for movies/TV,
// one for music), and newForm's library picker spans all of them the user
// owns. Each collection stores its own server_id (db.Collection.ServerID)
// rather than ever implicitly assuming "the" server. builder_type is
// immutable after creation to avoid ambiguous smart<->manual transitions
// mid-edit; a manual collection's item list is managed via the
// search-and-click endpoints below rather than through the main update
// form.
package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
	"github.com/drevilish/playlist-lab/internal/services/scheduler"
)

type CollectionsHandler struct {
	DB            *sql.DB
	PlexAuth      *auth.PlexClient
	Tmpl          *Templates
	Notifications *notifications.Store
	Queue         *actionqueue.Queue
	SchedulerDeps scheduler.Deps
}

func RegisterCollections(r chi.Router, mw *auth.Middleware, h *CollectionsHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Use(mw.RequirePlexOwner)
		r.Get("/collections", h.list)
		r.Get("/collections/list", h.listFragment)
		r.Get("/collections/new", h.newForm)
		r.Get("/collections/presets", h.presetsGallery)
		r.Get("/collections/presets/search", h.searchPresets)
		r.Get("/collections/presets/{slug}/years", h.presetYearsForm)
		r.Post("/collections/presets/{slug}/years", h.createPresetYears)
		r.Post("/collections", h.create)
		r.Post("/collections/bulk-delete", h.bulkDelete)
		r.Post("/collections/bulk-run", h.bulkRun)
		r.Get("/collections/{id}", h.edit)
		r.Put("/collections/{id}", h.update)
		r.Delete("/collections/{id}", h.delete)
		r.Post("/collections/{id}/run", h.run)
		r.Get("/collections/{id}/search", h.searchItems)
		r.Post("/collections/{id}/poster", h.setPoster)
		r.Get("/collections/{id}/missing", h.missingListForCollection)
		r.Get("/collections/{id}/items", h.itemsPreview)
		r.Delete("/collections/missing/{id}", h.removeMissingItem)
		r.Post("/collections/missing/bulk-remove", h.bulkRemoveMissingItems)
		r.Delete("/collections/unmanaged", h.deleteUnmanaged)
		r.Post("/collections/{id}/manual-items", h.addManualItem)
		r.Delete("/collections/{id}/manual-items/{ratingKey}", h.removeManualItem)
	})
}

func (h *CollectionsHandler) client(user *db.User, server *db.UserServer) *plex.Client {
	token := plex.ResolveToken(user.PlexToken, server.AccessToken.String)
	return plex.NewClient(server.ServerURL, token, h.PlexAuth.ClientID, "Playlist Lab")
}

// libraryOption is the compound "serverID|id|type|name" value packed into
// the library <select> - the library's server, not just its type, has to
// round-trip with it now that a user may have linked more than one Plex
// server (DESIGN.md §11.11): a collection's server is a real choice, not
// implicit.
type libraryOption struct {
	ServerID       int64
	ID, Type, Name string
}

func encodeLibraryOption(serverID int64, l plex.Library) string {
	return strings.Join([]string{strconv.FormatInt(serverID, 10), l.ID, l.Type, l.Name}, "|")
}

func decodeLibraryOption(v string) (libraryOption, bool) {
	parts := strings.SplitN(v, "|", 4)
	if len(parts) != 4 {
		return libraryOption{}, false
	}
	serverID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return libraryOption{}, false
	}
	return libraryOption{ServerID: serverID, ID: parts[1], Type: parts[2], Name: parts[3]}, true
}

// CollectionView adds display-only fields db.Collection doesn't carry -
// Go templates can't call methods with arguments, so the rule summary and
// library display name are precomputed here.
type CollectionView struct {
	db.Collection
	ServerName     string
	LibraryDisplay string
	RulesSummary   string
	ProviderLabel  string // set only when BuilderType == "external_list"
	SourceLink     string // set only when BuilderType == "external_list" - the actual TMDb/IMDb/TVDb/Letterboxd list/chart URL
	IsSmart        bool   // Plex's own native smart-collection flag (unmanaged rows only - a managed row's "smart" is this app's own rule builder, tracked via BuilderType instead)
	MissingCount   int
	ItemCount      int

	// Schedule fields, DESIGN.md §11.11 - mirrors home.html's Playlists
	// table Schedule/Next Run/Last Run columns (playlists.go's
	// nextRunRelative - NextRunAt is its second return value, previously
	// discarded here, needed to sort by next run rather than just display it).
	ScheduleID    int64
	Frequency     string
	NextRun       string
	NextRunAt     int64
	LastRun       int64
	LastRunStatus string // "success"/"failed"/"running"; empty if never run - mirrors playlists.go's playlistRow.LastRunStatus
}

func (h *CollectionsHandler) buildView(c db.Collection, serverName string, libraryNames map[string]string, missingCount int, latestStatus map[int64]db.LatestExecution) CollectionView {
	v := CollectionView{Collection: c, ServerName: serverName, LibraryDisplay: c.LibraryType, MissingCount: missingCount}
	if name, ok := libraryNames[c.LibrarySectionID]; ok {
		v.LibraryDisplay = name
	}
	switch c.BuilderType {
	case "manual":
		v.RulesSummary = strconv.Itoa(len(c.ParsedManualItems())) + " item(s)"
	case "external_list":
		src := c.ParsedExternalListSource()
		label := providerLabel(src.Provider)
		v.ProviderLabel = label
		v.SourceLink = sourceLinkFor(src, c.LibraryType)
		switch src.Mode {
		case "popular":
			v.RulesSummary = label + ": Popular"
		case "top_rated":
			v.RulesSummary = label + ": Top Rated"
		case "trending_daily":
			v.RulesSummary = label + ": Trending (Daily)"
		case "trending_weekly":
			v.RulesSummary = label + ": Trending (Weekly)"
		case "collection":
			v.RulesSummary = label + ": Collection " + src.ListID
		case "top250":
			v.RulesSummary = label + ": Top 250"
		default:
			v.RulesSummary = label + ": List " + src.ListID
		}
	default:
		rules := c.ParsedRules()
		parts := make([]string, len(rules))
		for i, r := range rules {
			parts[i] = r.Field + "=" + r.Value
		}
		v.RulesSummary = strings.Join(parts, ", ")
	}
	if s, err := db.GetScheduleByCollectionID(h.DB, c.ID); err == nil && s != nil {
		v.ScheduleID = s.ID
		v.Frequency = s.Frequency
		v.NextRun, v.NextRunAt = nextRunRelative(*s)
		v.LastRun = s.LastRun.Int64
		v.LastRunStatus = latestStatus[s.ID].Status
	}
	return v
}

// libraryNamesForUser fetches the user's current library list once, keyed
// by section id, to label each collection row with something friendlier
// than its raw stored library_type - best-effort: a collection whose
// library no longer exists just falls back to showing its stored type.
func (h *CollectionsHandler) libraryNamesForUser(user *db.User, server *db.UserServer) map[string]string {
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

func (h *CollectionsHandler) renderList(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	filter := parseCollectionFilter(r)
	rows, err := db.GetUserCollections(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	missingByCollection := map[int64]int{}
	if missing, err := db.GetUserMissingCollectionItems(h.DB, user.ID); err == nil {
		for _, m := range missing {
			missingByCollection[m.CollectionID]++
		}
	}
	// Same lookup playlists.go uses for its own Last Run status column/filter
	// (DESIGN.md §11.11 mirrors §11.1 here) - schedule_executions is shared
	// across playlist and collection schedules alike, keyed by schedule ID.
	latestStatus, err := db.GetLatestExecutionStatuses(h.DB, user.ID)
	if err != nil {
		latestStatus = map[int64]db.LatestExecution{}
	}

	// Each collection carries its own server - resolve every distinct
	// server referenced exactly once (not one default server for every
	// row), avoiding an N+1 GetLibraries() call when several collections
	// share a server.
	namesByServer := map[int64]map[string]string{}
	serverNames := map[int64]string{}
	for _, c := range rows {
		if _, ok := namesByServer[c.ServerID]; ok {
			continue
		}
		server, err := db.GetUserServerByID(h.DB, c.ServerID)
		if err != nil || server == nil {
			namesByServer[c.ServerID] = map[string]string{}
			continue
		}
		serverNames[c.ServerID] = server.ServerName
		namesByServer[c.ServerID] = h.libraryNamesForUser(user, server)
	}

	dbRowsByPlexID := map[string]bool{}
	views := make([]CollectionView, 0, len(rows))
	for _, c := range rows {
		if c.PlexCollectionID.Valid && c.PlexCollectionID.String != "" {
			dbRowsByPlexID[c.PlexCollectionID.String] = true
		}
		views = append(views, h.buildView(c, serverNames[c.ServerID], namesByServer[c.ServerID], missingByCollection[c.ID], latestStatus))
	}

	// Also show every real Plex collection this app didn't create -
	// DESIGN.md §11.11: the list is meant to reflect what's actually on
	// the Plex server, not just this app's own tracked rows (e.g. a
	// collection someone made by hand in Plex, or via Kometa itself).
	unmanaged, ownedServers := h.unmanagedCollectionViews(user, dbRowsByPlexID)
	views = append(views, unmanaged...)

	// Distinct libraries across every row (managed + unmanaged), for the
	// Library column's filter dropdown - collected before filtering so the
	// dropdown always offers every option, not just ones still visible
	// under the currently-active filter.
	libSeen := map[string]bool{}
	var libraryOptions []string
	for _, v := range views {
		if v.LibraryDisplay != "" && !libSeen[v.LibraryDisplay] {
			libSeen[v.LibraryDisplay] = true
			libraryOptions = append(libraryOptions, v.LibraryDisplay)
		}
	}
	sort.Strings(libraryOptions)

	srvSeen := map[string]bool{}
	var serverOptions []string
	for _, v := range views {
		if v.ServerName != "" && !srvSeen[v.ServerName] {
			srvSeen[v.ServerName] = true
			serverOptions = append(serverOptions, v.ServerName)
		}
	}
	sort.Strings(serverOptions)

	views = filterCollectionViews(views, filter)

	q := r.URL.Query()
	sortKey, dir := q.Get("sort"), q.Get("dir")
	if sortKey == "" {
		// Matches GetUserCollections' own default order (created_at DESC) -
		// picking a sort default that agrees with the unsorted case means
		// "no sort clicked yet" doesn't visibly reorder anything.
		sortKey, dir = "dateAdded", "desc"
	}
	sortCollectionViews(views, sortKey, dir)

	h.Tmpl.RenderPartial(w, "partials/collection_list.html", map[string]any{
		"Collections": views,
		"MultiServer": len(ownedServers) > 1,
		"Query":       collectionsQueryState(r.URL.Query()),
		"Filter":      filter,
		"Libraries":   libraryOptions,
		"Servers":     serverOptions,
		"Sort":        sortKey,
		"Dir":         dir,
	})
}

// collectionFilter mirrors playlists.go's rowFilter for the Collections
// table's column-header filter popovers (DESIGN.md §11.11) - same
// query-string-round-trips-to-the-server mechanism, scoped to the handful
// of columns that make sense to filter here.
// collectionsQueryState is playlists.go's queryState adapted to the
// Collections page's lazy-fragment architecture - it links back to GET
// /collections/list (an htmx fragment target), not the "/" full page
// queryState.With hardcodes, since /collections itself renders an empty
// shell that loads its data separately.
type collectionsQueryState url.Values

func (q collectionsQueryState) With(key, val string) string {
	v := url.Values{}
	for k, vals := range q {
		v[k] = append([]string(nil), vals...)
	}
	if val == "" {
		v.Del(key)
	} else {
		v.Set(key, val)
	}
	return "/collections/list?" + v.Encode()
}

// Toggle is With's multi-select sibling: Server/Library can have more than
// one active value at once (DESIGN.md §11.11 - a user with several servers/
// libraries wants "show these two libraries together," not one at a time),
// so a click adds val to that param's current list if absent, or removes it
// if already present, rather than replacing the whole value.
func (q collectionsQueryState) Toggle(key, val string) string {
	v := url.Values{}
	for k, vals := range q {
		v[k] = append([]string(nil), vals...)
	}
	current := v[key]
	next := current[:0:0]
	found := false
	for _, c := range current {
		if c == val {
			found = true
			continue
		}
		next = append(next, c)
	}
	if !found {
		next = append(next, val)
	}
	if len(next) == 0 {
		v.Del(key)
	} else {
		v[key] = next
	}
	return "/collections/list?" + v.Encode()
}

// SortHref mirrors playlists.go's queryState.SortHref (toggle asc/desc on
// the active column, default to asc on a newly-clicked one), filter-
// preserving and pointed at the fragment endpoint like With/Toggle above.
func (q collectionsQueryState) SortHref(key string) string {
	dir := "asc"
	if url.Values(q).Get("sort") == key && url.Values(q).Get("dir") == "asc" {
		dir = "desc"
	}
	v := url.Values{}
	for k, vals := range q {
		v[k] = append([]string(nil), vals...)
	}
	v.Set("sort", key)
	v.Set("dir", dir)
	return "/collections/list?" + v.Encode()
}

// SetBuilder and SetProvider clear each other (DESIGN.md §11.11's Builder
// filter): a Smart/Manual/External List/Chart choice and a specific
// provider (TMDb/IMDb/TVDb/Letterboxd) are two different ways of narrowing
// the same column, and combining them (e.g. Smart AND IMDb) could never
// match a real row - clicking one always resets the other rather than
// letting them silently AND together into a dead end.
func (q collectionsQueryState) SetBuilder(val string) string {
	v := url.Values{}
	for k, vals := range q {
		v[k] = append([]string(nil), vals...)
	}
	v.Del("fProvider")
	if val == "" {
		v.Del("fBuilder")
	} else {
		v.Set("fBuilder", val)
	}
	return "/collections/list?" + v.Encode()
}

func (q collectionsQueryState) SetProvider(val string) string {
	v := url.Values{}
	for k, vals := range q {
		v[k] = append([]string(nil), vals...)
	}
	v.Del("fBuilder")
	if val == "" {
		v.Del("fProvider")
	} else {
		v.Set("fProvider", val)
	}
	return "/collections/list?" + v.Encode()
}

type collectionFilter struct {
	Search      string   // matches collection name, case-insensitive
	Library     []string // any-of LibraryDisplay match; empty = any
	Builder     string   // "smart" | "manual" | "external_list" | ""=any (unmanaged rows never match a specific value)
	Provider    string   // "tmdb" | "imdb" | "tvdb" | "letterboxd"; "" = any - narrows external_list rows further than Builder alone can
	Server      []string // any-of ServerName match; empty = any
	MediaType   string   // Plex's own "movie" | "show" (LibraryType); ""=any - Collections never targets music libraries, so this is the only pair
	Missing     string   // "has" | "none"; "" = any - mirrors playlists.go's rowFilter
	Schedule    string   // "on" | "off"; "" = any
	NextRun     string   // "due" | "upcoming" | "none"; "" = any
	LastRun     string   // "success" | "failed" | "never"; "" = any
	AddedAfter  string   // yyyy-mm-dd, inclusive; "" = unbounded
	AddedBefore string   // yyyy-mm-dd, inclusive; "" = unbounded
}

func (f collectionFilter) Active() bool {
	return f.Search != "" || len(f.Library) > 0 || f.Builder != "" || f.Provider != "" || len(f.Server) > 0 || f.MediaType != "" ||
		f.Missing != "" || f.Schedule != "" || f.NextRun != "" || f.LastRun != "" || f.AddedAfter != "" || f.AddedBefore != ""
}

func parseCollectionFilter(r *http.Request) collectionFilter {
	q := r.URL.Query()
	return collectionFilter{
		Search:      strings.TrimSpace(q.Get("q")),
		Library:     q["fLibrary"],
		Builder:     q.Get("fBuilder"),
		Provider:    q.Get("fProvider"),
		Server:      q["fServer"],
		MediaType:   q.Get("fType"),
		Missing:     q.Get("fMissing"),
		Schedule:    q.Get("fSchedule"),
		NextRun:     q.Get("fNextRun"),
		LastRun:     q.Get("fLastRun"),
		AddedAfter:  q.Get("fAddedAfter"),
		AddedBefore: q.Get("fAddedBefore"),
	}
}

func filterCollectionViews(views []CollectionView, f collectionFilter) []CollectionView {
	if !f.Active() {
		return views
	}
	search := strings.ToLower(f.Search)
	out := views[:0:0]
	for _, v := range views {
		if search != "" && !strings.Contains(strings.ToLower(v.Name), search) {
			continue
		}
		if len(f.Library) > 0 && !slices.Contains(f.Library, v.LibraryDisplay) {
			continue
		}
		if len(f.Server) > 0 && !slices.Contains(f.Server, v.ServerName) {
			continue
		}
		if f.MediaType != "" && v.LibraryType != f.MediaType {
			continue
		}
		if f.Provider != "" && (v.BuilderType != "external_list" || !strings.EqualFold(v.ProviderLabel, providerLabel(f.Provider))) {
			continue
		}
		switch f.Builder {
		case "":
			// any
		case "unmanaged":
			if v.ID != 0 {
				continue
			}
		default:
			if v.BuilderType != f.Builder {
				continue
			}
		}
		switch f.Missing {
		case "has":
			if v.MissingCount == 0 {
				continue
			}
		case "none":
			if v.MissingCount > 0 {
				continue
			}
		}
		switch f.Schedule {
		case "on":
			if v.ScheduleID == 0 {
				continue
			}
		case "off":
			if v.ScheduleID != 0 {
				continue
			}
		}
		switch f.NextRun {
		case "due":
			if v.NextRunAt == 0 || v.NextRunAt > time.Now().Unix() {
				continue
			}
		case "upcoming":
			if v.NextRunAt == 0 || v.NextRunAt <= time.Now().Unix() {
				continue
			}
		case "none":
			if v.NextRunAt != 0 {
				continue
			}
		}
		switch f.LastRun {
		case "success", "failed":
			if v.LastRunStatus != f.LastRun {
				continue
			}
		case "never":
			if v.LastRun != 0 {
				continue
			}
		}
		if t, ok := parseDateFilter(f.AddedAfter); ok && v.CreatedAt < t.Unix() {
			continue
		}
		if t, ok := parseDateFilter(f.AddedBefore); ok && v.CreatedAt >= t.AddDate(0, 0, 1).Unix() {
			continue
		}
		out = append(out, v)
	}
	return out
}

// sortCollectionViews applies the table's column sort (DESIGN.md §11.11),
// mirroring playlists.go's sortRows: server-side, one render per request,
// rather than client-side table state. An unmanaged row has no local
// schedule/timestamps (all zero), so it naturally sorts to whichever end
// those zero values belong on for a given key - not special-cased further,
// same tolerance the rest of this file already has for unmanaged rows.
func sortCollectionViews(views []CollectionView, sortKey, dir string) {
	if sortKey == "" {
		return
	}
	less := func(i, j CollectionView) bool {
		switch sortKey {
		case "missing":
			return i.MissingCount < j.MissingCount
		case "lastRun":
			return i.LastRun < j.LastRun
		case "nextRun":
			return i.NextRunAt < j.NextRunAt
		case "updatedAt":
			return i.UpdatedAt < j.UpdatedAt
		case "dateAdded":
			return i.CreatedAt < j.CreatedAt
		default: // "name"
			return strings.ToLower(i.Name) < strings.ToLower(j.Name)
		}
	}
	sort.SliceStable(views, func(i, j int) bool {
		if dir == "desc" {
			return less(views[j], views[i])
		}
		return less(views[i], views[j])
	})
}

// unmanagedCollectionViews sweeps every server the user owns for real Plex
// collections with no matching Playlist Lab row (DESIGN.md §11.11) - these
// render read-only (no edit/refresh/schedule, since there's no local
// definition to act on), just a "Remove from Plex" delete. Also returns
// the owned-server list so renderList's MultiServer flag reflects every
// server the user could see a collection on, not just ones a DB row
// happens to already reference. Best-effort: an unreachable server is
// skipped rather than failing the whole list, same tolerance newForm's
// library picker already has.
func (h *CollectionsHandler) unmanagedCollectionViews(user *db.User, dbRowsByPlexID map[string]bool) ([]CollectionView, []db.UserServer) {
	servers, err := db.GetUserServers(h.DB, user.ID)
	if err != nil {
		return nil, nil
	}
	var owned []db.UserServer
	var views []CollectionView
	for _, server := range servers {
		if !server.IsOwner {
			continue
		}
		owned = append(owned, server)
		client := h.client(user, &server)
		libs, err := client.GetLibraries()
		if err != nil {
			continue
		}
		for _, lib := range libs {
			plexColls, err := client.GetCollections(lib.ID)
			if err != nil {
				continue
			}
			for _, pc := range plexColls {
				if dbRowsByPlexID[pc.RatingKey] {
					continue
				}
				views = append(views, unmanagedCollectionView(server, lib, pc))
			}
		}
	}
	return views, owned
}

func unmanagedCollectionView(server db.UserServer, lib plex.Library, pc plex.Collection) CollectionView {
	return CollectionView{
		Collection: db.Collection{
			ServerID:         server.ID,
			LibrarySectionID: lib.ID,
			LibraryType:      lib.Type,
			PlexCollectionID: sql.NullString{String: pc.RatingKey, Valid: true},
			Name:             pc.Title,
		},
		ServerName:     server.ServerName,
		LibraryDisplay: lib.Name,
		RulesSummary:   strconv.Itoa(pc.ChildCount) + " item(s)",
		IsSmart:        bool(pc.Smart),
	}
}

func (h *CollectionsHandler) list(w http.ResponseWriter, r *http.Request) {
	h.Tmpl.RenderPage(w, r, "collections", nil)
}

func (h *CollectionsHandler) listFragment(w http.ResponseWriter, r *http.Request) {
	h.renderList(w, r)
}

// serverLibraryGroup is one <optgroup> of a server's libraries in the
// "new collection" library picker (DESIGN.md §11.11) - a user with several
// linked servers (e.g. one for movies/TV, one for music) picks a library
// from any of them, grouped by server so it's clear which is which.
type serverLibraryGroup struct {
	ServerName string
	Libraries  []libraryPickerOption
}

type libraryPickerOption struct{ Value, Name string }

// collectionPrefill carries a chosen preset's values into the otherwise-
// blank New Collection form (collection_form.html) - only populated when
// ?preset= names a "list"-kind entry from the Browse Presets gallery
// (collection_presets.go); a "dynamic"-kind preset targets the New Dynamic
// Collection Set form instead (dynamic_collections.go's newForm), never
// this one.
type collectionPrefill struct {
	Name             string
	ExternalProvider string
	ExternalMode     string
	ExternalListID   string
	MediaType        string // "movie" | "tv" | "" (unknown) - see collectionPreset.MediaType; drives newForm's library auto-select
}

// wantedPlexLibraryType maps a collectionPrefill's "movie"/"tv" media type to
// the Plex library type it actually appears as (plex.Library.Type) - Plex
// itself calls a TV library "show", not "tv".
func wantedPlexLibraryType(mediaType string) string {
	switch mediaType {
	case "movie":
		return "movie"
	case "tv":
		return "show"
	default:
		return ""
	}
}

func (h *CollectionsHandler) newForm(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	servers, err := db.GetUserServers(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	prefill := prefillFromRequest(r)
	wantType := ""
	if prefill != nil {
		wantType = wantedPlexLibraryType(prefill.MediaType)
	}

	var groups []serverLibraryGroup
	var recommended string
	for _, server := range servers {
		if !server.IsOwner {
			continue
		}
		libs, err := h.client(user, &server).GetLibraries()
		if err != nil {
			// Unreachable server - skip it rather than failing the whole
			// form; the other server(s) may still be usable.
			continue
		}
		options := make([]libraryPickerOption, len(libs))
		for i, l := range libs {
			value := encodeLibraryOption(server.ID, l)
			options[i] = libraryPickerOption{Value: value, Name: l.Name + " (" + l.Type + ")"}
			// First matching library wins - good enough default for the
			// common case of one movie/one show library; the picker is
			// still a plain <select> a user can just change if they have
			// several and want a different one (DESIGN.md §11.11).
			if recommended == "" && wantType != "" && l.Type == wantType {
				recommended = value
			}
		}
		groups = append(groups, serverLibraryGroup{ServerName: server.ServerName, Libraries: options})
	}

	h.Tmpl.RenderPartial(w, "partials/collection_form.html", map[string]any{
		"ServerGroups": groups, "Prefill": prefill, "RecommendedLibrary": recommended,
	})
}

// presetsGallery renders the Browse Presets modal (collection_presets.go) -
// shared between the Collections and Dynamic Collections pages (both link
// here), since a preset itself already knows which of the two create forms
// it targets.
func (h *CollectionsHandler) presetsGallery(w http.ResponseWriter, r *http.Request) {
	h.Tmpl.RenderPartial(w, "partials/collection_presets.html", map[string]any{
		"Categories": presetsByCategory(),
	})
}

// maxPresetYearRange caps a single "All Years" bulk-create (below) - a
// year-parameterized preset like Oscars realistically spans a ceremony
// history of a few decades, not centuries; this is a sanity guard against a
// typo (e.g. swapped from/to) turning into a several-hundred-row mistake,
// not a real usage ceiling.
const maxPresetYearRange = 75

// presetYearsForm renders the small "create every year at once" modal for
// one year-parameterized preset (collection_presets.go's NeedsYear entries -
// the direct answer to wanting a whole award show's history as individual
// collections in one action instead of picking one year at a time).
func (h *CollectionsHandler) presetYearsForm(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	preset := findCollectionPreset(slug)
	if preset == nil || !preset.NeedsYear {
		http.Error(w, "Preset not found", http.StatusNotFound)
		return
	}
	user := auth.CurrentUser(r)
	servers, err := db.GetUserServers(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	wantType := wantedPlexLibraryType(preset.MediaType)
	var groups []serverLibraryGroup
	var recommended string
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
			value := encodeLibraryOption(server.ID, l)
			options[i] = libraryPickerOption{Value: value, Name: l.Name + " (" + l.Type + ")"}
			if recommended == "" && wantType != "" && l.Type == wantType {
				recommended = value
			}
		}
		groups = append(groups, serverLibraryGroup{ServerName: server.ServerName, Libraries: options})
	}
	h.Tmpl.RenderPartial(w, "partials/collection_preset_years_form.html", map[string]any{
		"Preset": preset, "ServerGroups": groups, "MaxRange": maxPresetYearRange, "RecommendedLibrary": recommended,
	})
}

// createPresetYears is presetYearsForm's submit handler: one Collection row
// per year in [fromYear, toYear], each an ordinary external_list collection
// exactly like a single preset pick produces - created empty (no Plex round
// trip per year at creation time, same as the regular New Collection form),
// left for the existing per-row "Refresh" or the Collections table's
// already-fixed "Refresh Selected" bulk action to actually populate.
// Skips any year whose resolved name already exists among this user's
// collections, so re-running it after adding a few more years doesn't
// duplicate the ones already there.
func (h *CollectionsHandler) createPresetYears(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	slug := chi.URLParam(r, "slug")
	preset := findCollectionPreset(slug)
	if preset == nil || !preset.NeedsYear {
		http.Error(w, "Preset not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
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
	fromYear, errFrom := strconv.Atoi(strings.TrimSpace(r.FormValue("fromYear")))
	toYear, errTo := strconv.Atoi(strings.TrimSpace(r.FormValue("toYear")))
	if errFrom != nil || errTo != nil || fromYear <= 0 || toYear <= 0 {
		http.Error(w, "A valid from/to year is required", http.StatusBadRequest)
		return
	}
	if fromYear > toYear {
		fromYear, toYear = toYear, fromYear
	}
	if toYear-fromYear+1 > maxPresetYearRange {
		http.Error(w, fmt.Sprintf("That's %d years - keep a single batch to %d or fewer", toYear-fromYear+1, maxPresetYearRange), http.StatusBadRequest)
		return
	}

	existing, err := db.GetUserCollections(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	existingNames := map[string]bool{}
	for _, c := range existing {
		if c.ServerID == lib.ServerID && c.LibrarySectionID == lib.ID {
			existingNames[strings.ToLower(c.Name)] = true
		}
	}

	created, skipped := 0, 0
	for year := fromYear; year <= toYear; year++ {
		yearStr := strconv.Itoa(year)
		name := preset.resolvedName(yearStr)
		if existingNames[strings.ToLower(name)] {
			skipped++
			continue
		}
		src := db.ExternalListSource{Provider: preset.Provider, Mode: preset.Mode, ListID: preset.resolvedListID(yearStr)}
		b, _ := json.Marshal(src)
		if _, err := db.CreateCollection(h.DB, user.ID, lib.ServerID, lib.ID, lib.Type, name, "", "external_list", string(b), "[]", "sync"); err != nil {
			slog.Error("preset bulk-create: failed to create collection", "error", err, "name", name)
			continue
		}
		created++
	}

	h.Notifications.Add(user.ID, notifications.TypeAction, "Created "+strconv.Itoa(created)+" "+preset.Name+" collection(s)",
		fmt.Sprintf("%d created, %d already existed - use Refresh Selected to populate them", created, skipped), notifications.StatusSuccess, nil)
	h.renderList(w, r)
}

// parseRuleRows reads the repeatable ruleField/ruleValue form arrays
// (collection-form.js's add/remove rows) into []db.Rule, plus a standalone
// "unwatched" checkbox folded in as its own rule - see the package doc for
// why there's no operator picker in the UI yet.
func parseRuleRows(r *http.Request) []db.Rule {
	fields := r.Form["ruleField"]
	values := r.Form["ruleValue"]
	var rules []db.Rule
	for i := 0; i < len(fields) && i < len(values); i++ {
		field := strings.TrimSpace(fields[i])
		value := strings.TrimSpace(values[i])
		if field == "" || value == "" {
			continue
		}
		rules = append(rules, db.Rule{Field: field, Operator: "is", Value: value})
	}
	if r.Form.Has("unwatchedOnly") {
		rules = append(rules, db.Rule{Field: "unwatched", Operator: "is", Value: "true"})
	}
	return rules
}

// providerLabel is the display name for an ExternalListSource.Provider
// value, shared by buildView's summary and the Missing Films/TV Shows list.
func providerLabel(provider string) string {
	switch provider {
	case "imdb":
		return "IMDb"
	case "tvdb":
		return "TVDb"
	case "letterboxd":
		return "Letterboxd"
	case "trakt":
		return "Trakt"
	default:
		return "TMDb"
	}
}

// sourceLinkFor builds the actual external page a collection's list/chart
// was pulled from, so the Builder badge can link straight to it. libraryType
// is Plex's own "movie" | "show", used to pick movie vs tv chart URLs for
// providers where that distinction matters.
func sourceLinkFor(src db.ExternalListSource, libraryType string) string {
	mediaPath := "movie"
	if libraryType == "show" {
		mediaPath = "tv"
	}
	switch src.Provider {
	case "imdb":
		if src.Mode == "top250" {
			return "https://www.imdb.com/chart/top/"
		}
		return "https://www.imdb.com/list/" + src.ListID + "/"
	case "tvdb":
		return "https://thetvdb.com/lists/" + src.ListID
	case "letterboxd":
		return "https://letterboxd.com/" + src.ListID + "/"
	case "trakt":
		// ListID is already the full trakt.tv list URL the user pasted in
		// (same convention GetListItems parses username/list back out of) -
		// nothing to reconstruct, unlike the id-only providers above.
		if strings.HasPrefix(src.ListID, "http") {
			return src.ListID
		}
		return ""
	default: // "tmdb"
		switch src.Mode {
		case "popular":
			return "https://www.themoviedb.org/" + mediaPath + "/popular"
		case "top_rated":
			return "https://www.themoviedb.org/" + mediaPath + "/top-rated"
		case "collection":
			return "https://www.themoviedb.org/collection/" + src.ListID
		default:
			return "https://www.themoviedb.org/list/" + src.ListID
		}
	}
}

// validModesByProvider lists each provider's non-"list" modes - TMDb has
// chart endpoints, IMDb has its fixed Top 250 chart, TVDb/Letterboxd are
// list-only. Anything not in a provider's set (including an empty/garbage
// value) falls back to "list", the one mode every provider supports.
var validModesByProvider = map[string]map[string]bool{
	"tmdb": {"popular": true, "top_rated": true, "trending_daily": true, "trending_weekly": true, "collection": true},
	"imdb": {"top250": true},
}

// parseExternalListSourceForm reads collection_form.html's external-list
// source fields into a db.ExternalListSource - used by both create and
// update, since (unlike Smart's rules) an external-list collection's
// source config is editable after creation.
func parseExternalListSourceForm(r *http.Request) (db.ExternalListSource, error) {
	provider := r.FormValue("externalProvider")
	if provider != "tmdb" && provider != "imdb" && provider != "tvdb" && provider != "letterboxd" && provider != "trakt" {
		return db.ExternalListSource{}, fmt.Errorf("invalid external list provider")
	}
	mode := r.FormValue("tmdbMode")
	if !validModesByProvider[provider][mode] {
		mode = "list"
	}
	listID := strings.TrimSpace(r.FormValue("tmdbListId"))
	if (mode == "list" || mode == "collection") && listID == "" {
		return db.ExternalListSource{}, fmt.Errorf("a list/collection ID or URL is required")
	}
	limit, _ := strconv.Atoi(r.FormValue("tmdbLimit"))
	return db.ExternalListSource{Provider: provider, Mode: mode, ListID: listID, Limit: limit}, nil
}

func (h *CollectionsHandler) create(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 255 {
		http.Error(w, "Collection name is required (max 255 characters)", http.StatusBadRequest)
		return
	}
	lib, ok := decodeLibraryOption(r.FormValue("library"))
	if !ok {
		http.Error(w, "A library is required", http.StatusBadRequest)
		return
	}
	// Don't trust the round-tripped server id from the form - re-verify it
	// belongs to this user and is actually owned by them (same principle
	// ownedCollection already applies to reads), rather than assuming the
	// <select>'s value is honest.
	server, err := db.GetUserServerByID(h.DB, lib.ServerID)
	if err != nil || server == nil || server.UserID != user.ID || !server.IsOwner {
		http.Error(w, "Invalid server", http.StatusBadRequest)
		return
	}
	builderType := r.FormValue("builderType")
	if builderType != "manual" && builderType != "external_list" {
		builderType = "smart"
	}
	syncMode := r.FormValue("syncMode")
	if syncMode != "add_only" {
		syncMode = "sync"
	}

	rulesJSON := ""
	switch builderType {
	case "smart":
		rules := parseRuleRows(r)
		if len(rules) == 0 {
			http.Error(w, "At least one rule is required for a smart collection", http.StatusBadRequest)
			return
		}
		// Smart collections (actor/studio ones especially) are the case
		// most likely to get accidentally recreated under a slightly
		// different name from a different source (this app, Kometa, a
		// manual Plex collection) - a same-name collection already
		// existing in the target library, regardless of who created it,
		// is treated as that same duplicate and blocked rather than
		// silently creating a second one with an identical selection.
		if dup, err := h.client(user, server).GetCollections(lib.ID); err == nil {
			for _, existing := range dup {
				if strings.EqualFold(existing.Title, name) {
					http.Error(w, "A collection named \""+name+"\" already exists in this library - rename it or remove the existing one first to avoid duplicate smart collections.", http.StatusConflict)
					return
				}
			}
		}
		b, _ := json.Marshal(rules)
		rulesJSON = string(b)
	case "external_list":
		src, err := parseExternalListSourceForm(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := json.Marshal(src)
		rulesJSON = string(b)
	}

	created, err := db.CreateCollection(h.DB, user.ID, server.ID, lib.ID, lib.Type, name, r.FormValue("description"), builderType, rulesJSON, "[]", syncMode)
	if err != nil {
		slog.Error("failed to create collection", "error", err, "userId", user.ID, "name", name)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Create collection", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to create collection", http.StatusInternalServerError)
		return
	}
	if sortTitle := strings.TrimSpace(r.FormValue("sortTitle")); sortTitle != "" {
		_ = db.UpdateCollection(h.DB, created.ID, db.CollectionUpdate{SortTitle: &sortTitle})
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Create collection", name, notifications.StatusSuccess, nil)
	h.renderList(w, r)
}

// ownedCollection loads a collection and verifies it belongs to the current
// user - mirrors schedules.go's ownedSchedule.
func (h *CollectionsHandler) ownedCollection(r *http.Request, id int64) (*db.Collection, error) {
	coll, err := db.GetCollectionByID(h.DB, id)
	if err != nil || coll == nil {
		return nil, err
	}
	if coll.UserID != auth.CurrentUser(r).ID {
		return nil, nil
	}
	return coll, nil
}

func (h *CollectionsHandler) edit(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	coll, err := h.ownedCollection(r, id)
	if err != nil || coll == nil {
		http.Error(w, "Collection not found", http.StatusNotFound)
		return
	}
	server, err := db.GetUserServerByID(h.DB, coll.ServerID)
	if err != nil || server == nil {
		http.Error(w, "no Plex server configured", http.StatusInternalServerError)
		return
	}
	names := h.libraryNamesForUser(user, server)

	var manualItems []trackRow
	if coll.BuilderType == "manual" && len(coll.ParsedManualItems()) > 0 {
		client := h.client(user, server)
		for _, key := range coll.ParsedManualItems() {
			t, err := client.GetTrackDetails(key)
			if err != nil || t == nil {
				continue
			}
			manualItems = append(manualItems, toTrackRow(*t))
		}
	}

	h.Tmpl.RenderPartial(w, "partials/collection_form.html", map[string]any{
		"Collection":  h.buildView(*coll, server.ServerName, names, 0, nil),
		"ManualItems": manualItems,
	})
}

func (h *CollectionsHandler) update(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	coll, err := h.ownedCollection(r, id)
	if err != nil || coll == nil {
		http.Error(w, "Collection not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	update := db.CollectionUpdate{}
	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		if len(name) > 255 {
			http.Error(w, "Collection name must be 255 characters or less", http.StatusBadRequest)
			return
		}
		update.Name = &name
	}
	if r.Form.Has("description") {
		desc := r.FormValue("description")
		update.Description = &desc
	}
	if syncMode := r.FormValue("syncMode"); syncMode == "sync" || syncMode == "add_only" {
		update.SyncMode = &syncMode
	}
	if r.Form.Has("sortTitle") {
		sortTitle := strings.TrimSpace(r.FormValue("sortTitle"))
		update.SortTitle = &sortTitle
	}
	if coll.BuilderType == "smart" && r.Form.Has("ruleField") {
		rules := parseRuleRows(r)
		if len(rules) == 0 {
			http.Error(w, "At least one rule is required for a smart collection", http.StatusBadRequest)
			return
		}
		b, _ := json.Marshal(rules)
		s := string(b)
		update.Rules = &s
	}
	if coll.BuilderType == "external_list" && r.Form.Has("externalProvider") {
		src, err := parseExternalListSourceForm(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := json.Marshal(src)
		s := string(b)
		update.Rules = &s
	}

	if err := db.UpdateCollection(h.DB, id, update); err != nil {
		slog.Error("failed to update collection", "error", err, "collectionId", id)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Update collection", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to update collection", http.StatusInternalServerError)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Update collection", coll.Name, notifications.StatusSuccess, nil)
	h.renderList(w, r)
}

func (h *CollectionsHandler) delete(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	coll, err := h.ownedCollection(r, id)
	if err != nil || coll == nil {
		http.Error(w, "Collection not found", http.StatusNotFound)
		return
	}
	if r.FormValue("deleteInPlex") == "true" && coll.PlexCollectionID.Valid {
		if server, err := db.GetUserServerByID(h.DB, coll.ServerID); err == nil && server != nil {
			if err := h.client(user, server).DeleteCollection(coll.PlexCollectionID.String); err != nil {
				slog.Error("failed to delete collection from Plex", "error", err, "collectionId", id)
			}
		}
	}
	if err := db.DeleteCollection(h.DB, id); err != nil {
		slog.Error("failed to delete collection", "error", err, "collectionId", id)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Delete collection", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to delete collection", http.StatusInternalServerError)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Delete collection", coll.Name, notifications.StatusSuccess, nil)
	h.renderList(w, r)
}

// run triggers an immediate refresh through the action queue - the same
// bounded-worker/notification pattern other interactive Plex writes use
// (deemix/lidarr/missing-track actions), rather than a bare goroutine.
// Unlike a schedule-driven refresh, this one-off run has no schedule row to
// attach schedule_executions bookkeeping to, same as mix_templates.go's
// runTemplate.
func (h *CollectionsHandler) run(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	coll, err := h.ownedCollection(r, id)
	if err != nil || coll == nil {
		http.Error(w, "Collection not found", http.StatusNotFound)
		return
	}
	server, err := db.GetUserServerByID(h.DB, coll.ServerID)
	if err != nil || server == nil {
		http.Error(w, "no Plex server configured", http.StatusInternalServerError)
		return
	}

	jobID, _ := h.Queue.Enqueue(user.ID, "Refreshing "+coll.Name, notifications.TypeSchedule, func(notificationID string) error {
		client := h.client(user, server)
		_, _, err := scheduler.RefreshCollection(h.DB, client, coll, server)
		status, detail := notifications.StatusSuccess, "Refreshed"
		if err != nil {
			status, detail = notifications.StatusError, err.Error()
		}
		pct := 100
		h.Notifications.Update(user.ID, notificationID, notifications.Patch{Status: &status, Detail: &detail, Progress: &pct})
		return err
	})

	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{
		"Notifications": h.Notifications.List(user.ID),
		"JobID":         jobID,
	})
}

// bulkDelete mirrors playlists.go's bulkDelete: delete every selected
// (managed) collection, tolerating individual failures. Unmanaged rows have
// no checkbox (collection_list.html), so ids here are always DB rows the
// per-row delete button would also accept; Plex-side is left alone, same
// default as the per-row delete.
func (h *CollectionsHandler) bulkDelete(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	_ = r.ParseForm()
	failed := 0
	for _, raw := range r.Form["id"] {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		coll, err := h.ownedCollection(r, id)
		if err != nil || coll == nil {
			continue
		}
		if err := db.DeleteCollection(h.DB, id); err != nil {
			slog.Error("bulk delete: failed to delete collection", "collectionId", id, "error", err)
			failed++
		}
	}

	// Real Plex collections with no Playlist Lab row (collection_list.html's
	// unmanaged rows) have no local definition to delete - the only delete
	// that applies to them is straight off the Plex server, same as their
	// single-row "Delete from Plex" action (deleteUnmanaged, below). Clients
	// are cached per server since a bulk Plex cleanup can easily select
	// dozens/hundreds of rows on the same server (DESIGN.md §11.11 measured
	// a real 921-collection account).
	clients := map[int64]*plex.Client{}
	for _, raw := range r.Form["unmanaged"] {
		serverIDStr, plexID, ok := strings.Cut(raw, "|")
		if !ok || plexID == "" {
			continue
		}
		serverID, err := strconv.ParseInt(serverIDStr, 10, 64)
		if err != nil {
			continue
		}
		client, ok := clients[serverID]
		if !ok {
			server, err := db.GetUserServerByID(h.DB, serverID)
			if err != nil || server == nil || server.UserID != user.ID || !server.IsOwner {
				clients[serverID] = nil
				continue
			}
			client = h.client(user, server)
			clients[serverID] = client
		}
		if client == nil {
			continue
		}
		if err := client.DeleteCollection(plexID); err != nil {
			slog.Error("bulk delete: failed to delete unmanaged collection from Plex", "serverId", serverID, "plexId", plexID, "error", err)
			failed++
		}
	}

	if failed > 0 {
		slog.Warn("bulk collection delete finished with failures", "failed", failed)
	}
	h.renderList(w, r)
}

// bulkRun enqueues an immediate refresh (see run, above) for every selected
// collection, tolerating individual failures the same way.
func (h *CollectionsHandler) bulkRun(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	_ = r.ParseForm()
	for _, raw := range r.Form["id"] {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		coll, err := h.ownedCollection(r, id)
		if err != nil || coll == nil {
			continue
		}
		server, err := db.GetUserServerByID(h.DB, coll.ServerID)
		if err != nil || server == nil {
			continue
		}
		h.Queue.Enqueue(user.ID, "Refreshing "+coll.Name, notifications.TypeSchedule, func(notificationID string) error {
			client := h.client(user, server)
			_, _, err := scheduler.RefreshCollection(h.DB, client, coll, server)
			status, detail := notifications.StatusSuccess, "Refreshed"
			if err != nil {
				status, detail = notifications.StatusError, err.Error()
			}
			pct := 100
			h.Notifications.Update(user.ID, notificationID, notifications.Patch{Status: &status, Detail: &detail, Progress: &pct})
			return err
		})
	}
	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{
		"Notifications": h.Notifications.List(user.ID),
	})
}

// setPoster sets a Collection's Plex-side poster from an image URL -
// reuses plex.Client.UploadPlaylistPoster verbatim (POST /library/metadata/
// {ratingKey}/posters is a generic Plex metadata-object endpoint, not
// playlist-specific, despite the method's name; not worth a renamed/
// duplicated method for this one extra caller). Only available once the
// collection has actually been refreshed at least once (PlexCollectionID
// set) - there's no Plex object to attach a poster to before that.
func (h *CollectionsHandler) setPoster(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	coll, err := h.ownedCollection(r, id)
	if err != nil || coll == nil {
		http.Error(w, "Collection not found", http.StatusNotFound)
		return
	}
	if !coll.PlexCollectionID.Valid || coll.PlexCollectionID.String == "" {
		http.Error(w, "Run this collection at least once before setting a poster", http.StatusBadRequest)
		return
	}
	imageURL := strings.TrimSpace(r.FormValue("imageUrl"))
	if imageURL == "" {
		http.Error(w, "An image URL is required", http.StatusBadRequest)
		return
	}
	server, err := db.GetUserServerByID(h.DB, coll.ServerID)
	if err != nil || server == nil {
		http.Error(w, "no Plex server configured", http.StatusInternalServerError)
		return
	}
	errMsg := ""
	if err := h.client(user, server).UploadPlaylistPoster(coll.PlexCollectionID.String, imageURL); err != nil {
		slog.Error("failed to set collection poster", "error", err, "collectionId", id)
		errMsg = err.Error()
		h.Notifications.Add(user.ID, notifications.TypeAction, "Set collection poster", errMsg, notifications.StatusError, nil)
	} else {
		h.Notifications.Add(user.ID, notifications.TypeAction, "Set collection poster", coll.Name, notifications.StatusSuccess, nil)
	}
	h.Tmpl.RenderPartial(w, "partials/collection_poster_status.html", map[string]any{"Error": errMsg})
}

// searchItems backs the manual builder's debounced "add items" search box
// (mirrors editor.html's track search / the Missing Tracks Match modal),
// scoped to the collection's own library and item type.
func (h *CollectionsHandler) searchItems(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	coll, err := h.ownedCollection(r, id)
	if err != nil || coll == nil {
		http.Error(w, "Collection not found", http.StatusNotFound)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	data := map[string]any{"CollectionID": id, "Query": query}
	if query != "" {
		server, err := db.GetUserServerByID(h.DB, coll.ServerID)
		if err == nil && server != nil {
			items, err := h.client(user, server).SearchLibraryItemsByQuery(coll.LibrarySectionID, itemTypeForLibraryType(coll.LibraryType), query, 20)
			if err == nil {
				rows := make([]trackRow, len(items))
				for i, t := range items {
					rows[i] = toTrackRow(t)
				}
				data["Results"] = rows
			}
		}
	}
	h.Tmpl.RenderPartial(w, "partials/collection_search_results.html", data)
}

// toTrackRow is playlists.go's toTrackRows for a single already-fetched
// item - used here for movie/show/track items alike (trackRow's Artist/
// Album fields are simply empty for a movie/show, which have no
// grandparent/parent metadata).
func toTrackRow(t plex.Track) trackRow {
	return toTrackRows([]plex.Track{t})[0]
}

// itemTypeForLibraryType mirrors scheduler.itemTypeForLibraryType (kept as
// a small unexported duplicate rather than exporting the scheduler
// package's internal helper just for this one call site).
func itemTypeForLibraryType(libraryType string) int {
	switch libraryType {
	case "movie":
		return 1
	case "show":
		return 2
	default:
		return 10
	}
}

func (h *CollectionsHandler) addManualItem(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	coll, err := h.ownedCollection(r, id)
	if err != nil || coll == nil {
		http.Error(w, "Collection not found", http.StatusNotFound)
		return
	}
	ratingKey := r.FormValue("ratingKey")
	if ratingKey == "" {
		http.Error(w, "ratingKey is required", http.StatusBadRequest)
		return
	}
	items := coll.ParsedManualItems()
	for _, existing := range items {
		if existing == ratingKey {
			h.renderManualItems(w, user, coll)
			return
		}
	}
	items = append(items, ratingKey)
	b, _ := json.Marshal(items)
	s := string(b)
	if err := db.UpdateCollection(h.DB, id, db.CollectionUpdate{ManualItems: &s}); err != nil {
		slog.Error("failed to add manual item to collection", "error", err, "collectionId", id)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Add item to collection", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to update collection", http.StatusInternalServerError)
		return
	}
	coll.ManualItems = sql.NullString{String: s, Valid: true}
	h.renderManualItems(w, user, coll)
}

func (h *CollectionsHandler) removeManualItem(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	coll, err := h.ownedCollection(r, id)
	if err != nil || coll == nil {
		http.Error(w, "Collection not found", http.StatusNotFound)
		return
	}
	ratingKey := chi.URLParam(r, "ratingKey")
	var kept []string
	for _, existing := range coll.ParsedManualItems() {
		if existing != ratingKey {
			kept = append(kept, existing)
		}
	}
	if kept == nil {
		kept = []string{}
	}
	b, _ := json.Marshal(kept)
	s := string(b)
	if err := db.UpdateCollection(h.DB, id, db.CollectionUpdate{ManualItems: &s}); err != nil {
		slog.Error("failed to remove manual item from collection", "error", err, "collectionId", id)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Remove item from collection", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to update collection", http.StatusInternalServerError)
		return
	}
	coll.ManualItems = sql.NullString{String: s, Valid: true}
	h.renderManualItems(w, user, coll)
}

func (h *CollectionsHandler) renderManualItems(w http.ResponseWriter, user *db.User, coll *db.Collection) {
	server, err := db.GetUserServerByID(h.DB, coll.ServerID)
	var rows []trackRow
	if err == nil && server != nil {
		client := h.client(user, server)
		for _, key := range coll.ParsedManualItems() {
			t, err := client.GetTrackDetails(key)
			if err != nil || t == nil {
				continue
			}
			rows = append(rows, toTrackRow(*t))
		}
	}
	h.Tmpl.RenderPartial(w, "partials/collection_manual_items.html", map[string]any{
		"CollectionID": coll.ID,
		"ManualItems":  rows,
	})
}

// missingItemView adds display-only fields db.MissingCollectionItem
// doesn't carry - Link/ProviderLabel are derived from GuidKey's provider
// prefix, same "precompute for the template" reasoning as CollectionView.
type missingItemView struct {
	db.MissingCollectionItem
	ProviderLabel string
	Link          string // empty when no direct-by-id URL is known for the provider (TVDb)
}

func externalItemLink(guidKey, mediaType string) (provider, link string) {
	switch {
	case strings.HasPrefix(guidKey, "tmdb://"):
		id := strings.TrimPrefix(guidKey, "tmdb://")
		t := "movie"
		if mediaType == "tv" {
			t = "tv"
		}
		return "TMDb", "https://www.themoviedb.org/" + t + "/" + id
	case strings.HasPrefix(guidKey, "imdb://"):
		return "IMDb", "https://www.imdb.com/title/" + strings.TrimPrefix(guidKey, "imdb://") + "/"
	case strings.HasPrefix(guidKey, "tvdb://"):
		// No confirmed direct-by-id URL pattern for TVDb (its site uses
		// slugs, not bare ids) - link omitted rather than guessed.
		return "TVDb", ""
	default:
		return "", ""
	}
}

// missingListForCollection backs the Collections table's inline "Missing"
// expando row (DESIGN.md §11.11) - same GET-on-expand, lazy-loaded,
// scoped-to-one-row pattern as home.html's Missing Tracks column
// (GET /missing/list?playlistId=), integrated directly into the main table
// rather than as a separate page section.
func (h *CollectionsHandler) missingListForCollection(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	h.renderMissingForCollection(w, user, id)
}

// collectionItemRow is GetCollectionItems' movie/show-flavored answer to
// playlists.go's trackRow - same "Plex Track shape, one field subset per
// media kind" split, just Title/Year instead of Title/Artist/Album/Codec
// (a collection's members have no audio metadata to show).
type collectionItemRow struct {
	RatingKey string
	Title     string
	Year      int
}

func toCollectionItemRows(items []plex.Track) []collectionItemRow {
	rows := make([]collectionItemRow, len(items))
	for i, it := range items {
		rows[i] = collectionItemRow{RatingKey: it.RatingKey, Title: it.Title, Year: it.Year}
	}
	return rows
}

// itemsPreview backs the Collections table's row-click expando (mirrors
// home.html's Playlists row-click track preview, DESIGN.md §11.1) - a
// live GET /library/collections/{ratingKey}/items call against Plex, not
// anything cached locally, so it always reflects what's actually in the
// collection right now rather than this app's last-refreshed idea of it.
// Only meaningful for a managed row that's been refreshed at least once
// (PlexCollectionID set) - an unmanaged row's own item list isn't wired
// up here (DESIGN.md §11.11 scopes its click/edit/refresh actions the
// same way).
func (h *CollectionsHandler) itemsPreview(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	coll, err := h.ownedCollection(r, id)
	if err != nil || coll == nil {
		h.Tmpl.RenderPartial(w, "partials/collection_items_preview.html", map[string]any{"Error": true})
		return
	}
	if !coll.PlexCollectionID.Valid || coll.PlexCollectionID.String == "" {
		h.Tmpl.RenderPartial(w, "partials/collection_items_preview.html", map[string]any{})
		return
	}
	server, err := db.GetUserServerByID(h.DB, coll.ServerID)
	if err != nil || server == nil {
		h.Tmpl.RenderPartial(w, "partials/collection_items_preview.html", map[string]any{"Error": true})
		return
	}
	items, err := h.client(user, server).GetCollectionItems(coll.PlexCollectionID.String)
	if err != nil {
		slog.Error("failed to fetch collection items for preview", "error", err, "collectionId", id)
		h.Tmpl.RenderPartial(w, "partials/collection_items_preview.html", map[string]any{"Error": true})
		return
	}
	h.Tmpl.RenderPartial(w, "partials/collection_items_preview.html", map[string]any{"Items": toCollectionItemRows(items)})
}

func (h *CollectionsHandler) renderMissingForCollection(w http.ResponseWriter, user *db.User, collectionID int64) {
	all, err := db.GetUserMissingCollectionItems(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var views []missingItemView
	for _, it := range all {
		if it.CollectionID != collectionID {
			continue
		}
		label, link := externalItemLink(it.GuidKey, it.MediaType)
		views = append(views, missingItemView{MissingCollectionItem: it, ProviderLabel: label, Link: link})
	}
	h.Tmpl.RenderPartial(w, "partials/missing_media_list.html", map[string]any{
		"CollectionID": collectionID,
		"Items":        views,
	})
}

// removeMissingItem dismisses one missing entry until the next refresh
// re-derives it - same "verify it's in the user's own list, then delete by
// id" pattern as missing.go's deleteTrack.
func (h *CollectionsHandler) removeMissingItem(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	items, err := db.GetUserMissingCollectionItems(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var collectionID int64
	owned := false
	for _, it := range items {
		if it.ID == id {
			owned = true
			collectionID = it.CollectionID
			break
		}
	}
	if !owned {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := db.RemoveMissingCollectionItem(h.DB, id); err != nil {
		slog.Error("failed to remove missing collection item", "error", err, "itemId", id)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Dismiss missing item", err.Error(), notifications.StatusError, nil)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.renderMissingForCollection(w, user, collectionID)
}

// bulkRemoveMissingItems dismisses every checked missing entry in one
// collection's expando row - same hx-include-gathered "id" (repeated) form
// field convention as missing.go's bulkRemove, scoped to collectionId
// (a query param, mirroring removeMissingItem's own collectionId param)
// since a bulk request may check items across... actually always exactly
// one collection in practice (the expando only ever shows one collection's
// items), but the id list is still verified against the current user's own
// items either way.
func (h *CollectionsHandler) bulkRemoveMissingItems(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	_ = r.ParseForm()
	collectionID, _ := strconv.ParseInt(r.URL.Query().Get("collectionId"), 10, 64)

	items, err := db.GetUserMissingCollectionItems(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	owned := make(map[int64]bool, len(items))
	for _, it := range items {
		owned[it.ID] = true
	}
	failed := 0
	for _, raw := range r.Form["id"] {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || !owned[id] {
			continue
		}
		if err := db.RemoveMissingCollectionItem(h.DB, id); err != nil {
			slog.Warn("bulk-remove: failed to remove missing collection item", "error", err, "itemId", id)
			failed++
		}
	}
	if failed > 0 {
		h.Notifications.Add(user.ID, notifications.TypeAction, "Dismiss missing items", fmt.Sprintf("%d failed", failed), notifications.StatusError, nil)
	} else {
		h.Notifications.Add(user.ID, notifications.TypeAction, "Dismiss missing items", "Dismissed", notifications.StatusSuccess, nil)
	}
	h.renderMissingForCollection(w, user, collectionID)
}

// deleteUnmanaged removes a real Plex collection this app never created a
// row for (DESIGN.md §11.11) - identified by serverId+plexId (a Plex
// ratingKey) rather than a Playlist Lab collection id, since there's no DB
// row to look one up from. Verifies the server belongs to and is owned by
// the current user before touching anything, same principle
// ownedCollection already applies for managed rows.
func (h *CollectionsHandler) deleteUnmanaged(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	serverID, _ := strconv.ParseInt(r.URL.Query().Get("serverId"), 10, 64)
	plexID := r.URL.Query().Get("plexId")
	if serverID == 0 || plexID == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	server, err := db.GetUserServerByID(h.DB, serverID)
	if err != nil || server == nil || server.UserID != user.ID || !server.IsOwner {
		http.Error(w, "Invalid server", http.StatusBadRequest)
		return
	}
	if err := h.client(user, server).DeleteCollection(plexID); err != nil {
		slog.Error("failed to delete unmanaged collection from Plex", "error", err, "serverId", serverID, "plexId", plexID)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Delete collection from Plex", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to delete collection", http.StatusBadGateway)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Delete collection from Plex", "Deleted", notifications.StatusSuccess, nil)
	h.renderList(w, r)
}
