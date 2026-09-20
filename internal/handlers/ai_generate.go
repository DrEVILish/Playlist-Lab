// AI playlist generation, ported from routes/ai.ts's POST /api/import/ai:
// turn a free-text prompt into search queries via Gemini/Grok, search the
// user's Plex library for each, and build a playlist from what matches.
// The AI client itself (internal/services/ai) and the "test API key"
// half of routes/ai.ts (POST /api/import/ai/test) were already ported -
// see SettingsHandler.testAI - this is the missing other half: nothing in
// the UI actually reached ai.GetSearchQueriesFromGemini/Grok until now.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/ai"
	"github.com/drevilish/playlist-lab/internal/services/importsvc"
	"github.com/drevilish/playlist-lab/internal/services/limiter"
	"github.com/drevilish/playlist-lab/internal/services/matching"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

func (h *ImportHandler) startAIImport(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	_ = r.ParseForm()
	prompt := r.FormValue("prompt")
	if prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}
	// Same 10-100 clamp as validTrackCount in ai.ts, defaulting to 50.
	trackCount := 50
	if n, err := strconv.Atoi(r.FormValue("trackCount")); err == nil {
		trackCount = min(100, max(10, n))
	}

	settings, err := db.GetAISettings(h.DB, user.ID)
	if err != nil {
		http.Error(w, "failed to load AI settings", http.StatusInternalServerError)
		return
	}
	provider := settings.Provider
	if v := r.FormValue("aiProvider"); v != "" {
		provider = v
	}
	apiKey := settings.GeminiAPIKey
	if provider == "grok" {
		apiKey = settings.GrokAPIKey
	}
	if apiKey == "" {
		label := "Gemini"
		if provider == "grok" {
			label = "Grok"
		}
		http.Error(w, label+" API key is required. Add it in Settings first.", http.StatusBadRequest)
		return
	}

	userServer, err := db.GetUserMusicServer(h.DB, user.ID)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "No library selected. Please go to Settings and select a music library first.", http.StatusBadRequest)
		return
	}

	// Queued, same as every other import trigger: the AI calls plus one
	// Plex search per generated query can take a while, and this is exactly
	// what the action queue's cap on concurrent background work is for.
	h.Queue.Enqueue(user.ID, "AI Generated Playlist", notifications.TypeImport, func(notificationID string) error {
		return h.runAI(context.Background(), user.ID, userServer, provider, apiKey, prompt, trackCount, notificationID)
	})
	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{"Notifications": h.Notifications.List(user.ID)})
}

func (h *ImportHandler) runAI(ctx context.Context, userID int64, userServer *db.UserServer, provider, apiKey, prompt string, trackCount int, notificationID string) error {
	fail := func(err error) error {
		status, detail := notifications.StatusError, aiErrorMessage(err)
		h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return err
	}
	progress := func(detail string, pct int) {
		p := pct
		h.Notifications.Update(userID, notificationID, notifications.Patch{Detail: &detail, Progress: &p})
	}

	user, err := db.GetUserByID(h.DB, userID)
	if err != nil {
		return fail(err)
	}
	client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(user.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")
	libraryID := userServer.LibraryID.String

	progress("Asking AI for track ideas...", 0)
	queries, err := limiter.Run(limiter.External, func() ([]string, error) {
		if provider == "grok" {
			return ai.GetSearchQueriesFromGrok(ctx, prompt, apiKey)
		}
		return ai.GetSearchQueriesFromGemini(ctx, prompt, apiKey)
	})
	if err != nil {
		return fail(err)
	}
	slog.Info("[AI] generated search queries", "userId", userID, "provider", provider, "queries", queries)

	progress("Searching your Plex library...", 25)
	// Limit to 10 queries, top 5 tracks each - same caps as ai.ts.
	if len(queries) > 10 {
		queries = queries[:10]
	}
	seen := map[string]bool{}
	var found []plex.Track
	for _, q := range queries {
		tracks, err := client.SearchTrack(q, libraryID, "", "")
		if err != nil {
			slog.Warn("[AI] search failed for a generated query", "query", q, "error", err)
			continue
		}
		if len(tracks) > 5 {
			tracks = tracks[:5]
		}
		for _, t := range tracks {
			if t.RatingKey != "" && !seen[t.RatingKey] {
				seen[t.RatingKey] = true
				found = append(found, t)
			}
		}
	}
	if len(found) == 0 {
		return fail(fmt.Errorf("no tracks in your Plex library matched what the AI suggested - try a more specific prompt"))
	}

	rand.Shuffle(len(found), func(i, j int) { found[i], found[j] = found[j], found[i] })
	if len(found) > trackCount {
		found = found[:trackCount]
	}

	playlistName, err := limiter.Run(limiter.External, func() (string, error) {
		if provider == "grok" {
			return ai.GeneratePlaylistNameWithGrok(ctx, prompt, apiKey)
		}
		return ai.GeneratePlaylistNameWithGemini(ctx, prompt, apiKey)
	})
	if err != nil {
		// A naming failure isn't fatal to the playlist itself, matching
		// ai.ts's own fallbacks inside generatePlaylistNameWith{Gemini,Grok}
		// (those never propagate an error to their caller) - "AI Generated
		// Playlist" beats abandoning tracks that were already found.
		slog.Warn("[AI] failed to generate a playlist name, using fallback", "error", err)
		playlistName = "AI Generated Playlist"
	}
	// Renames the notification from its generic starting title to the
	// AI-chosen one, same moment ai.ts's own updateNotification(..., {
	// title: playlistName, ... }) does.
	titleDetail := "Creating Plex playlist..."
	pct := 75
	h.Notifications.Update(userID, notificationID, notifications.Patch{Title: &playlistName, Detail: &titleDetail, Progress: &pct})

	matched := make([]matching.MatchedTrack, len(found))
	for i, t := range found {
		matched[i] = matching.MatchedTrack{
			Title: t.Title, Artist: t.DisplayArtist(), Album: t.ParentTitle,
			Matched: true, PlexRatingKey: t.RatingKey,
			PlexTitle: t.Title, PlexArtist: t.DisplayArtist(), PlexAlbum: t.ParentTitle,
		}
	}
	result := &importsvc.Result{
		PlaylistName: playlistName, Source: "ai",
		Matched: matched, MatchedCount: len(matched), TotalCount: len(matched),
	}

	dbPlaylistID, _, err := importsvc.FinalizeImportResult(h.DB, client, "ai", "AI: "+prompt, userID,
		userServer.ServerClientID, libraryID, result, importsvc.FinalizeOpts{PlaylistName: playlistName})
	if err != nil {
		return fail(err)
	}
	slog.Info("[AI] created playlist", "playlistId", dbPlaylistID, "trackCount", len(matched))

	status, pct := notifications.StatusSuccess, 100
	title := playlistName
	detail := fmt.Sprintf("Added %d tracks", len(matched))
	h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Progress: &pct, Detail: &detail, Title: &title})
	return nil
}

// aiErrorMessage matches ai.ts's own error handling: an *ai.AuthError from
// either provider gets a clearer message than whatever the raw API error
// text says, since "invalid API key" is a self-service fix and every other
// error isn't.
func aiErrorMessage(err error) string {
	var authErr *ai.AuthError
	if errors.As(err, &authErr) {
		return "Invalid API key. Please check your API key and try again."
	}
	return err.Error()
}
