package deemix

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// newTestService points a Service at an httptest server instead of a real
// deemix-server install.
func newTestService(t *testing.T, handler http.HandlerFunc) *Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{URL: srv.URL, ARL: "test-arl"}, nil, nil)
}

// TestGetQueueSharesSnapshotAcrossConcurrentCallers pins the fix
// deemix-queue-polling.test.ts calls "serves many concurrent pollers from a
// single queue fetch": deemix-server has no per-item status endpoint, so
// every in-flight download polling independently multiplies request volume
// by N. getQueue must serve every caller within the TTL window from one
// shared in-flight fetch.
func TestGetQueueSharesSnapshotAcrossConcurrentCallers(t *testing.T) {
	var calls int32
	var mu sync.Mutex
	svc := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		queue := map[string]QueueItem{}
		for i := 0; i < 50; i++ {
			queue[fmt.Sprintf("track_%d_3", i)] = QueueItem{Status: "downloading", Progress: 42}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"queue": queue})
	})

	var wg sync.WaitGroup
	results := make([]*QueueItem, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			item, err := svc.GetQueueItem(fmt.Sprintf("track_%d_3", i))
			if err != nil {
				t.Errorf("GetQueueItem(%d) error: %v", i, err)
				return
			}
			results[i] = item
		}(i)
	}
	wg.Wait()

	for i, item := range results {
		if item == nil || item.Progress != 42 {
			t.Fatalf("result %d: expected progress 42, got %+v", i, item)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected exactly 1 upstream request for 50 concurrent pollers, got %d", calls)
	}
}

// TestGetQueueRefetchesAfterTTL covers the flip side: once the snapshot goes
// stale, the next call must hit deemix-server again rather than serving a
// frozen snapshot forever (progress has to be able to move).
func TestGetQueueRefetchesAfterTTL(t *testing.T) {
	var calls int32
	var mu sync.Mutex
	svc := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"queue": map[string]QueueItem{"track_1_3": {Status: "downloading", Progress: 10}},
		})
	})

	if _, err := svc.GetQueueItem("track_1_3"); err != nil {
		t.Fatalf("first GetQueueItem: %v", err)
	}
	if _, err := svc.GetQueueItem("track_1_3"); err != nil {
		t.Fatalf("second GetQueueItem: %v", err)
	}
	mu.Lock()
	if calls != 1 {
		mu.Unlock()
		t.Fatalf("expected snapshot to be reused within the TTL, got %d upstream calls", calls)
	}
	mu.Unlock()

	// Force the cached snapshot to look stale without a real 2s sleep.
	svc.queueMu.Lock()
	svc.snapshot.at = time.Now().Add(-queueSnapshotTTL - time.Second)
	svc.queueMu.Unlock()

	if _, err := svc.GetQueueItem("track_1_3"); err != nil {
		t.Fatalf("third GetQueueItem: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("expected a refetch once the snapshot is past its TTL, got %d upstream calls", calls)
	}
}

// TestParseTrackOrAlbumURL covers QueueDownload's fallback path: when
// deemix-server reports an empty queue-add result, the uuid it would have
// used is derived from the URL's own track/album id so the existing queue
// entry can be looked up directly. Getting this parse wrong silently breaks
// that fallback (falls through to "deemix could not queue" even though the
// download is actually already queued and progressing).
func TestParseTrackOrAlbumURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantTyp string
		wantID  string
		wantOK  bool
	}{
		{"track url", "https://www.deezer.com/track/123456", "track", "123456", true},
		{"album url", "https://www.deezer.com/album/987", "album", "987", true},
		{"track with trailing slash", "https://www.deezer.com/track/42/", "track", "42", true},
		{"track with query string", "https://www.deezer.com/track/42?utm=foo", "track", "42", true},
		{"non-numeric id rejected", "https://www.deezer.com/track/abc", "", "", false},
		{"unrelated url", "https://www.deezer.com/artist/1", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typ, id, ok := parseTrackOrAlbumURL(tt.url)
			if ok != tt.wantOK || typ != tt.wantTyp || id != tt.wantID {
				t.Fatalf("parseTrackOrAlbumURL(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.url, typ, id, ok, tt.wantTyp, tt.wantID, tt.wantOK)
			}
		})
	}
}

// TestResolveDownloadURL_AlbumVsSingle covers the queue-URL decision that
// determines whether a whole album downloads (and lands as a proper
// Artist/Album folder) or just the one matched track: queueing the album
// for a single-track release would drag in nothing extra but a wasted
// lookup, while queueing just-the-track for a real multi-track release
// silently drops the rest of the collection the user would have wanted.
func TestResolveDownloadURL_AlbumVsSingle(t *testing.T) {
	tests := []struct {
		name         string
		albumID      int
		trackCount   int
		countKnown   bool
		wantAlbumURL bool
	}{
		{"no album metadata at all", 0, 0, false, false},
		{"single-track release", 55, 1, true, false},
		{"real multi-track album", 55, 12, true, true},
		{"track-count lookup failed", 55, 0, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match := SearchResult{Link: "https://www.deezer.com/track/1"}
			match.Album.ID = tt.albumID

			got := resolveDownloadURL(match, tt.trackCount, tt.countKnown)

			gotIsAlbum := got != match.Link
			if gotIsAlbum != tt.wantAlbumURL {
				t.Fatalf("resolveDownloadURL(...) = %q (isAlbum=%v), want isAlbum=%v", got, gotIsAlbum, tt.wantAlbumURL)
			}
		})
	}
}
