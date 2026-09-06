// Package fileimport ports the file-format parsers import.ts's POST
// /api/import/file route dispatches to (parseM3UFile/parseCSVFile/
// parsePLSFile/parseXSPFFile in services/scrapers.ts): turn an uploaded
// playlist file's raw content into a name + track list, dispatched by file
// extension exactly as the Node original does (case '.csv'/'.pls'/'.xspf',
// default M3U/M3U8/TXT).
package fileimport

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/drevilish/playlist-lab/internal/adapters"
)

// Track mirrors ExternalTrack for these parsers' purposes.
type Track = adapters.TrackInfo

// Result mirrors ExternalPlaylist's fields as produced by each parseXFile().
type Result struct {
	Name   string
	Tracks []Track
}

// AllowedExtensions ports the multer fileFilter's allowedExtensions list
// (routes/import.ts) - every format this app can also export a playlist to,
// plus .txt for playlists exported as plain text by other tools.
var AllowedExtensions = []string{".m3u", ".m3u8", ".pls", ".xspf", ".csv", ".txt"}

func IsAllowedExtension(filename string) bool {
	ext := extOf(filename)
	for _, a := range AllowedExtensions {
		if ext == a {
			return true
		}
	}
	return false
}

func extOf(filename string) string {
	filename = strings.ToLower(filename)
	if i := strings.LastIndexByte(filename, '.'); i >= 0 {
		return filename[i:]
	}
	return ""
}

func nameWithoutExt(filename string) string {
	if i := strings.LastIndexByte(filename, '.'); i >= 0 {
		return filename[:i]
	}
	return filename
}

// Parse dispatches on filename's extension, matching the switch in
// import.ts's scrapePlaylist() 'file' case.
func Parse(content, filename string) (Result, error) {
	switch extOf(filename) {
	case ".csv":
		return parseCSV(content, filename)
	case ".pls":
		return parsePLS(content, filename)
	case ".xspf":
		return parseXSPF(content, filename)
	default: // .m3u, .m3u8, .txt
		return parseM3U(content, filename)
	}
}

func splitLines(content string) []string {
	return strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
}

// extinfPattern extracts the comma-separated info after #EXTINF:duration,.
var extinfPattern = regexp.MustCompile(`^#EXTINF:[^,]*,(.+)$`)

// splitPair splits "A - B" on the LAST " - ", same as info.lastIndexOf(' - ')
// in the original (so a dash inside the title itself doesn't split early).
func splitPair(s string) (before, after string, ok bool) {
	idx := strings.LastIndex(s, " - ")
	if idx <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+3:]), true
}

var bracketPattern = regexp.MustCompile(`[(\[{]`)

// detectPairFormat ports the detectPairFormat() closure in parseM3UFile:
// true = Apple format (Title - Artist), false = standard (Artist - Title),
// nil (ok=false) = ambiguous, caller falls back to the file-level default.
func detectPairFormat(before, after string) (appleFormat bool, ok bool) {
	afterSimple := !bracketPattern.MatchString(after) && len(after) < 50
	beforeComplex := bracketPattern.MatchString(before)
	if afterSimple && beforeComplex {
		return true, true
	}
	if !afterSimple {
		return false, true
	}
	return false, false
}

