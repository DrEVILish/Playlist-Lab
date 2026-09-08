package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/services/plex"
)

func sampleExportTracks() []plex.Track {
	return []plex.Track{
		{Title: "Get Lucky", OriginalTitle: "Daft Punk", ParentTitle: "Random Access Memories", Duration: 248000, Index: 1},
		// No OriginalTitle: falls back to GrandparentTitle, and no title at
		// all: falls back to "Unknown Track" (or PLS's own "Unknown").
		{Title: "", GrandparentTitle: "Unknown Artist Inc", ParentTitle: "", Duration: 0},
	}
}

func TestGenerateM3U_IncludesHeaderAndPerTrackEXTINF(t *testing.T) {
	got := generateM3U(sampleExportTracks(), "My Mix", false)
	if !strings.HasPrefix(got, "#EXTM3U\n#PLAYLIST:My Mix\n") {
		t.Fatalf("missing M3U header, got:\n%s", got)
	}
	if !strings.Contains(got, "#EXTINF:248,Daft Punk - Get Lucky\n") {
		t.Errorf("missing expected EXTINF line, got:\n%s", got)
	}
	if !strings.Contains(got, "#EXTINF:0,Unknown Artist Inc - Unknown Track\n") {
		t.Errorf("missing fallback-artist/title EXTINF line, got:\n%s", got)
	}
}

func TestExportPath_NoFileIsEmpty(t *testing.T) {
	if got := exportPath(plex.Track{Title: "T"}, true); got != "" {
		t.Fatalf("exportPath with no Media/Part = %q, want empty", got)
	}
}

func TestExportPath_RelativeRewritesLeadingSlash(t *testing.T) {
	track := trackWithFile("/music/Artist/Album/01 Song.flac")
	if got := exportPath(track, false); got != "/music/Artist/Album/01 Song.flac" {
		t.Errorf("absolute path = %q, want unchanged", got)
	}
	if got := exportPath(track, true); got != "../music/Artist/Album/01 Song.flac" {
		t.Errorf("relative path = %q, want leading slash rewritten to ../", got)
	}
}

func TestGeneratePLS_UsesPlainUnknownFallback(t *testing.T) {
	got := generatePLS(sampleExportTracks(), "My Mix")
	if !strings.Contains(got, "PlaylistName=My Mix\n") {
		t.Errorf("missing PlaylistName line, got:\n%s", got)
	}
	if !strings.Contains(got, "NumberOfEntries=2\n") {
		t.Errorf("missing NumberOfEntries line, got:\n%s", got)
	}
	// PLS's title fallback is bare "Unknown", not "Unknown Track" - matches
	// export.ts's own inconsistency between formats, ported deliberately.
	if !strings.Contains(got, "Title2=Unknown Artist Inc - Unknown\n") {
		t.Errorf("want PLS's own Unknown fallback (not \"Unknown Track\"), got:\n%s", got)
	}
	if !strings.HasSuffix(got, "Version=2\n") {
		t.Errorf("missing trailing Version=2, got:\n%s", got)
	}
}

func TestGenerateXSPF_EscapesXMLSpecialChars(t *testing.T) {
	tracks := []plex.Track{{Title: `Rock & Roll "Anthem"`, OriginalTitle: "AC/DC", Duration: 1000}}
	got := generateXSPF(tracks, "M&Ms Mix")
	if !strings.Contains(got, "<title>M&amp;Ms Mix</title>") {
		t.Errorf("playlist title not escaped, got:\n%s", got)
	}
	if !strings.Contains(got, "Rock &amp; Roll &quot;Anthem&quot;") {
		t.Errorf("track title not escaped, got:\n%s", got)
	}
	if !strings.Contains(got, "<duration>1000</duration>") {
		t.Errorf("missing duration in milliseconds (not seconds, unlike M3U/PLS), got:\n%s", got)
	}
}

func TestGenerateCSV_QuotesFieldsContainingCommas(t *testing.T) {
	tracks := []plex.Track{{Title: "Song, Part 1", OriginalTitle: "Artist, The", Duration: 90000}}
	got := generateCSV(tracks)
	if !strings.Contains(got, `"Song, Part 1"`) {
		t.Errorf("comma-containing title not quoted per RFC 4180, got:\n%s", got)
	}
	if !strings.Contains(got, "1:30") {
		t.Errorf("missing MM:SS duration, got:\n%s", got)
	}
}

func TestGenerateTXT_MatchesArtistDashTitleFormat(t *testing.T) {
	got := generateTXT(sampleExportTracks())
	want := "Daft Punk - Get Lucky\nUnknown Artist Inc - Unknown Track\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestSanitizeFilename_StripsUnsafeCharsAndSpaces(t *testing.T) {
	got := sanitizeFilename(`My: "Best" Mix / 2024?`)
	if strings.ContainsAny(got, `<>:"/\|?*`) {
		t.Errorf("filename %q still contains an unsafe character", got)
	}
	if strings.Contains(got, " ") {
		t.Errorf("filename %q still contains a space", got)
	}
}

func TestExportHandler_RejectsUnknownFormat(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	seedUserServer(t, sqlDB, user.ID, "http://127.0.0.1:1")

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodPost, "/playlists/{plexId}/export", h.export)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/playlists/rk-1/export", "format=exe", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unsupported format", rec.Code)
	}
}

func TestExportHandler_ServesFileWithCorrectHeaders(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)

	srv := newFakePlexServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/items"):
			writePlexTracks(w, []fakePlexTrack{{RatingKey: "t1", PlaylistItemID: 1, Title: "Song One"}})
		case r.URL.Path == "/playlists":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"rk-1","title":"Export Me"}]}}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	seedUserServer(t, sqlDB, user.ID, srv.URL)

	h := newPlaylistsHandler(sqlDB)
	router := testRouter(sqlDB, http.MethodPost, "/playlists/{plexId}/export", h.export)
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/playlists/rk-1/export", "format=txt", "application/x-www-form-urlencoded")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="Export_Me.txt"`) {
		t.Errorf("Content-Disposition = %q, want a sanitized filename", got)
	}
	if !strings.Contains(rec.Body.String(), "Song One") {
		t.Errorf("body missing the track, got: %s", rec.Body.String())
	}
}

// trackWithFile builds a Track whose FilePath() resolves through the real
// Media/Part JSON shape, by round-tripping through the same unmarshal
// GetPlaylistTracks uses - simpler than hand-constructing the unexported
// media/part struct literals from this package.
func trackWithFile(path string) plex.Track {
	var t plex.Track
	raw, _ := json.Marshal(map[string]any{
		"title": "T",
		"Media": []map[string]any{{"Part": []map[string]any{{"file": path}}}},
	})
	_ = json.Unmarshal(raw, &t)
	return t
}
