// Playlist export, ported from routes/export.ts: write a playlist's tracks
// out as a downloadable file in one of six formats.
//
// Not ported: the Node route's EXPORT_QUEUE_THRESHOLD (playlists over 2000
// tracks built the file in the background via the action queue, then served
// it from a separate GET .../download endpoint once ready). That threshold
// existed to keep a large in-memory string build from blocking Node's
// single-threaded event loop for every other request. Go handlers run one
// per goroutine, so a slow export - the GetPlaylistTracks call to Plex, not
// the string building itself, is the only part that takes real time -
// blocks only its own request, not the server. The pendingExports map/TTL
// and the extra route this bought in Node bought nothing here, so this
// route always replies with the finished file directly, however large.
package handlers

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

func RegisterExport(r chi.Router, mw *auth.Middleware, h *PlaylistsHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Post("/playlists/{plexId}/export", h.export)
	})
}

var exportFormats = map[string]string{
	"m3u":  "audio/x-mpegurl",
	"m3u8": "application/vnd.apple.mpegurl",
	"pls":  "audio/x-scpls",
	"xspf": "application/xspf+xml",
	"csv":  "text/csv",
	"txt":  "text/plain",
}

func (h *PlaylistsHandler) export(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	_ = r.ParseForm()
	format := r.FormValue("format")
	contentType, ok := exportFormats[format]
	if !ok {
		names := make([]string, 0, len(exportFormats))
		for k := range exportFormats {
			names = append(names, k)
		}
		http.Error(w, "format must be one of: "+strings.Join(names, ", "), http.StatusBadRequest)
		return
	}
	// "relative" only affects M3U/M3U8 (generateM3U's own `relative` flag in
	// the original); every other format always wrote absolute paths, ported
	// as-is below.
	relative := r.FormValue("pathType") == "relative"

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}

	name := plexID
	if playlists, err := client.GetPlaylists(); err == nil {
		for _, p := range playlists {
			if p.RatingKey == plexID {
				name = p.Title
				break
			}
		}
	}

	tracks, err := client.GetPlaylistTracks(plexID)
	if err != nil {
		http.Error(w, "Failed to load playlist tracks", http.StatusBadGateway)
		return
	}

	content := buildExportContent(tracks, name, format, relative)
	filename := sanitizeFilename(name) + "." + format
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Write([]byte(content))
}

func buildExportContent(tracks []plex.Track, playlistName, format string, relative bool) string {
	switch format {
	case "m3u", "m3u8":
		return generateM3U(tracks, playlistName, relative)
	case "pls":
		return generatePLS(tracks, playlistName)
	case "xspf":
		return generateXSPF(tracks, playlistName)
	case "csv":
		return generateCSV(tracks)
	case "txt":
		return generateTXT(tracks)
	default:
		return ""
	}
}

// exportArtist prefers the track-level artist (OriginalTitle) over the
// album/grandparent artist, ports export.ts's getTrackArtist - deliberately
// its own function rather than reusing Track.DisplayArtist(), which also
// treats an OriginalTitle equal to GrandparentTitle as "no better answer";
// export.ts's getTrackArtist has no such equality check, so this matches it
// exactly rather than introducing a behavior difference in exported files.
func exportArtist(t plex.Track) string {
	if t.OriginalTitle != "" {
		return t.OriginalTitle
	}
	if t.GrandparentTitle != "" {
		return t.GrandparentTitle
	}
	return "Unknown Artist"
}

func exportTitle(t plex.Track) string {
	if t.Title != "" {
		return t.Title
	}
	return "Unknown Track"
}

// exportPath is export.ts's getTrackPath: relative rewrites a leading "/"
// to "../" (the on-disk path as Plex sees it, relative to Plex's own
// library root, going up one level to wherever the exported file is
// expected to sit alongside a copy of that library layout) - a blunt
// transform ported unchanged from the original rather than reworked, since
// there's no way to know a given user's actual playback-device layout to
// do better.
func exportPath(t plex.Track, relative bool) string {
	path := t.FilePath()
	if path == "" {
		return ""
	}
	if relative {
		return "../" + strings.TrimPrefix(path, "/")
	}
	return path
}

