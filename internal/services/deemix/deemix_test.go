package deemix

import (
	"fmt"
	"sync"
	"testing"
)

// TestQueueItem_SetAndGetRoundTrip covers the in-memory queue read/write
// path StartDownload's poller (trackDownload) and QueueDownload's
// already-queued check both depend on - replacing what used to be an HTTP
// round trip to a separate deemix-server process with a plain map, so the
// only thing left worth pinning here is that a write is visible to a
// concurrent reader and an unknown uuid reports "not found" rather than a
// zero value.
func TestQueueItem_SetAndGetRoundTrip(t *testing.T) {
	svc := New(Config{ARL: "test-arl"}, nil, nil)

	svc.setQueueItem("track_1_3", QueueItem{Status: "downloading", Progress: 42, Title: "A", Artist: "B"})

	item, err := svc.GetQueueItem("track_1_3")
	if err != nil {
		t.Fatalf("GetQueueItem: %v", err)
	}
	if item == nil || item.Progress != 42 || item.Status != "downloading" {
		t.Fatalf("GetQueueItem = %+v, want Progress=42 Status=downloading", item)
	}

	missing, err := svc.GetQueueItem("track_missing_3")
	if err != nil {
		t.Fatalf("GetQueueItem(missing): %v", err)
	}
	if missing != nil {
		t.Fatalf("GetQueueItem(missing) = %+v, want nil", missing)
	}
}

// TestQueueItem_ConcurrentAccess exercises the queue map under the race
// detector - every real caller (QueueDownload's already-queued check,
// download.go's progress reporting, trackDownload's poller) reads/writes it
// from a different goroutine.
func TestQueueItem_ConcurrentAccess(t *testing.T) {
	svc := New(Config{ARL: "test-arl"}, nil, nil)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uuid := fmt.Sprintf("track_%d_3", i)
			svc.setQueueItem(uuid, QueueItem{Status: "downloading", Progress: i})
			if _, err := svc.GetQueueItem(uuid); err != nil {
				t.Errorf("GetQueueItem(%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()
}

// TestParseTrackOrAlbumURL covers QueueDownload's URL parsing: the uuid it
// assigns a queued download is derived from the URL's own track/album id
// (deezer's own `${type}_${id}_${bitrate}` scheme), so getting this parse
// wrong breaks both the initial queue and the "already queued" lookup.
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
