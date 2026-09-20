// import_file.go: the "Import from File" modal's two-step flow (pick a
// file -> auto-upload and detect its playlist name -> edit the name and
// confirm), separate from startFileImport/runFile (import.go) - that
// older one-shot fire-and-forget endpoint is left exactly as it was
// (still registered, still tested, just no longer linked from the UI),
// same precedent the plain-URL preview/review flow already set alongside
// startImport.
package handlers

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/fileimport"
	"github.com/drevilish/playlist-lab/internal/services/importsvc"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

// maxFileImportPreviewSize caps the modal's auto-upload at 100KB - playlist
// export files are plain text and always tiny, so this is a deliberately
// tight guard against picking the wrong file, well under
// maxImportFileSize (10MB, the older /import/file endpoint's own limit).
const maxFileImportPreviewSize = 100 << 10

// pendingFileImport holds one upload's already-parsed name+tracks between
// the modal's "detect name" step (previewFileImport) and its "Import"
// button (confirmFileImport) - same opaque-id-owned-by-one-user shape as
// importreview.Session, just without per-track review since this flow only
// ever lets the user edit the playlist name before confirming.
type pendingFileImport struct {
	UserID   int64
	Filename string
	Name     string
	Tracks   []adapters.TrackInfo
}

type pendingFileStore struct {
	mu    sync.Mutex
	items map[string]*pendingFileImport
}

func newPendingFileStore() *pendingFileStore {
	return &pendingFileStore{items: map[string]*pendingFileImport{}}
}

func (s *pendingFileStore) New(userID int64, filename, name string, tracks []adapters.TrackInfo) string {
	id := uuid.NewString()
	s.mu.Lock()
	s.items[id] = &pendingFileImport{UserID: userID, Filename: filename, Name: name, Tracks: tracks}
	s.mu.Unlock()
	return id
}

func (s *pendingFileStore) Get(id string, userID int64) (*pendingFileImport, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.items[id]
	if !ok || p.UserID != userID {
		return nil, false
	}
	return p, true
}

func (s *pendingFileStore) Delete(id string) {
	s.mu.Lock()
	delete(s.items, id)
	s.mu.Unlock()
}

func RegisterImportFile(r chi.Router, mw *auth.Middleware, h *ImportHandler) {
	h.PendingFiles = newPendingFileStore()
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/import/file/form", h.fileImportForm)
		r.Post("/import/file/preview", h.previewFileImport)
		r.Post("/import/file/confirm/{sessionId}", h.confirmFileImport)
	})
}

func (h *ImportHandler) fileImportForm(w http.ResponseWriter, r *http.Request) {
	h.Tmpl.RenderPartial(w, "partials/import_file_modal.html", nil)
}

// previewFileImport is the file input's auto-upload target (hx-trigger
// "change" on the <input type=file> itself - no separate upload button):
// validate size/type, parse the file to detect its name (fileimport.Parse,
// same as the older endpoint), stash the result under a new pending-import
// id, and render the "here's the detected name, edit it if you want" step.
// Every rejection re-renders the picker below the error so the user can
// immediately try a different file without reopening the modal.
func (h *ImportHandler) previewFileImport(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	if err := r.ParseMultipartForm(maxFileImportPreviewSize + 4096); err != nil {
		h.Tmpl.RenderPartial(w, "partials/import_file_error.html", map[string]any{"Error": "File is too large - the limit is 100KB."})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		h.Tmpl.RenderPartial(w, "partials/import_file_error.html", map[string]any{"Error": "No file selected."})
		return
	}
	defer file.Close()

	if !fileimport.IsAllowedExtension(header.Filename) {
		h.Tmpl.RenderPartial(w, "partials/import_file_error.html", map[string]any{"Error": "Unsupported file type. Please upload an M3U, M3U8, PLS, XSPF, CSV, or TXT playlist file."})
		return
	}

	raw, err := io.ReadAll(io.LimitReader(file, maxFileImportPreviewSize+1))
	if err != nil {
		h.Tmpl.RenderPartial(w, "partials/import_file_error.html", map[string]any{"Error": "Unable to read the uploaded file."})
		return
	}
	if len(raw) > maxFileImportPreviewSize {
		h.Tmpl.RenderPartial(w, "partials/import_file_error.html", map[string]any{"Error": "File is too large - the limit is 100KB."})
		return
	}
	content := string(raw)
	if strings.TrimSpace(content) == "" {
		h.Tmpl.RenderPartial(w, "partials/import_file_error.html", map[string]any{"Error": "File is empty."})
		return
	}

	parsed, err := fileimport.Parse(content, header.Filename)
	if err != nil {
		h.Tmpl.RenderPartial(w, "partials/import_file_error.html", map[string]any{"Error": err.Error()})
		return
	}
	if len(parsed.Tracks) == 0 {
		h.Tmpl.RenderPartial(w, "partials/import_file_error.html", map[string]any{"Error": "No tracks found in this file."})
		return
	}

	sessionID := h.PendingFiles.New(user.ID, header.Filename, parsed.Name, parsed.Tracks)
	h.Tmpl.RenderPartial(w, "partials/import_file_confirm.html", map[string]any{
		"SessionID": sessionID, "Name": parsed.Name, "TrackCount": len(parsed.Tracks),
	})
}

