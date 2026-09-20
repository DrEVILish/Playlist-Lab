package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/ai"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

func geminiTextResponse(text string) string {
	return `{"candidates":[{"content":{"parts":[{"text":` + jsonQuote(text) + `}]}}]}`
}

// jsonQuote avoids pulling in encoding/json just to escape one string for
// these fake responses.
func jsonQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func TestStartAIImport_RequiresPrompt(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	if err := db.SaveAIProvider(sqlDB, user.ID, "gemini"); err != nil {
		t.Fatalf("SetUserAIProvider: %v", err)
	}

	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, http.MethodPost, "/import/ai", h.startAIImport)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/ai", "", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a missing prompt", rec.Code)
	}
}

func TestStartAIImport_RequiresAPIKey(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	// No API key saved anywhere - GetAISettings falls back to the "gemini"
	// provider with an empty key.

	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, http.MethodPost, "/import/ai", h.startAIImport)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/ai", "prompt=road+trip+rock", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for no configured API key", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Gemini API key") {
		t.Errorf("body = %q, want it to name the missing provider", rec.Body.String())
	}
}

func TestStartAIImport_RequiresLibrarySelected(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	if err := db.SaveGeminiAPIKey(sqlDB, user.ID, "test-key"); err != nil {
		t.Fatalf("SaveGeminiAPIKey: %v", err)
	}
	// No user_servers row at all - GetUserServer returns nil.

	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, http.MethodPost, "/import/ai", h.startAIImport)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/ai", "prompt=road+trip+rock", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for no library selected", rec.Code)
	}
}

// TestRunAI_EndToEnd exercises the full pipeline: fake Gemini for both the
// search-queries and playlist-naming calls, a fake Plex server for the
// per-query search and the eventual playlist creation, and polls the real
// notification store for the terminal state - the same integration style
// playlists_ops_test.go uses for its action-queue handlers.
func TestRunAI_EndToEnd(t *testing.T) {
	restore := ai.SetAPIBasesForTest("", "")
	defer restore()

	geminiCalls := 0
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		geminiCalls++
		w.Header().Set("Content-Type", "application/json")
		if geminiCalls == 1 {
			// GetSearchQueriesFromGemini expects a JSON array of strings.
			w.Write([]byte(geminiTextResponse(`["road trip", "rock"]`)))
			return
		}
		w.Write([]byte(geminiTextResponse("Highway Anthems")))
	}))
	defer geminiSrv.Close()
	restore2 := ai.SetAPIBasesForTest(geminiSrv.URL, "")
	defer restore2()

	var createdTitle string
	plexSrv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine-1"}}`))
		case r.URL.Path == "/hubs/search":
			// SearchTrack with empty artist/title routes to searchHub, which
			// reads a "track"-typed Hub, not a flat Metadata array. Both
			// generated queries hit this same endpoint; returning one track
			// per call is enough to exercise dedup (RatingKey repeats across
			// queries) without modeling Plex's full search-hub response.
			w.Write([]byte(`{"MediaContainer":{"Hub":[{"type":"track","title":"Tracks","Metadata":[{"ratingKey":"t1","title":"Life in the Fast Lane","originalTitle":"Eagles","librarySectionID":1}]}]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/playlists":
			createdTitle = r.URL.Query().Get("title")
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"new-1","title":"` + createdTitle + `"}]}}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	if err := db.SaveGeminiAPIKey(sqlDB, user.ID, "test-key"); err != nil {
		t.Fatalf("SaveGeminiAPIKey: %v", err)
	}
	seedUserServer(t, sqlDB, user.ID, plexSrv.URL)

	store := notifications.NewStore()
	h := &ImportHandler{
		DB: sqlDB, Tmpl: nopTemplates(), Notifications: store, Queue: actionqueue.New(store),
		PlexAuth: auth.NewPlexClient("test-client-id", "Playlist Lab"),
	}
	router := testRouter(sqlDB, http.MethodPost, "/import/ai", h.startAIImport)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/ai",
		"prompt=road+trip+rock&trackCount=10", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (renders the notifications partial); body=%s", rec.Code, rec.Body.String())
	}

	n := waitForNotification(t, store, user.ID)
	if n.Status != notifications.StatusSuccess {
		t.Fatalf("notification status = %q, want success (detail: %s)", n.Status, n.Detail)
	}
	if n.Title != "Highway Anthems" {
		t.Errorf("notification title = %q, want the AI-generated name", n.Title)
	}
	if createdTitle != "Highway Anthems" {
		t.Errorf("created playlist title = %q, want the AI-generated name", createdTitle)
	}
}

func TestAIErrorMessage_AuthErrorGetsFriendlyText(t *testing.T) {
	err := &ai.AuthError{Status: 401, Body: "invalid key"}
	if got := aiErrorMessage(err); !strings.Contains(got, "Invalid API key") {
		t.Errorf("aiErrorMessage(AuthError) = %q, want a friendly invalid-key message", got)
	}
}

func TestAIErrorMessage_OtherErrorsPassThrough(t *testing.T) {
	err := &testError{"network timeout"}
	if got := aiErrorMessage(err); got != "network timeout" {
		t.Errorf("aiErrorMessage(other) = %q, want the raw error text", got)
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
