// pathtemplate.go computes where a downloaded track lands on disk, from
// the admin-configured naming templates/folder toggles - a trimmed port of
// deemix's utils/pathtemplates.js covering only what track and album
// downloads need (this app never queues a playlist download - see
// settings.go's Settings doc comment for why playlist-shaped settings are
// exposed but unused).
package deemix

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// sanitizeName strips characters illegal in filenames on common
// filesystems, replacing each with settings.IllegalCharacterReplacer.
func sanitizeName(name, replacement string) string {
	if replacement == "" {
		replacement = "_"
	}
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteString(replacement)
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func renderTemplate(tmpl string, fields map[string]string) string {
	out := tmpl
	for k, v := range fields {
		out = strings.ReplaceAll(out, "%"+k+"%", v)
	}
	return out
}

func padTrackNumber(n int, settings Settings) string {
	if !settings.PadTracks || n <= 0 {
		return strconv.Itoa(n)
	}
	width := 2
	if size, err := strconv.Atoi(settings.PaddingSize); err == nil && size > 0 {
		width = size
	}
	return fmt.Sprintf("%0*d", width, n)
}

// trackFilePath computes the on-disk destination for one track. isAlbumTrack
// selects between Settings.TracknameTemplate (a standalone single) and
// Settings.AlbumTracknameTemplate + the album/artist/CD folder toggles.
func trackFilePath(settings Settings, meta trackMeta, ext string, isAlbumTrack bool) string {
	dir := settings.DownloadLocation
	replacer := settings.IllegalCharacterReplacer

	artist := meta.AlbumArtist
	if artist == "" {
		artist = joinArtists(meta.Artists, "default")
	}
	artist = sanitizeName(artist, replacer)

	fields := map[string]string{
		"artist":      sanitizeName(joinArtists(meta.Artists, "default"), replacer),
		"title":       sanitizeName(meta.Title, replacer),
		"album":       sanitizeName(meta.Album, replacer),
		"tracknumber": padTrackNumber(meta.TrackNumber, settings),
	}

	if settings.CreateArtistFolder {
		name := sanitizeName(renderTemplate(settings.ArtistNameTemplate, map[string]string{"artist": artist}), replacer)
		if name != "" {
			dir = filepath.Join(dir, name)
		}
	}

	var filename string
	if isAlbumTrack {
		if settings.CreateAlbumFolder {
			name := sanitizeName(renderTemplate(settings.AlbumNameTemplate, map[string]string{"artist": artist, "album": fields["album"]}), replacer)
			if name != "" {
				dir = filepath.Join(dir, name)
			}
		}
		if settings.CreateCDFolder && meta.DiscNumber > 1 {
			dir = filepath.Join(dir, fmt.Sprintf("CD%d", meta.DiscNumber))
		}
		filename = sanitizeName(renderTemplate(settings.AlbumTracknameTemplate, fields), replacer)
	} else {
		filename = sanitizeName(renderTemplate(settings.TracknameTemplate, fields), replacer)
		if settings.CreateSingleFolder && filename != "" {
			dir = filepath.Join(dir, filename)
		}
	}

	if filename == "" {
		filename = "track_" + meta.SourceID
	}
	return filepath.Join(dir, filename+ext)
}