// confirmFileImport is the detected-name step's "Import" button: create the
// playlist from the pending upload's already-parsed tracks (same
// match+create job runFile does, just fed pre-parsed tracks instead of
// re-parsing raw content) using whatever name the user confirmed or edited.
func (h *ImportHandler) confirmFileImport(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	sessionID := chi.URLParam(r, "sessionId")
	pending, ok := h.PendingFiles.Get(sessionID, user.ID)
	if !ok {
		http.Error(w, "upload expired - please choose the file again", http.StatusNotFound)
		return
	}
	_ = r.ParseForm()
	playlistName := strings.Join(strings.Fields(r.FormValue("playlistName")), " ")
	if playlistName == "" {
		playlistName = pending.Name
	}

	userServer, err := db.GetUserMusicServer(h.DB, user.ID)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "No music library selected. Please select a library first.", http.StatusBadRequest)
		return
	}

	h.PendingFiles.Delete(sessionID)
	jobID, _ := h.Queue.Enqueue(user.ID, "Importing "+pending.Filename, notifications.TypeImport, func(notificationID string) error {
		return h.runPendingFile(context.Background(), user.ID, userServer, pending, playlistName, notificationID)
	})

	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{
		"Notifications": h.Notifications.List(user.ID), "JobID": jobID,
	})
}

// runPendingFile is confirmFileImport's background job body - the same
// match+create-playlist tail runFile (import.go) uses, minus the
// fileimport.Parse step that already happened in previewFileImport.
func (h *ImportHandler) runPendingFile(ctx context.Context, userID int64, userServer *db.UserServer, pending *pendingFileImport, playlistName, notificationID string) error {
	dbUser, err := db.GetUserByID(h.DB, userID)
	if err != nil {
		return err
	}
	client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(dbUser.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")

	progress := func(phase string, current, total int) {
		detail := phase
		if total > 0 {
			detail = fmt.Sprintf("%s (%d/%d)", phase, current, total)
		}
		h.Notifications.Update(userID, notificationID, notifications.Patch{Detail: &detail})
	}

	result, err := importsvc.ImportTracksFromFile(h.DB, client, playlistName, pending.Tracks, importsvc.Options{
		UserID: userID, LibraryID: userServer.LibraryID.String, CustomName: playlistName,
	}, progress, func() bool { return false })
	if err != nil {
		status := notifications.StatusError
		detail := err.Error()
		h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return err
	}

	dbPlaylistID, _, err := importsvc.FinalizeImportResult(h.DB, client, "file", pending.Filename, userID,
		userServer.ServerClientID, userServer.LibraryID.String, result, importsvc.FinalizeOpts{PlaylistName: playlistName})
	if err != nil {
		status := notifications.StatusError
		detail := err.Error()
		h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return err
	}
	slog.Info("[Import] Created playlist from file", "playlistId", dbPlaylistID, "filename", pending.Filename)

	status := notifications.StatusSuccess
	detail := fmt.Sprintf("Created %q - matched %d of %d tracks", result.PlaylistName, result.MatchedCount, result.TotalCount)
	pct := 100
	h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail, Progress: &pct})
	return nil
}
