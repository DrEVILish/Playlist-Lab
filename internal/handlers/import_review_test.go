package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/services/importreview"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

// fakeSource is a minimal adapters.SourceAdapter for exercising preview()
// without a real scraper - FetchTracks just returns whatever was configured.
type fakeSource struct {
	name     string
	playlist adapters.PlaylistInfo
	tracks   []adapters.TrackInfo
	err      error
}

func (f *fakeSource) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: f.name, Name: f.name}
}
func (f *fakeSource) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	return f.playlist, f.tracks, f.err
}

func TestPreview_RequiresSourceAndIdentifier(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates(), Registry: adapters.NewRegistry(), Reviews: importreview.NewStore()}
	router := testRouter(sqlDB, http.MethodPost, "/import/preview", h.preview)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/preview", "", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for missing source/identifier", rec.Code)
	}
}

func TestPreview_RejectsUnregisteredSource(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, "http://127.0.0.1:1")

	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates(), Registry: adapters.NewRegistry(), Reviews: importreview.NewStore()}
	router := testRouter(sqlDB, http.MethodPost, "/import/preview", h.preview)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/preview", "source=nope&identifier=x", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unregistered source", rec.Code)
	}
}

func TestPreview_RendersReviewListFromMatchedAndUnmatched(t *testing.T) {
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/hubs/search":
			// searchByArtistFirst's artist lookup step (type=8 first, all
			// leaves next) - keep it simple: no matches, everything falls
			// through to unmatched, which is enough to exercise preview's
			// own review-list assembly regardless of matching's own
			// internal search strategy details (covered elsewhere).
			w.Write([]byte(`{"MediaContainer":{"Hub":[]}}`))
		default:
			w.Write([]byte(`{"MediaContainer":{"Metadata":[]}}`))
		}
	})
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	registry := adapters.NewRegistry()
	registry.RegisterSource(&fakeSource{
		name:     "fake",
		playlist: adapters.PlaylistInfo{ID: "pl-1", Name: "Fake Playlist", TrackCount: 1},
		tracks:   []adapters.TrackInfo{{Title: "Song One", Artist: "Artist A"}},
	})

	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates(), Registry: registry, Reviews: importreview.NewStore(), PlexAuth: auth.NewPlexClient("c", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodPost, "/import/preview", h.preview)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/preview", "source=fake&identifier=abc", "application/x-www-form-urlencoded")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Song One") {
		t.Errorf("missing the fetched track, got: %s", body)
	}
	if !strings.Contains(body, `value="Fake Playlist"`) {
		t.Errorf("missing the default playlist name, got: %s", body)
	}
	// A session must have been created for the review's follow-up requests
	// (skip/select/confirm) to find.
	if h.Reviews.Count() != 1 {
		t.Fatalf("want exactly one review session created, got %d", h.Reviews.Count())
	}
}

func TestReviewTrackSkip_TogglesAndReRenders(t *testing.T) {
	sqlDB := newTestDB(t)
	store := importreview.NewStore()
	sess := store.New("fake", "abc", "My Mix", "")
	sess.SetTracks([]importreview.Track{{Title: "A", Matched: true, PlexRatingKey: "t1"}})

	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates(), Reviews: store}
	router := testRouter(sqlDB, http.MethodPost, "/import/review/{sessionId}/track/{idx}/skip", h.reviewTrackSkip)
	user := newTestUser(t, sqlDB)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/review/"+sess.ID+"/track/0/skip", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !sess.Tracks()[0].Skipped {
		t.Error("track should be marked skipped after the toggle")
	}
	if !strings.Contains(rec.Body.String(), "Unskip") {
		t.Errorf("rendered row should now offer Unskip, got: %s", rec.Body.String())
	}
}

func TestReviewTrackSelect_UpdatesMatchAndClearsSkip(t *testing.T) {
	sqlDB := newTestDB(t)
	store := importreview.NewStore()
	sess := store.New("fake", "abc", "My Mix", "")
	sess.SetTracks([]importreview.Track{{Title: "A", Matched: false, Skipped: true}})

	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates(), Reviews: store}
	router := testRouter(sqlDB, http.MethodPost, "/import/review/{sessionId}/track/{idx}/select", h.reviewTrackSelect)
	user := newTestUser(t, sqlDB)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/review/"+sess.ID+"/track/0/select",
		"ratingKey=t9&title=Real+Song&artist=Real+Artist&album=Real+Album", "application/x-www-form-urlencoded")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got := sess.Tracks()[0]
	if !got.Matched || got.Skipped {
		t.Errorf("track = %+v, want Matched=true, Skipped=false after selecting a candidate", got)
	}
	if got.PlexRatingKey != "t9" || got.PlexTitle != "Real Song" {
		t.Errorf("track = %+v, want the selected candidate's fields applied", got)
	}
}