func generateM3U(tracks []plex.Track, playlistName string, relative bool) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	fmt.Fprintf(&b, "#PLAYLIST:%s\n", playlistName)
	for _, t := range tracks {
		seconds := t.Duration / 1000
		fmt.Fprintf(&b, "#EXTINF:%d,%s - %s\n", seconds, exportArtist(t), exportTitle(t))
		fmt.Fprintf(&b, "%s\n", exportPath(t, relative))
	}
	return b.String()
}

func generatePLS(tracks []plex.Track, playlistName string) string {
	var b strings.Builder
	b.WriteString("[playlist]\n")
	fmt.Fprintf(&b, "PlaylistName=%s\n", playlistName)
	fmt.Fprintf(&b, "NumberOfEntries=%d\n\n", len(tracks))
	for i, t := range tracks {
		num := i + 1
		// export.ts's PLS title falls back to plain "Unknown" rather than
		// "Unknown Track" (every other format's fallback, via exportTitle) -
		// kept as its own inline fallback to match that inconsistency
		// exactly rather than silently "fixing" it into a different output.
		title := t.Title
		if title == "" {
			title = "Unknown"
		}
		fmt.Fprintf(&b, "File%d=%s\n", num, exportPath(t, false))
		fmt.Fprintf(&b, "Title%d=%s - %s\n", num, exportArtist(t), title)
		fmt.Fprintf(&b, "Length%d=%d\n\n", num, t.Duration/1000)
	}
	b.WriteString("Version=2\n")
	return b.String()
}

func generateXSPF(tracks []plex.Track, playlistName string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<playlist version="1" xmlns="http://xspf.org/ns/0/">` + "\n")
	fmt.Fprintf(&b, "  <title>%s</title>\n", xspfEscape(playlistName))
	b.WriteString("  <trackList>\n")
	for _, t := range tracks {
		album := t.ParentTitle
		if album == "" {
			album = "Unknown Album"
		}
		b.WriteString("    <track>\n")
		fmt.Fprintf(&b, "      <location>file://%s</location>\n", xspfEscape(exportPath(t, false)))
		fmt.Fprintf(&b, "      <title>%s</title>\n", xspfEscape(exportTitle(t)))
		fmt.Fprintf(&b, "      <creator>%s</creator>\n", xspfEscape(exportArtist(t)))
		fmt.Fprintf(&b, "      <album>%s</album>\n", xspfEscape(album))
		fmt.Fprintf(&b, "      <duration>%d</duration>\n", t.Duration)
		if t.Index != 0 {
			fmt.Fprintf(&b, "      <trackNum>%d</trackNum>\n", t.Index)
		}
		b.WriteString("    </track>\n")
	}
	b.WriteString("  </trackList>\n")
	b.WriteString("</playlist>\n")
	return b.String()
}

func xspfEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

// generateCSV uses encoding/csv rather than porting export.ts's hand-rolled
// escapeCSV: the stdlib writer already handles quoting/escaping correctly
// per RFC 4180, including the embedded-newline case escapeCSV's simple
// "contains a comma, quote, or newline" check also caught, so there's no
// reason to hand-write it again.
func generateCSV(tracks []plex.Track) string {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	writer.Write([]string{"Track", "Artist", "Album", "Duration", "File Path"})
	for _, t := range tracks {
		album := t.ParentTitle
		if album == "" {
			album = "Unknown Album"
		}
		writer.Write([]string{
			exportTitle(t), exportArtist(t), album,
			formatDuration(t.Duration), exportPath(t, false),
		})
	}
	writer.Flush()
	return buf.String()
}

func formatDuration(ms int64) string {
	seconds := ms / 1000
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

// generateTXT matches this app's own .txt importer (fileimport.go's plain-
// text fallback path) so a playlist exported to .txt round-trips through
// re-import unchanged.
func generateTXT(tracks []plex.Track) string {
	var b strings.Builder
	for _, t := range tracks {
		fmt.Fprintf(&b, "%s - %s\n", exportArtist(t), exportTitle(t))
	}
	return b.String()
}

var unsafeFilenameChars = regexp.MustCompile(`[<>:"/\\|?*]`)
var whitespaceRun = regexp.MustCompile(`\s+`)

func sanitizeFilename(name string) string {
	name = unsafeFilenameChars.ReplaceAllString(name, "_")
	name = whitespaceRun.ReplaceAllString(name, "_")
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}
