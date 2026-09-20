package deemix

import (
	"database/sql"
	"encoding/json"

	"github.com/drevilish/playlist-lab/internal/db"
)

// TagSettings mirrors deemix-gui's settings.js DEFAULTS.tags - which ID3/
// Vorbis-comment fields get written into a downloaded file. Field names
// match the original JS keys so anyone who used deemix-gui recognizes them.
type TagSettings struct {
	Title                     bool   `json:"title"`
	Artist                    bool   `json:"artist"`
	Artists                   bool   `json:"artists"`
	Album                     bool   `json:"album"`
	Cover                     bool   `json:"cover"`
	TrackNumber               bool   `json:"trackNumber"`
	TrackTotal                bool   `json:"trackTotal"`
	DiscNumber                bool   `json:"discNumber"`
	DiscTotal                 bool   `json:"discTotal"`
	AlbumArtist               bool   `json:"albumArtist"`
	Genre                     bool   `json:"genre"`
	Year                      bool   `json:"year"`
	Date                      bool   `json:"date"`
	Explicit                  bool   `json:"explicit"`
	ISRC                      bool   `json:"isrc"`
	Length                    bool   `json:"length"`
	Barcode                   bool   `json:"barcode"`
	BPM                       bool   `json:"bpm"`
	ReplayGain                bool   `json:"replayGain"`
	Label                     bool   `json:"label"`
	Lyrics                    bool   `json:"lyrics"`
	SyncedLyrics              bool   `json:"syncedLyrics"`
	Copyright                 bool   `json:"copyright"`
	Composer                  bool   `json:"composer"`
	InvolvedPeople            bool   `json:"involvedPeople"`
	Source                    bool   `json:"source"`
	Rating                    bool   `json:"rating"`
	SavePlaylistAsCompilation bool   `json:"savePlaylistAsCompilation"`
	SaveID3v1                 bool   `json:"saveID3v1"`
	MultiArtistSeparator      string `json:"multiArtistSeparator"` // "default" | "nothing" | any literal separator string
	SingleAlbumArtist         bool   `json:"singleAlbumArtist"`
	CoverDescriptionUTF8      bool   `json:"coverDescriptionUTF8"`
}

// Settings mirrors deemix-gui's settings.js DEFAULTS - the full config
// surface the original app's Settings page exposed, kept under the same
// field names/semantics so the admin Deemix tab is a like-for-like port
// rather than a reduced one. Almost every field is fully wired into the
// download/tag pipeline (see download.go, pathtemplate.go, tag.go,
// transform.go, artwork.go, legacy.go). The only ones that are stored and
// shown but genuinely can't do anything in this app, and why:
//
//   - Playlist-shaped downloads never trigger (internal/handlers/admin.go's
//     missing-track flow only ever queues a track or album URL, never a
//     playlist one): createPlaylistFolder, playlistNameTemplate,
//     playlistTracknameTemplate, createStructurePlaylist,
//     playlistFilenameTemplate, createM3U8File, tags.savePlaylistAsCompilation.
//   - fallbackSearch/fallbackISRC: re-searching by metadata (or by ISRC)
//     when a direct Deezer ID lookup fails - this app's missing_tracks rows
//     carry no ISRC, and every download here already starts from a
//     FindBestMatches search result, so there's no "direct lookup" step
//     upstream of this to retry.
//   - tags.syncedLyrics: the id3v2 library this port uses has no SYLT frame
//     support, and hand-encoding one correctly (a binary sub-format with
//     its own timestamp encoding) isn't worth it for a frame few players
//     render - tags.lyrics (plain, unsynced) is fully wired instead, from
//     the same gw.song.getLyrics call.
//
// albumVariousArtists needs no code of its own: Deezer's own API already
// returns "Various Artists" as the artist name for compilation tracks, so
// AlbumArtist reflects it automatically without a special case.
type Settings struct {
	DownloadLocation          string      `json:"downloadLocation"`
	TracknameTemplate         string      `json:"tracknameTemplate"`
	AlbumTracknameTemplate    string      `json:"albumTracknameTemplate"`
	PlaylistTracknameTemplate string      `json:"playlistTracknameTemplate"`
	CreatePlaylistFolder      bool        `json:"createPlaylistFolder"`
	PlaylistNameTemplate      string      `json:"playlistNameTemplate"`
	CreateArtistFolder        bool        `json:"createArtistFolder"`
	ArtistNameTemplate        string      `json:"artistNameTemplate"`
	CreateAlbumFolder         bool        `json:"createAlbumFolder"`
	AlbumNameTemplate         string      `json:"albumNameTemplate"`
	CreateCDFolder            bool        `json:"createCDFolder"`
	CreateStructurePlaylist   bool        `json:"createStructurePlaylist"`
	CreateSingleFolder        bool        `json:"createSingleFolder"`
	PadTracks                 bool        `json:"padTracks"`
	PaddingSize               string      `json:"paddingSize"`
	IllegalCharacterReplacer  string      `json:"illegalCharacterReplacer"`
	QueueConcurrency          int         `json:"queueConcurrency"`
	MaxBitrate                string      `json:"maxBitrate"` // TrackFormats: "9"=FLAC "3"=MP3_320 "1"=MP3_128
	FeelingLucky              bool        `json:"feelingLucky"`
	FallbackBitrate           bool        `json:"fallbackBitrate"`
	FallbackSearch            bool        `json:"fallbackSearch"`
	FallbackISRC              bool        `json:"fallbackISRC"`
	LogErrors                 bool        `json:"logErrors"`
	LogSearched               bool        `json:"logSearched"`
	OverwriteFile             string      `json:"overwriteFile"` // "y"|"n"|"e"|"b"|"t" - see OverwriteOption consts
	CreateM3U8File            bool        `json:"createM3U8File"`
	PlaylistFilenameTemplate  string      `json:"playlistFilenameTemplate"`
	EmbeddedArtworkSize       int         `json:"embeddedArtworkSize"`
	EmbeddedArtworkPNG        bool        `json:"embeddedArtworkPNG"`
	LocalArtworkSize          int         `json:"localArtworkSize"`
	LocalArtworkFormat        string      `json:"localArtworkFormat"`
	SaveArtwork               bool        `json:"saveArtwork"`
	CoverImageTemplate        string      `json:"coverImageTemplate"`
	SaveArtworkArtist         bool        `json:"saveArtworkArtist"`
	ArtistImageTemplate       string      `json:"artistImageTemplate"`
	JpegImageQuality          int         `json:"jpegImageQuality"`
	DateFormat                string      `json:"dateFormat"`
	AlbumVariousArtists       bool        `json:"albumVariousArtists"`
	RemoveAlbumVersion        bool        `json:"removeAlbumVersion"`
	RemoveDuplicateArtists    bool        `json:"removeDuplicateArtists"`
	FeaturedToTitle           string      `json:"featuredToTitle"` // "0"|"1"|"2"|"3" - see FeaturesOption consts
	TitleCasing               string      `json:"titleCasing"`
	ArtistCasing              string      `json:"artistCasing"`
	ExecuteCommand            string      `json:"executeCommand"`
	Tags                      TagSettings `json:"tags"`
}

