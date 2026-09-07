package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

// waitForNotification polls until userID's most recent notification leaves
// StatusInProgress (the action queue runs the handler on a goroutine, so a
// 202 Accepted response race-ahead of the work finishing), or fails the test
// after a generous timeout - these are in-memory, httptest-backed jobs with
// no real network latency, so a stuck notification means the job is broken,
// not slow.
func waitForNotification(t *testing.T, store *notifications.Store, userID int64) notifications.Notification {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		list := store.List(userID)
		if len(list) > 0 && list[0].Status != notifications.StatusInProgress {
			return list[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("notification never left in-progress status")
	return notifications.Notification{}
}

func TestShuffle_ReordersRealItemsOnly(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	var moveCalls []string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/playlists/rk-1/items":
			writePlexTracks(w, []fakePlexTrack{
				{RatingKey: "t1", PlaylistItemID: 1, Title: "A"},
				{RatingKey: "t2", PlaylistItemID: 2, Title: "B"},
				{RatingKey: "", PlaylistItemID: 0, Title: "Smart item, no PlaylistItemID"},
			})
		case r.Method == http.MethodPut:
			moveCalls = append(moveCalls, r.URL.Path+"?"+r.URL.RawQuery)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	store := notifications.NewStore()
	h := newPlaylistsHandler(sqlDB)
	h.Notifications = store
	h.Queue = actionqueue.New(store)

	router := testRouter(sqlDB, http.MethodPost, "/playlists/{plexId}/shuffle", h.shuffle)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/playlists/rk-1/shuffle", "", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}

	n := waitForNotification(t, store, user.ID)
	if n.Status != notifications.StatusSuccess {
		t.Fatalf("notification status = %q, want success (detail: %s)", n.Status, n.Detail)
	}
	// Only the 2 real items get a move call; the smart-playlist item (no
	// PlaylistItemID) must never be passed to MovePlaylistItem.
	if len(moveCalls) != 2 {
		t.Fatalf("got %d move calls, want 2 (smart item must be excluded): %v", len(moveCalls), moveCalls)
	}
}

func TestSortTracks_RejectsUnknownField(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, "http://127.0.0.1:1")

	h := newPlaylistsHandler(sqlDB)
	h.Notifications = notifications.NewStore()
	h.Queue = actionqueue.New(h.Notifications)

	router := testRouter(sqlDB, http.MethodPost, "/playlists/{plexId}/sort", h.sortTracks)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/playlists/rk-1/sort", "by=nonsense", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unrecognized sort field", rec.Code)
	}
}

func TestSortTracks_SortsByYearDescending(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	var order []string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/playlists/rk-1/items":
			writePlexTracks(w, []fakePlexTrack{
				{RatingKey: "old", PlaylistItemID: 1, Title: "Old Song", Year: 1990},
				{RatingKey: "new", PlaylistItemID: 2, Title: "New Song", Year: 2020},
				{RatingKey: "mid", PlaylistItemID: 3, Title: "Mid Song", Year: 2005},
			})
		case r.Method == http.MethodPut:
			order = append(order, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	store := notifications.NewStore()
	h := newPlaylistsHandler(sqlDB)
	h.Notifications = store
	h.Queue = actionqueue.New(store)

	router := testRouter(sqlDB, http.MethodPost, "/playlists/{plexId}/sort", h.sortTracks)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/playlists/rk-1/sort", "by=year&direction=desc", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	n := waitForNotification(t, store, user.ID)
	if n.Status != notifications.StatusSuccess {
		t.Fatalf("notification status = %q, want success (detail: %s)", n.Status, n.Detail)
	}
	want := []string{
		"/playlists/rk-1/items/2/move", // new (2020) moved after "0" (first)
		"/playlists/rk-1/items/3/move", // mid (2005) moved after new
		"/playlists/rk-1/items/1/move", // old (1990) moved after mid
	}
	if len(order) != len(want) {
		t.Fatalf("got %d move calls %v, want %v", len(order), order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("move[%d] = %q, want %q (year-descending order)", i, order[i], want[i])
		}
	}
}

func TestDedupe_RemovesRepeatsKeepingFirstOccurrence(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	var removed []string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/playlists/rk-1/items" && r.Method == http.MethodGet:
			writePlexTracks(w, []fakePlexTrack{
				{RatingKey: "dup", PlaylistItemID: 1, Title: "Same Song"},
				{RatingKey: "other", PlaylistItemID: 2, Title: "Different Song"},
				{RatingKey: "dup", PlaylistItemID: 3, Title: "Same Song"}, // repeat, later occurrence
			})
		case r.Method == http.MethodDelete:
			removed = append(removed, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	store := notifications.NewStore()
	h := newPlaylistsHandler(sqlDB)
	h.Notifications = store
	h.Queue = actionqueue.New(store)

	router := testRouter(sqlDB, http.MethodPost, "/playlists/{plexId}/dedupe", h.dedupe)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/playlists/rk-1/dedupe", "", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	n := waitForNotification(t, store, user.ID)
	if n.Status != notifications.StatusSuccess {
		t.Fatalf("notification status = %q, want success (detail: %s)", n.Status, n.Detail)
	}
	if len(removed) != 1 || removed[0] != "/playlists/rk-1/items/3" {
		t.Fatalf("removed = %v, want exactly the second (repeat) occurrence, playlistItemID 3", removed)
	}
}

func TestRename_UpdatesPlexAndTrackedRow(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	var gotQuery string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			gotQuery = r.URL.RawQuery
		}
		w.WriteHeader(http.StatusOK)
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	tracked, err := db.CreatePlaylistRow(sqlDB, user.ID, "rk-1", "Old Name", "spotify", "")
	if err != nil {
		t.Fatalf("CreatePlaylistRow: %v", err)
	}

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodPut, "/playlists/{plexId}/rename", h.rename)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPut, "/playlists/rk-1/rename", "name=New+Name", "application/x-www-form-urlencoded")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if gotQuery != "title=New+Name" {
		t.Errorf("Plex rename query = %q, want title=New+Name", gotQuery)
	}
	got, err := db.GetPlaylistByID(sqlDB, tracked.ID)
	if err != nil || got == nil {
		t.Fatalf("GetPlaylistByID: %v", err)
	}
	if got.Name != "New Name" {
		t.Errorf("tracked row name = %q, want %q", got.Name, "New Name")
	}
}

