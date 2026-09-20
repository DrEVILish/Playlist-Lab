// tag.go writes the metadata deemix/tagger.js's tagID3/tagFLAC write, using
// the same TagSettings toggles - the ID3v2 (MP3) and Vorbis-comment (FLAC)
// libraries used here are the Go ecosystem's standard ones for each format,
// not something worth hand-rolling.
package deemix

import (
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"

	"github.com/bogem/id3v2/v2"
	"github.com/go-flac/flacpicture/v2"
	"github.com/go-flac/flacvorbis/v2"
	flac "github.com/go-flac/go-flac/v2"
)

// trackMeta is the subset of a gwTrack (plus album/lyrics lookups) the
// tagger needs - deliberately flat rather than reusing gwTrack directly, so
// the tagger doesn't care whether its caller got this from a track or an
// album track-list entry.
type trackMeta struct {
	Title       string
	Artists     []string // Artists[0] is the main/credited artist
	Album       string
	AlbumArtist string
	TrackNumber int
	TrackTotal  int
	DiscNumber  int
	DiscTotal   int
	Year        string
	Date        string // full YYYY-MM-DD release date, for Settings.DateFormat
	Genre       string
	ISRC        string
	Barcode     string
	Label       string
	Copyright   string
	BPM         int
	ReplayGain  string // e.g. "-12.4 dB"
	Rating      byte   // 0-255, POPM/rating scale
	Composer    string
	// InvolvedPeople is role -> names, for the IPLS/TIPL frame - only the
	// roles tagger.js itself recognizes (author, engineer, mixer, producer,
	// writer, musicpublisher).
	InvolvedPeople map[string][]string
	Lyrics         string
	DurationSec    int
	Explicit       bool
	SourceID       string
	Cover          []byte // already-downloaded, already-format-converted embedded cover art, or nil
	CoverMIME      string // "image/jpeg" or "image/png" - matches Cover's actual encoding, see artwork.go's processCoverImage
	ArtistCover    []byte // already-downloaded artist image (JPEG), or nil - file-saving only, never embedded
}

// coverMIME defaults to JPEG when unset (callers that never went through
// processCoverImage, e.g. tests, leave CoverMIME blank).
func coverMIME(mime string) string {
	if mime == "" {
		return "image/jpeg"
	}
	return mime
}

func joinArtists(artists []string, separator string) string {
	if len(artists) == 0 {
		return ""
	}
	switch separator {
	case "", "default":
		return strings.Join(artists, ", ")
	case "nothing":
		return artists[0]
	default:
		return strings.Join(artists, separator)
	}
}

// splitISODate breaks a YYYY-MM-DD date into its components, or ("","","")
// if it's not in that shape.
func splitISODate(isoDate string) (year, month, day string) {
	parts := strings.SplitN(isoDate, "-", 3)
	if len(parts) != 3 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}

// formatDate renders a YYYY-MM-DD date under Settings.DateFormat's Y/M/D
// token template (e.g. "Y-M-D" or "D-M-Y") - a trimmed version of deemix's
// own Date.format, just the token substitution.
func formatDate(isoDate, format string) string {
	year, month, day := splitISODate(isoDate)
	if year == "" {
		return isoDate
	}
	if format == "" {
		format = "Y-M-D"
	}
	replacer := strings.NewReplacer("Y", year, "M", month, "D", day)
	return replacer.Replace(format)
}

// ddmmDate renders the ID3v2.3 TDAT frame's fixed DDMM format (§4.2.5) -
// not a user-configurable template like formatDate, so it doesn't share
// that token-replacement logic (a "D"/"M" token replacer would
// double-substitute on a literal "DDMM" string).
func ddmmDate(isoDate string) string {
	year, month, day := splitISODate(isoDate)
	if year == "" {
		return ""
	}
	return day + month
}

func tagFile(path, format string, meta trackMeta, tags TagSettings) error {
	if format == "FLAC" {
		return tagFLACFile(path, meta, tags)
	}
	return tagMP3File(path, meta, tags)
}

