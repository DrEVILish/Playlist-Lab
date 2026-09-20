package handlers

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	deemixsvc "github.com/drevilish/playlist-lab/internal/services/deemix"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/session"
)

func newAdminRouter(t *testing.T, sqlDB *sql.DB, h *AdminHandler) (chi.Router, *db.User) {
	t.Helper()
	admin := newTestUser(t, sqlDB)
	if err := db.AddAdmin(sqlDB, admin.ID); err != nil {
		t.Fatalf("AddAdmin: %v", err)
	}
	store := session.NewStore(sqlDB)
	mw := &auth.Middleware{DB: sqlDB, Store: store}
	r := chi.NewRouter()
	r.Use(mw.WithSession)
	RegisterAdmin(r, mw, h)
	return r, admin
}

// TestDeemixSettingsPanel_RendersAndSaves is the "one runnable check" for
// admin_deemix_panel.html's settings form: every field in deemix.Settings
// has a matching template reference and a matching parseDeemixSettingsForm
// entry, so a render (GET) and a full round-trip save (POST, then check the
// value actually took effect) both have to succeed for this to pass. A
// typo'd `{{$s.Something}}` or `name=` attribute fails this immediately
// instead of only showing up as a silently-ignored field in production.
func TestDeemixSettingsPanel_RendersAndSaves(t *testing.T) {
	sqlDB := newTestDB(t)
	deemixService := deemixsvc.New(deemixsvc.Config{}, sqlDB, nil)
	h := &AdminHandler{DB: sqlDB, Tmpl: nopTemplates(), Deemix: deemixService, Notifications: notifications.NewStore()}
	router, admin := newAdminRouter(t, sqlDB, h)

	getRec := authedRequest(t, sqlDB, router, admin, http.MethodGet, "/admin/deemix", "", "")
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /admin/deemix status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	if !strings.Contains(getRec.Body.String(), "Download location") {
		t.Fatalf("rendered panel missing expected content, body=%s", getRec.Body.String())
	}

	form := "downloadLocation=" + `%2Fmusic` +
		"&tracknameTemplate=%25artist%25+-+%25title%25" +
		"&albumTracknameTemplate=%25tracknumber%25+-+%25title%25" +
		"&artistNameTemplate=%25artist%25" +
		"&albumNameTemplate=%25artist%25+-+%25album%25" +
		"&paddingSize=0" +
		"&illegalCharacterReplacer=_" +
		"&queueConcurrency=5" +
		"&maxBitrate=3" +
		"&overwriteFile=n" +
		"&embeddedArtworkSize=800" +
		"&tags.title=true&tags.artist=true&tags.album=true&tags.cover=true"
	postRec := authedRequest(t, sqlDB, router, admin, http.MethodPost, "/admin/deemix/settings", form, "application/x-www-form-urlencoded")
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST /admin/deemix/settings status = %d, want 200; body=%s", postRec.Code, postRec.Body.String())
	}
	if !strings.Contains(postRec.Body.String(), "/music") {
		t.Fatalf("saved downloadLocation didn't round-trip into the re-rendered panel, body=%s", postRec.Body.String())
	}

	saved := deemixService.Settings()
	if saved.DownloadLocation != "/music" || saved.QueueConcurrency != 5 || !saved.Tags.Title || saved.Tags.Genre {
		t.Fatalf("Settings() after save = %+v, want the posted values applied in-process (ReloadSettings)", saved)
	}
}