// Overwrite options, mirroring deemix-gui's settings.js OverwriteOption.
const (
	OverwriteYes          = "y"
	OverwriteNo           = "n"
	OverwriteDontCheckExt = "e"
	OverwriteKeepBoth     = "b"
	OverwriteOnlyTags     = "t"
)

// Featured-artist title options, mirroring settings.js FeaturesOption.
const (
	FeaturesNoChange         = "0"
	FeaturesRemoveTitle      = "1"
	FeaturesMoveTitle        = "2"
	FeaturesRemoveTitleAlbum = "3"
)

// Bitrate values, mirroring deezer-js's TrackFormats.
const (
	BitrateFLAC    = "9"
	BitrateMP3_320 = "3"
	BitrateMP3_128 = "1"
)

// DefaultSettings mirrors settings.js's DEFAULTS - used whenever no admin
// config row exists yet (first run) or a saved blob fails to parse.
func DefaultSettings() Settings {
	return Settings{
		DownloadLocation:          "/downloads",
		TracknameTemplate:         "%artist% - %title%",
		AlbumTracknameTemplate:    "%tracknumber% - %title%",
		PlaylistTracknameTemplate: "%position% - %artist% - %title%",
		CreatePlaylistFolder:      true,
		PlaylistNameTemplate:      "%playlist%",
		CreateArtistFolder:        false,
		ArtistNameTemplate:        "%artist%",
		CreateAlbumFolder:         true,
		AlbumNameTemplate:         "%artist% - %album%",
		CreateCDFolder:            true,
		CreateStructurePlaylist:   false,
		CreateSingleFolder:        false,
		PadTracks:                 true,
		PaddingSize:               "0",
		IllegalCharacterReplacer:  "_",
		QueueConcurrency:          3,
		MaxBitrate:                BitrateMP3_320,
		FeelingLucky:              false,
		FallbackBitrate:           false,
		FallbackSearch:            false,
		FallbackISRC:              false,
		LogErrors:                 true,
		LogSearched:               false,
		OverwriteFile:             OverwriteNo,
		CreateM3U8File:            false,
		PlaylistFilenameTemplate:  "playlist",
		EmbeddedArtworkSize:       800,
		EmbeddedArtworkPNG:        false,
		LocalArtworkSize:          1200,
		LocalArtworkFormat:        "jpg",
		SaveArtwork:               true,
		CoverImageTemplate:        "cover",
		SaveArtworkArtist:         false,
		ArtistImageTemplate:       "folder",
		JpegImageQuality:          90,
		DateFormat:                "Y-M-D",
		AlbumVariousArtists:       true,
		RemoveAlbumVersion:        false,
		RemoveDuplicateArtists:    true,
		FeaturedToTitle:           FeaturesNoChange,
		TitleCasing:               "nothing",
		ArtistCasing:              "nothing",
		ExecuteCommand:            "",
		Tags: TagSettings{
			Title: true, Artist: true, Artists: true, Album: true, Cover: true,
			TrackNumber: true, TrackTotal: false, DiscNumber: true, DiscTotal: false,
			AlbumArtist: true, Genre: true, Year: true, Date: true, Explicit: false,
			ISRC: true, Length: true, Barcode: true, BPM: true, ReplayGain: false,
			Label: true, Lyrics: false, SyncedLyrics: false, Copyright: false,
			Composer: false, InvolvedPeople: false, Source: false, Rating: false,
			SavePlaylistAsCompilation: false, SaveID3v1: true,
			MultiArtistSeparator: "default", SingleAlbumArtist: false,
			CoverDescriptionUTF8: false,
		},
	}
}

const adminConfigKeySettings = "deemix_settings"

// LoadSettings reads the admin-saved settings blob, falling back to
// DefaultSettings on first run or a corrupt/missing row - mirroring
// settings.js's load()'s never-fail-startup behavior.
func LoadSettings(sqlDB *sql.DB) Settings {
	raw, ok, err := db.GetAdminConfig(sqlDB, adminConfigKeySettings)
	if err != nil || !ok {
		return DefaultSettings()
	}
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return DefaultSettings()
	}
	return s
}

// SaveSettings persists the whole settings blob as one admin_config row.
func SaveSettings(sqlDB *sql.DB, s Settings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return db.SetAdminConfig(sqlDB, adminConfigKeySettings, string(raw))
}