// ipls builds a raw ID3v2.3 IPLS ("Involved people list") frame body:
// an encoding byte followed by null-terminated [role, name] pairs. The
// id3v2 library has no typed frame for this (only common ones), so it's
// written as an UnknownFrame - the one place this package hand-encodes an
// ID3 frame instead of using the library's API.
func ipls(people map[string][]string) []byte {
	var b []byte
	b = append(b, 0x00) // ISO-8859-1
	for _, role := range []string{"author", "engineer", "mixer", "producer", "writer", "musicpublisher"} {
		for _, name := range people[role] {
			b = append(b, latin1Bytes(role)...)
			b = append(b, 0x00)
			b = append(b, latin1Bytes(name)...)
			b = append(b, 0x00)
		}
	}
	return b
}

func tagMP3File(path string, meta trackMeta, tags TagSettings) error {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: false})
	if err != nil {
		return fmt.Errorf("opening %s for tagging: %w", path, err)
	}
	defer tag.Close()
	tag.SetVersion(3)
	tag.SetDefaultEncoding(id3v2.EncodingUTF8)

	if tags.Title {
		tag.SetTitle(meta.Title)
	}
	if tags.Artist && len(meta.Artists) > 0 {
		tag.SetArtist(joinArtists(meta.Artists, tags.MultiArtistSeparator))
		if tags.Artists {
			for _, a := range meta.Artists {
				tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{
					Encoding: id3v2.EncodingUTF8, Description: "ARTISTS", Value: a,
				})
			}
		}
	}
	if tags.Album {
		tag.SetAlbum(meta.Album)
	}
	if tags.AlbumArtist && meta.AlbumArtist != "" {
		tag.AddTextFrame(tag.CommonID("Band/Orchestra/Accompaniment"), id3v2.EncodingUTF8, meta.AlbumArtist)
	}
	if tags.TrackNumber && meta.TrackNumber > 0 {
		v := strconv.Itoa(meta.TrackNumber)
		if tags.TrackTotal && meta.TrackTotal > 0 {
			v += "/" + strconv.Itoa(meta.TrackTotal)
		}
		tag.AddTextFrame(tag.CommonID("Track number/Position in set"), id3v2.EncodingUTF8, v)
	}
	if tags.DiscNumber && meta.DiscNumber > 0 {
		v := strconv.Itoa(meta.DiscNumber)
		if tags.DiscTotal && meta.DiscTotal > 0 {
			v += "/" + strconv.Itoa(meta.DiscTotal)
		}
		tag.AddTextFrame(tag.CommonID("Part of a set"), id3v2.EncodingUTF8, v)
	}
	if tags.Genre && meta.Genre != "" {
		tag.SetGenre(meta.Genre)
	}
	if tags.Year && meta.Year != "" {
		tag.SetYear(meta.Year)
	}
	if tags.Date && meta.Date != "" {
		if d := ddmmDate(meta.Date); d != "" {
			tag.AddTextFrame(tag.CommonID("Date"), id3v2.EncodingUTF8, d)
		}
	}
	if tags.ISRC && meta.ISRC != "" {
		tag.AddTextFrame(tag.CommonID("ISRC"), id3v2.EncodingUTF8, meta.ISRC)
	}
	if tags.Barcode && meta.Barcode != "" {
		tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{Encoding: id3v2.EncodingUTF8, Description: "BARCODE", Value: meta.Barcode})
	}
	if tags.Label && meta.Label != "" {
		tag.AddTextFrame(tag.CommonID("Publisher"), id3v2.EncodingUTF8, meta.Label)
	}
	if tags.Length && meta.DurationSec > 0 {
		tag.AddTextFrame(tag.CommonID("Length"), id3v2.EncodingUTF8, strconv.Itoa(meta.DurationSec*1000))
	}
	if tags.BPM && meta.BPM > 0 {
		tag.AddTextFrame(tag.CommonID("BPM"), id3v2.EncodingUTF8, strconv.Itoa(meta.BPM))
	}
	if tags.ReplayGain && meta.ReplayGain != "" {
		tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{Encoding: id3v2.EncodingUTF8, Description: "REPLAYGAIN_TRACK_GAIN", Value: meta.ReplayGain})
	}
	if tags.Copyright && meta.Copyright != "" {
		tag.AddTextFrame(tag.CommonID("Copyright message"), id3v2.EncodingUTF8, meta.Copyright)
	}
	if tags.Composer && meta.Composer != "" {
		tag.AddTextFrame(tag.CommonID("Composer"), id3v2.EncodingUTF8, meta.Composer)
	}
	if tags.InvolvedPeople && len(meta.InvolvedPeople) > 0 {
		if body := ipls(meta.InvolvedPeople); len(body) > 1 {
			tag.AddFrame("IPLS", id3v2.UnknownFrame{Body: body})
		}
	}
	if tags.Rating && meta.Rating > 0 {
		tag.AddFrame("POPM", id3v2.PopularimeterFrame{Email: "", Rating: meta.Rating, Counter: big.NewInt(0)})
	}
	if tags.Lyrics && meta.Lyrics != "" {
		tag.AddUnsynchronisedLyricsFrame(id3v2.UnsynchronisedLyricsFrame{
			Encoding: id3v2.EncodingUTF8, Language: "XXX", ContentDescriptor: "", Lyrics: meta.Lyrics,
		})
	}
	if tags.Explicit {
		v := "0"
		if meta.Explicit {
			v = "1"
		}
		tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{Encoding: id3v2.EncodingUTF8, Description: "ITUNESADVISORY", Value: v})
	}
	if tags.Source {
		tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{Encoding: id3v2.EncodingUTF8, Description: "SOURCE", Value: "Deezer"})
		tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{Encoding: id3v2.EncodingUTF8, Description: "SOURCEID", Value: meta.SourceID})
	}
	if tags.Cover && len(meta.Cover) > 0 {
		coverEncoding := id3v2.EncodingUTF8
		if !tags.CoverDescriptionUTF8 {
			coverEncoding = id3v2.EncodingISO
		}
		tag.AddAttachedPicture(id3v2.PictureFrame{
			Encoding: coverEncoding, MimeType: coverMIME(meta.CoverMIME), PictureType: 3, // "Cover (front)"
			Description: "cover", Picture: meta.Cover,
		})
	}
	if err := tag.Save(); err != nil {
		return err
	}
	if tags.SaveID3v1 {
		return appendID3v1(path, meta, tags)
	}
	return nil
}