func TestRename_RejectsEmptyName(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, "http://127.0.0.1:1")

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodPut, "/playlists/{plexId}/rename", h.rename)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPut, "/playlists/rk-1/rename", "name=", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an empty name", rec.Code)
	}
}

func TestSplit_CreatesNewPlaylistFromSelectedTracks(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	var createdName string
	var addedTrackURI string
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			// GetMachineIdentifier
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine-1"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/playlists":
			createdName = r.URL.Query().Get("title")
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"new-1","title":"` + createdName + `"}]}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/playlists/new-1/items":
			// AddToPlaylist's batch add - uri is a comma-joined
			// server://.../library/metadata/<key1>,<key2> URI.
			addedTrackURI = r.URL.Query().Get("uri")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	store := notifications.NewStore()
	h := newPlaylistsHandler(sqlDB)
	h.Notifications = store
	h.Queue = actionqueue.New(store)

	router := testRouter(sqlDB, http.MethodPost, "/playlists/{plexId}/split", h.split)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/playlists/rk-1/split",
		"name=Split+Off&trackId=t1&trackId=t2", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	n := waitForNotification(t, store, user.ID)
	if n.Status != notifications.StatusSuccess {
		t.Fatalf("notification status = %q, want success (detail: %s)", n.Status, n.Detail)
	}
	if createdName != "Split Off" {
		t.Errorf("created playlist name = %q, want %q", createdName, "Split Off")
	}
	if !strings.Contains(addedTrackURI, "t1") || !strings.Contains(addedTrackURI, "t2") {
		t.Errorf("added track URI = %q, want it to reference both t1 and t2", addedTrackURI)
	}
}

// fakePlexTrack/writePlexTracks build the minimal Plex "Metadata" shape
// GetPlaylistTracks parses.
type fakePlexTrack struct {
	RatingKey      string
	PlaylistItemID int
	Title          string
	Year           int
}

func writePlexTracks(w http.ResponseWriter, tracks []fakePlexTrack) {
	w.Header().Set("Content-Type", "application/json")
	items := make([]map[string]any, len(tracks))
	for i, t := range tracks {
		m := map[string]any{"ratingKey": t.RatingKey, "title": t.Title}
		if t.PlaylistItemID != 0 {
			m["playlistItemID"] = t.PlaylistItemID
		}
		if t.Year != 0 {
			m["year"] = t.Year
		}
		items[i] = m
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"Metadata": items}})
}

func TestUploadCover_RelaysBytesToPlex(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	var gotContentType string
	var gotBody []byte
	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/library/metadata/rk-1/posters" {
			gotContentType = r.Header.Get("Content-Type")
			gotBody, _ = io.ReadAll(r.Body)
		}
		w.WriteHeader(http.StatusOK)
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	// CreateFormFile always writes Content-Type: application/octet-stream
	// regardless of filename, so a real image/png upload is built via
	// CreatePart with an explicit header instead, matching what a browser's
	// <input type="file"> actually sends for an image.
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="cover"; filename="cover.png"`},
		"Content-Type":        {"image/png"},
	})
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	fakeImageBytes := []byte("\x89PNG\r\n\x1a\nfake-image-data")
	part.Write(fakeImageBytes)
	mw.Close()

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodPost, "/playlists/{plexId}/cover", h.uploadCover)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/playlists/rk-1/cover", body.String(), mw.FormDataContentType())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(gotBody, fakeImageBytes) {
		t.Errorf("bytes relayed to Plex = %q, want %q", gotBody, fakeImageBytes)
	}
	if gotContentType != "image/png" {
		t.Errorf("Content-Type relayed = %q, want image/png", gotContentType)
	}
}

func TestUploadCover_RejectsMissingFile(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, "http://127.0.0.1:1")

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.Close()

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodPost, "/playlists/{plexId}/cover", h.uploadCover)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/playlists/rk-1/cover", body.String(), mw.FormDataContentType())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a request with no file", rec.Code)
	}
}