// parseM3U ports parseM3UFile (scrapers.ts:1141): #EXTINF lines carry
// "Artist - Title" or "Title - Artist" (auto-detected per-file, with a
// per-pair override), non-comment lines are file paths that close out the
// preceding EXTINF entry or, lacking one, get their own artist/title guessed
// from the filename.
func parseM3U(content, filename string) (Result, error) {
	lines := splitLines(content)

	// Sample the first 30 lines' EXTINF entries to pick a file-level
	// default format, same as the original's two-pass approach.
	var appleVotes, standardVotes int
	sampled := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#EXTINF:") {
			continue
		}
		sampled++
		if sampled > 30 {
			break
		}
		if sampled > 10 {
			continue
		}
		m := extinfPattern.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		before, after, ok := splitPair(strings.TrimSpace(m[1]))
		if !ok {
			continue
		}
		if apple, ok := detectPairFormat(before, after); ok {
			if apple {
				appleVotes++
			} else {
				standardVotes++
			}
		}
	}
	useAppleDefault := appleVotes > standardVotes

	var tracks []Track
	var curTitle, curArtist string

	resolvePair := func(before, after string) (title, artist string) {
		apple, ok := detectPairFormat(before, after)
		if !ok {
			apple = useAppleDefault
		}
		if apple {
			return before, after
		}
		return after, before
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || (strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "#EXTINF")) {
			continue
		}
		if strings.HasPrefix(trimmed, "#EXTINF") {
			m := extinfPattern.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}
			info := strings.TrimSpace(m[1])
			if before, after, ok := splitPair(info); ok {
				curTitle, curArtist = resolvePair(before, after)
			} else {
				curTitle, curArtist = info, "Unknown"
			}
			continue
		}
		// A file path/URL line: close out the pending EXTINF entry, or
		// (no EXTINF metadata) guess artist/title from the filename.
		if curTitle != "" {
			tracks = append(tracks, Track{Title: curTitle, Artist: orUnknown(curArtist)})
			curTitle, curArtist = "", ""
			continue
		}
		base := trimmed
		if i := strings.LastIndexAny(base, `/\`); i >= 0 {
			base = base[i+1:]
		}
		stem := nameWithoutExt(base)
		if len(stem) < 3 || strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
			continue
		}
		if before, after, ok := splitPair(stem); ok {
			title, artist := resolvePair(before, after)
			tracks = append(tracks, Track{Title: title, Artist: artist})
		} else {
			tracks = append(tracks, Track{Title: stem, Artist: "Unknown"})
		}
	}

	if len(tracks) == 0 {
		return Result{}, fmt.Errorf("no tracks found in file. Please ensure the file is a valid M3U/M3U8 playlist with track information")
	}
	return Result{Name: nameWithoutExt(filename), Tracks: tracks}, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "Unknown"
	}
	return s
}

// parseCSV ports parseCSVFile (scrapers.ts:1324): a header row naming
// Track/Title, Artist, and (optionally) Album columns - matching this app's
// own CSV export - falling back to column position 0/1 for a headerless or
// unrecognized-header file.
func parseCSV(content, filename string) (Result, error) {
	r := csv.NewReader(strings.NewReader(content))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return Result{}, fmt.Errorf("failed to parse CSV file: %w", err)
	}
	if len(rows) == 0 {
		return Result{}, fmt.Errorf("CSV file is empty")
	}

	titleIdx, artistIdx, albumIdx := -1, -1, -1
	for i, h := range rows[0] {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "track", "title":
			titleIdx = i
		case "artist":
			artistIdx = i
		case "album":
			albumIdx = i
		}
	}

	dataRows := rows
	if titleIdx >= 0 || artistIdx >= 0 {
		dataRows = rows[1:]
	}

	var tracks []Track
	cell := func(row []string, idx int) string {
		if idx >= 0 && idx < len(row) {
			return strings.TrimSpace(row[idx])
		}
		return ""
	}
	for _, row := range dataRows {
		title := cell(row, titleIdx)
		if titleIdx < 0 {
			title = cell(row, 0)
		}
		artist := cell(row, artistIdx)
		if artistIdx < 0 {
			artist = cell(row, 1)
		}
		album := cell(row, albumIdx)
		if title != "" {
			tracks = append(tracks, Track{Title: title, Artist: orUnknown(artist), Album: album})
		}
	}

	if len(tracks) == 0 {
		return Result{}, fmt.Errorf(`no tracks found in CSV file. Expected a "Track"/"Title" column and an "Artist" column`)
	}
	return Result{Name: nameWithoutExt(filename), Tracks: tracks}, nil
}

var plsTitlePattern = regexp.MustCompile(`(?i)^Title(\d+)=(.+)$`)

// parsePLS ports parsePLSFile (scrapers.ts:1394): "TitleN=Artist - Title"
// INI-style entries, ordered by N.
func parsePLS(content, filename string) (Result, error) {
	type numbered struct {
		n    int
		info string
	}
	var entries []numbered
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		if m := plsTitlePattern.FindStringSubmatch(strings.TrimRight(scanner.Text(), "\r")); m != nil {
			var n int
			fmt.Sscanf(m[1], "%d", &n)
			entries = append(entries, numbered{n: n, info: strings.TrimSpace(m[2])})
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].n < entries[j].n })

	var tracks []Track
	for _, e := range entries {
		if before, after, ok := splitPair(e.info); ok {
			tracks = append(tracks, Track{Artist: before, Title: after})
		} else {
			tracks = append(tracks, Track{Title: e.info, Artist: "Unknown"})
		}
	}

	if len(tracks) == 0 {
		return Result{}, fmt.Errorf(`no tracks found in PLS file. Expected "TitleN=Artist - Title" entries`)
	}
	return Result{Name: nameWithoutExt(filename), Tracks: tracks}, nil
}

var (
	xspfTrackPattern = regexp.MustCompile(`(?is)<track>.*?</track>`)
	xspfTitlePattern = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
)

func xspfTag(block, tag string) string {
	m := regexp.MustCompile(`(?is)<` + tag + `>(.*?)</` + tag + `>`).FindStringSubmatch(block)
	if m == nil {
		return ""
	}
	return unescapeXML(strings.TrimSpace(m[1]))
}

func unescapeXML(s string) string {
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
	return r.Replace(s)
}

// parseXSPF ports parseXSPFFile (scrapers.ts:1437): one <track> block per
// entry, with <title>/<creator>/<album> children.
func parseXSPF(content, filename string) (Result, error) {
	blocks := xspfTrackPattern.FindAllString(content, -1)
	var tracks []Track
	for _, block := range blocks {
		title := xspfTag(block, "title")
		if title == "" {
			continue
		}
		tracks = append(tracks, Track{
			Title:  title,
			Artist: orUnknown(xspfTag(block, "creator")),
			Album:  xspfTag(block, "album"),
		})
	}

	if len(tracks) == 0 {
		return Result{}, fmt.Errorf("no tracks found in XSPF file. Expected <track> entries with a <title>")
	}

	name := nameWithoutExt(filename)
	if m := xspfTitlePattern.FindStringSubmatch(content); m != nil {
		if t := unescapeXML(strings.TrimSpace(m[1])); t != "" {
			name = t
		}
	}
	return Result{Name: name, Tracks: tracks}, nil
}