func TestConfirmImport_RequiresPlaylistName(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, "http://127.0.0.1:1")
	store := importreview.NewStore()
	sess := store.New("fake", "abc", "", "")
	sess.SetTracks([]importreview.Track{{Matched: true, PlexRatingKey: "t1"}})

	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates(), Reviews: store, Notifications: notifications.NewStore(), PlexAuth: auth.NewPlexClient("c", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodPost, "/import/confirm/{sessionId}", h.confirmImport)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/confirm/"+sess.ID, "", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a missing playlist name", rec.Code)
	}
}

func TestConfirmImport_RejectsAllTracksSkippedOrUnmatched(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, "http://127.0.0.1:1")
	store := importreview.NewStore()
	sess := store.New("fake", "abc", "My Mix", "")
	sess.SetTracks([]importreview.Track{
		{Matched: true, PlexRatingKey: "t1", Skipped: true},
		{Matched: false},
	})

	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates(), Reviews: store, Notifications: notifications.NewStore(), PlexAuth: auth.NewPlexClient("c", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodPost, "/import/confirm/{sessionId}", h.confirmImport)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/confirm/"+sess.ID, "playlistName=My+Mix", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when nothing usable remains", rec.Code)
	}
}

func TestConfirmImport_CreatesPlaylistFromEditedMatchesAndDeletesSession(t *testing.T) {
	var createdTitle string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/":
			w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine-1"}}`))
		case r.URL.Path == "/playlists" && r.Method == http.MethodGet:
			w.Write([]byte(`{"MediaContainer":{"Metadata":[]}}`)) // no existing playlist to overwrite
		case r.URL.Path == "/playlists" && r.Method == http.MethodPost:
			createdTitle = r.URL.Query().Get("title")
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"new-1","title":"` + createdTitle + `"}]}}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	store := importreview.NewStore()
	sess := store.New("fake", "abc", "My Mix", "")
	sess.SetTracks([]importreview.Track{
		{Title: "A", Matched: true, PlexRatingKey: "t1"},
		{Title: "B", Matched: false}, // unmatched, must be excluded from the created playlist
		{Title: "C", Matched: true, PlexRatingKey: "t3", Skipped: true}, // skipped, must be excluded
	})

	notifStore := notifications.NewStore()
	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates(), Reviews: store, Notifications: notifStore, PlexAuth: auth.NewPlexClient("c", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodPost, "/import/confirm/{sessionId}", h.confirmImport)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/confirm/"+sess.ID, "playlistName=My+Mix", "application/x-www-form-urlencoded")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if createdTitle != "My Mix" {
		t.Errorf("created playlist title = %q, want %q", createdTitle, "My Mix")
	}
	if _, ok := store.Get(sess.ID); ok {
		t.Error("review session should be deleted after a successful confirm")
	}
	list := notifStore.List(user.ID)
	if len(list) != 1 || list[0].Status != notifications.StatusSuccess {
		t.Fatalf("want one success notification, got %+v", list)
	}
	if !strings.Contains(list[0].Detail, "1 tracks") {
		t.Errorf("notification detail = %q, want it to report exactly 1 track added (unmatched/skipped excluded)", list[0].Detail)
	}
}

func TestConfirmImport_OverwriteExistingDeletesOldPlaylist(t *testing.T) {
	var deletedKeys []string
	var createCount int
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/":
			w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine-1"}}`))
		case r.URL.Path == "/playlists" && r.Method == http.MethodGet:
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"old-1","title":"My Mix","playlistType":"audio"}]}}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/playlists/"):
			deletedKeys = append(deletedKeys, strings.TrimPrefix(r.URL.Path, "/playlists/"))
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/playlists" && r.Method == http.MethodPost:
			createCount++
			title := r.URL.Query().Get("title")
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"new-1","title":"` + title + `"}]}}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	store := importreview.NewStore()
	sess := store.New("fake", "abc", "My Mix", "")
	sess.SetTracks([]importreview.Track{{Title: "A", Matched: true, PlexRatingKey: "t1"}})

	h := &ImportHandler{DB: sqlDB, Tmpl: nopTemplates(), Reviews: store, Notifications: notifications.NewStore(), PlexAuth: auth.NewPlexClient("c", "Playlist Lab")}
	router := testRouter(sqlDB, http.MethodPost, "/import/confirm/{sessionId}", h.confirmImport)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/import/confirm/"+sess.ID,
		"playlistName=My+Mix&overwriteExisting=true", "application/x-www-form-urlencoded")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(deletedKeys) != 1 || deletedKeys[0] != "old-1" {
		t.Errorf("deleted = %v, want exactly [\"old-1\"]", deletedKeys)
	}
	if createCount != 1 {
		t.Errorf("created %d playlists, want 1", createCount)
	}
}
