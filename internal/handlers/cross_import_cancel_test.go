package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/crossimport"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/templates"
)

func TestCrossImportCancelStopsPolling(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	tmpl, err := LoadTemplates(templates.FS)
	if err != nil {
		t.Fatal(err)
	}
	job, err := db.CreateCrossImportJob(sqlDB, user.ID, "plex", "Favorites", "youtube")
	if err != nil {
		t.Fatal(err)
	}
	h := &CrossImportHandler{
		DB: sqlDB, Tmpl: tmpl, Sessions: crossimport.NewStore(), Notifications: notifications.NewStore(),
	}
	sess := h.Sessions.New("review", job, "youtube", adapters.TargetConfig{}, user.ID)
	sess.SetProgress(crossimport.Progress{Phase: crossimport.PhaseMatching})
	pending := h.Notifications.Add(user.ID, notifications.TypeImport, "Favorites", "Matching tracks", notifications.StatusInProgress, nil)
	sess.SetMatchNotification(pending.ID)
	router := testRouter(sqlDB, http.MethodPost, "/cross-import/match/{sessionId}/cancel", h.cancelMatch)
	response := authedRequest(t, sqlDB, router, user, http.MethodPost, "/cross-import/match/review/cancel", "", "")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "hx-trigger=") || !strings.Contains(response.Body.String(), "Matching canceled") {
		t.Fatalf("want terminal canceled fragment, got %d %s", response.Code, response.Body.String())
	}
	list := h.Notifications.List(user.ID)
	if len(list) != 1 || list[0].Status == notifications.StatusInProgress || list[0].Detail != "Matching canceled" {
		t.Fatalf("want one terminal cancellation notification, got %+v", list)
	}
	jobs, err := db.GetCrossImportJobs(sqlDB, user.ID)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("want canceled job removed from history, got %v, %v", jobs, err)
	}
}
