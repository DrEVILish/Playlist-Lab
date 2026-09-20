// transform.go is the small text-processing settings deemix-gui exposed:
// casing, "(Album Version)" stripping, and moving/removing featured-artist
// credits from a title. Each is applied to trackMeta right after it's built
// (see download.go's downloadOneTrack), before the file is named or tagged,
// so both see the same transformed text.
package deemix

import (
	"regexp"
	"strings"
	"unicode"
)

// applyCasing implements Settings.TitleCasing/ArtistCasing. "start" is
// title-case (first letter of each word); anything else falls through to
// "nothing" (unchanged) rather than erroring on an unrecognized value.
func applyCasing(s, casing string) string {
	switch casing {
	case "upper":
		return strings.ToUpper(s)
	case "lower":
		return strings.ToLower(s)
	case "start":
		return startCase(s)
	default:
		return s
	}
}

func startCase(s string) string {
	var b strings.Builder
	atWordStart := true
	for _, r := range s {
		if unicode.IsSpace(r) {
			atWordStart = true
			b.WriteRune(r)
			continue
		}
		if atWordStart {
			b.WriteRune(unicode.ToUpper(r))
			atWordStart = false
		} else {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

var albumVersionPattern = regexp.MustCompile(`\s*\(Album Version\)`)

// removeAlbumVersionSuffix implements Settings.RemoveAlbumVersion.
func removeAlbumVersionSuffix(title string) string {
	return strings.TrimSpace(albumVersionPattern.ReplaceAllString(title, ""))
}

var featPattern = regexp.MustCompile(`(?i)\s*[\(\[]feat\.?.*?[\)\]]`)

// removeFeatures strips a "(feat. X)"/"[ft. X]"-shaped suffix from a title.
func removeFeatures(title string) string {
	return strings.TrimSpace(featPattern.ReplaceAllString(title, ""))
}

// applyFeaturedToTitle implements Settings.FeaturedToTitle, using the
// "featuring" role deemix's own SNG_CONTRIBUTORS carries (mirrors
// tagger.js's getCleanTitle/getFeatTitle). No-ops safely if there's no
// featured-artist credit to move, so it's harmless to call unconditionally.
func applyFeaturedToTitle(title, album string, featuring []string, mode string) (newTitle, newAlbum string) {
	switch mode {
	case FeaturesRemoveTitle:
		return removeFeatures(title), album
	case FeaturesRemoveTitleAlbum:
		return removeFeatures(title), removeFeatures(album)
	case FeaturesMoveTitle:
		if len(featuring) == 0 || strings.Contains(strings.ToLower(title), "feat.") {
			return title, album
		}
		return title + " (feat. " + strings.Join(featuring, ", ") + ")", album
	default:
		return title, album
	}
}

// computeRating maps Deezer's RANK (0-~1,000,000+) onto the POPM/rating
// byte scale (tagger.js: `rank = (RANK/10000)*2.55`, capped at 255).
func computeRating(rank int) byte {
	if rank <= 0 {
		return 0
	}
	v := (float64(rank) / 10000) * 2.55
	if v > 255 {
		v = 255
	}
	return byte(v)
}
