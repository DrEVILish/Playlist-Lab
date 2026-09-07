// Package handlers: backup.go ports BackupRestorePage.tsx to HTMX. Backup is
// a plain GET download (browser handles the "save file" part natively -
// no client-side Blob/anchor-click dance needed). Restore reuses the same
// pieces as import.go's file-upload path: importsvc.ImportTracksFromFile to
// match each backed-up playlist's tracks against the user's Plex library,
// then importsvc.FinalizeImportResult to create it - restore never
// overwrites a same-named playlist, matching the React version's behavior,
// since FinalizeImportResult always creates fresh.
package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/importsvc"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

type BackupHandler struct {
	DB       *sql.DB
	PlexAuth *auth.PlexClient
	Tmpl     *Templates
}

func RegisterBackup(r chi.Router, mw *auth.Middleware, h *BackupHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/backup", h.page)
		r.Get("/backup/export", h.export)
		r.Post("/backup/restore", h.restore)
	})
}

func (h *BackupHandler) page(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	userServer, _ := db.GetUserServer(h.DB, user.ID)
	data := map[string]any{"User": user, "HasServer": userServer != nil && userServer.LibraryID.Valid}
	if userServer != nil && userServer.LibraryID.Valid {
		client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(user.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")
		if playlists, err := client.GetPlaylists(); err == nil {
			data["Playlists"] = playlists
		}
	}
	h.Tmpl.RenderPage(w, r, "backup_restore", data)
}

// backupTrack/backupPlaylist/backupFile mirror BackupRestorePage.tsx's
// BackupPlaylist/BackupData JSON shape exactly, so a file exported by the
// React app (or an earlier Go export) can still be restored here.
type backupTrack struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album,omitempty"`
}

type backupPlaylist struct {
	Title      string        `json:"title"`
	Tracks     []backupTrack `json:"tracks"`
	BackupDate string        `json:"backupDate"`
}

type backupFile struct {
	Version    int              `json:"version"`
	ExportDate string           `json:"exportDate"`
	ServerName string           `json:"serverName"`
	Playlists  []backupPlaylist `json:"playlists"`
}

// export ports handleBackup(): the selected Plex playlist IDs (repeated
// "id" form values on a GET, since this is a plain browser download link,
// not a fetch) get their tracks pulled and written out as one JSON file.
func (h *BackupHandler) export(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "no Plex server selected", http.StatusBadRequest)
		return
	}
	client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(user.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")

	ids := r.URL.Query()["id"]
	if len(ids) == 0 {
		http.Error(w, "no playlists selected", http.StatusBadRequest)
		return
	}
	plexPlaylists, err := client.GetPlaylists()
	if err != nil {
		http.Error(w, "failed to load playlists from Plex", http.StatusBadGateway)
		return
	}
	byID := make(map[string]plex.Playlist, len(plexPlaylists))
	for _, p := range plexPlaylists {
		byID[p.RatingKey] = p
	}

	now := time.Now().Format(time.RFC3339)
	out := backupFile{Version: 1, ExportDate: now, ServerName: "Plex Server"}
	for _, id := range ids {
		p, ok := byID[id]
		if !ok {
			continue
		}
		tracks, err := client.GetPlaylistTracks(id)
		if err != nil {
			slog.Warn("[Backup] failed to load playlist tracks", "playlistId", id, "error", err)
			continue
		}
		bp := backupPlaylist{Title: p.Title, BackupDate: now}
		for _, t := range tracks {
			artist := t.GrandparentTitle
			if artist == "" {
				artist = "Unknown"
			}
			bp.Tracks = append(bp.Tracks, backupTrack{Title: t.Title, Artist: artist, Album: t.ParentTitle})
		}
		out.Playlists = append(out.Playlists, bp)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=plex-playlists-backup-%s.json", time.Now().Format("2006-01-02")))
	_ = json.NewEncoder(w).Encode(out)
}

// restore ports handleRestore(): parse the uploaded backup JSON, then for
// each selected playlist run it through the same match+create pipeline as
// a regular file import (importsvc.ImportTracksFromFile / FinalizeImportResult),
// synchronously (backups are small enough - a handful of playlists, not a
// bulk external-source scrape) so the result partial can render per-playlist
// matched/total counts immediately, same as the React version's inline
// restoreResults state.
func (h *BackupHandler) restore(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "no Plex server selected", http.StatusBadRequest)
		return
	}

	if err := r.ParseMultipartForm(maxImportFileSize); err != nil {
		http.Error(w, "invalid or too-large file upload (10MB limit)", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no backup file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()

	var data backupFile
	if err := json.NewDecoder(file).Decode(&data); err != nil || data.Version != 1 {
		http.Error(w, "invalid backup file format", http.StatusBadRequest)
		return
	}

	client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(user.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")

	type restoreResult struct {
		Name    string
		Matched int
		Total   int
		Error   string
	}
	var results []restoreResult

	for _, p := range data.Playlists {
		tracks := make([]adapters.TrackInfo, len(p.Tracks))
		for i, t := range p.Tracks {
			tracks[i] = adapters.TrackInfo{Title: t.Title, Artist: t.Artist, Album: t.Album}
		}

		result, err := importsvc.ImportTracksFromFile(h.DB, client, p.Title, tracks, importsvc.Options{
			UserID: user.ID, LibraryID: userServer.LibraryID.String,
		}, nil, func() bool { return false })
		if err != nil {
			results = append(results, restoreResult{Name: p.Title, Total: len(p.Tracks), Error: err.Error()})
			continue
		}
		if result.MatchedCount == 0 {
			results = append(results, restoreResult{Name: p.Title, Total: result.TotalCount, Error: "None of these tracks were found in your Plex library"})
			continue
		}

		if _, _, err := importsvc.FinalizeImportResult(h.DB, client, "backup-restore", "backup-"+p.Title, user.ID,
			userServer.ServerClientID, userServer.LibraryID.String, result, importsvc.FinalizeOpts{PlaylistName: p.Title}); err != nil {
			results = append(results, restoreResult{Name: p.Title, Total: result.TotalCount, Error: err.Error()})
			continue
		}
		results = append(results, restoreResult{Name: p.Title, Matched: result.MatchedCount, Total: result.TotalCount})
	}

	h.Tmpl.RenderPartial(w, "partials/backup_restore_results.html", map[string]any{"Results": results})
}
