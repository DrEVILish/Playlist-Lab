package deemix

import "testing"

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