// appendID3v1 appends a minimal 128-byte ID3v1 trailer (title/artist/album/
// year only - genre is always written as 0xFF/"unknown" rather than
// reimplementing ID3v1's fixed 191-entry genre table for a legacy format
// modern players fall back to only when no ID3v2 tag is present at all).
func appendID3v1(path string, meta trackMeta, tags TagSettings) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	var trailer [128]byte
	copy(trailer[0:3], "TAG")
	if tags.Title {
		copy(trailer[3:33], latin1Bytes(meta.Title))
	}
	if tags.Artist {
		copy(trailer[33:63], latin1Bytes(joinArtists(meta.Artists, tags.MultiArtistSeparator)))
	}
	if tags.Album {
		copy(trailer[63:93], latin1Bytes(meta.Album))
	}
	if tags.Year && len(meta.Year) >= 4 {
		copy(trailer[93:97], meta.Year[:4])
	}
	trailer[127] = 0xFF // genre: unknown

	if _, err := f.Seek(0, 2); err != nil {
		return err
	}
	_, err = f.Write(trailer[:])
	return err
}

func tagFLACFile(path string, meta trackMeta, tags TagSettings) error {
	f, err := flac.ParseFile(path)
	if err != nil {
		return fmt.Errorf("opening %s for tagging: %w", path, err)
	}

	comments := flacvorbis.New()
	add := func(key, val string) {
		if val != "" {
			_ = comments.Add(key, val)
		}
	}
	if tags.Title {
		add("TITLE", meta.Title)
	}
	if tags.Artist {
		if tags.MultiArtistSeparator == "default" && len(meta.Artists) > 0 {
			for _, a := range meta.Artists {
				add("ARTIST", a)
			}
		} else {
			add("ARTIST", joinArtists(meta.Artists, tags.MultiArtistSeparator))
		}
		if tags.Artists {
			for _, a := range meta.Artists {
				add("ARTISTS", a)
			}
		}
	}
	if tags.Album {
		add("ALBUM", meta.Album)
	}
	if tags.AlbumArtist {
		add("ALBUMARTIST", meta.AlbumArtist)
	}
	if tags.TrackNumber && meta.TrackNumber > 0 {
		add("TRACKNUMBER", strconv.Itoa(meta.TrackNumber))
	}
	if tags.TrackTotal && meta.TrackTotal > 0 {
		add("TRACKTOTAL", strconv.Itoa(meta.TrackTotal))
	}
	if tags.DiscNumber && meta.DiscNumber > 0 {
		add("DISCNUMBER", strconv.Itoa(meta.DiscNumber))
	}
	if tags.DiscTotal && meta.DiscTotal > 0 {
		add("DISCTOTAL", strconv.Itoa(meta.DiscTotal))
	}
	if tags.Genre {
		add("GENRE", meta.Genre)
	}
	// DATE subsumes YEAR when both are on (mirrors tagger.js: "YEAR tag is
	// not suggested as a standard tag - being YEAR already contained in
	// DATE will only use DATE instead").
	if tags.Date && meta.Date != "" {
		add("DATE", formatDate(meta.Date, "Y-M-D"))
	} else if tags.Year {
		add("DATE", meta.Year)
	}
	if tags.Length && meta.DurationSec > 0 {
		add("LENGTH", strconv.Itoa(meta.DurationSec*1000))
	}
	if tags.BPM && meta.BPM > 0 {
		add("BPM", strconv.Itoa(meta.BPM))
	}
	if tags.ReplayGain {
		add("REPLAYGAIN_TRACK_GAIN", meta.ReplayGain)
	}
	if tags.Label {
		add("PUBLISHER", meta.Label)
	}
	if tags.ISRC {
		add("ISRC", meta.ISRC)
	}
	if tags.Barcode {
		add("BARCODE", meta.Barcode)
	}
	if tags.Copyright {
		add("COPYRIGHT", meta.Copyright)
	}
	if tags.Composer {
		add("COMPOSER", meta.Composer)
	}
	if tags.InvolvedPeople {
		for _, role := range []string{"author", "engineer", "mixer", "producer", "writer"} {
			for _, name := range meta.InvolvedPeople[role] {
				add(strings.ToUpper(role), name)
			}
		}
		for _, name := range meta.InvolvedPeople["musicpublisher"] {
			add("ORGANIZATION", name)
		}
	}
	if tags.Lyrics {
		add("LYRICS", meta.Lyrics)
	}
	if tags.Rating && meta.Rating > 0 {
		add("RATING", strconv.Itoa(int(meta.Rating)))
	}
	if tags.Explicit {
		v := "0"
		if meta.Explicit {
			v = "1"
		}
		add("ITUNESADVISORY", v)
	}
	if tags.Source {
		add("SOURCE", "Deezer")
		add("SOURCEID", meta.SourceID)
	}

	// Drop any pre-existing VORBIS_COMMENT/PICTURE blocks before appending
	// the fresh ones built above - this file was just downloaded, but
	// ParseFile makes no assumption about that, so don't silently double up
	// blocks if it ever runs on an already-tagged file.
	kept := f.Meta[:0]
	for _, block := range f.Meta {
		if block.Type != flac.VorbisComment && block.Type != flac.Picture {
			kept = append(kept, block)
		}
	}
	f.Meta = kept

	commentBlock := comments.Marshal()
	f.Meta = append(f.Meta, &commentBlock)

	if tags.Cover && len(meta.Cover) > 0 {
		pic, err := flacpicture.NewFromImageData(flacpicture.PictureTypeFrontCover, "cover", meta.Cover, coverMIME(meta.CoverMIME))
		if err == nil {
			picBlock := pic.Marshal()
			f.Meta = append(f.Meta, &picBlock)
		}
	}

	return f.Save(path)
}
