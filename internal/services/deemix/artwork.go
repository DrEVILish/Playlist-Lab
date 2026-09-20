// artwork.go covers Settings' image-handling knobs beyond the plain
// embedded cover already wired in download.go: converting the embedded
// cover to PNG / a specific JPEG quality, and saving separate cover/artist
// image files alongside a download. Both formats decode/encode through the
// stdlib (image/jpeg, image/png) - no new dependency needed for either.
package deemix

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
)

// processCoverImage re-encodes raw (a JPEG straight off Deezer's CDN) as
// PNG if asPNG is set, else as JPEG at jpegQuality (1-100). Returns the
// processed bytes and the MIME type they're actually encoded as - always
// use the returned MIME, not an assumed one, when embedding or saving it.
// Falls back to the original bytes/"image/jpeg" if decoding fails, so a
// CDN response this package can't parse doesn't kill the whole download.
func processCoverImage(raw []byte, asPNG bool, jpegQuality int) ([]byte, string) {
	if len(raw) == 0 {
		return nil, "image/jpeg"
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return raw, "image/jpeg"
	}

	var buf bytes.Buffer
	if asPNG {
		if err := png.Encode(&buf, img); err != nil {
			return raw, "image/jpeg"
		}
		return buf.Bytes(), "image/png"
	}

	q := jpegQuality
	if q <= 0 || q > 100 {
		q = 90
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
		return raw, "image/jpeg"
	}
	return buf.Bytes(), "image/jpeg"
}

// localImageExt maps Settings.LocalArtworkFormat to a file extension,
// defaulting to jpg for anything unrecognized.
func localImageExt(format string) string {
	switch format {
	case "png":
		return ".png"
	default:
		return ".jpg"
	}
}

// saveImageFile writes image bytes next to a download (dir/name.ext),
// converting to PNG first if the configured format calls for it. Failures
// are logged by the caller, not returned as fatal - a missing artwork file
// shouldn't fail the track/album download it's decorating.
func saveImageFile(dir, name string, raw []byte, format string) error {
	if len(raw) == 0 {
		return nil
	}
	ext := localImageExt(format)
	data := raw
	if ext == ".png" {
		converted, mime := processCoverImage(raw, true, 0)
		if mime == "image/png" {
			data = converted
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+ext), data, 0o644)
}

// fetchArtistImage downloads a track's main artist image at the given
// pixel size, using the ART_PICTURE hash song.getData already returned
// (see gwTrack.artistPictureHash) - same CDN URL shape as album covers,
// just under /images/artist/ instead of /images/cover/.
func fetchArtistImage(pictureHash string, size int) []byte {
	if pictureHash == "" {
		return nil
	}
	if size <= 0 {
		size = 1200
	}
	url := fmt.Sprintf("https://e-cdns-images.dzcdn.net/images/artist/%s/%dx%d-000000-80-0-0.jpg", pictureHash, size, size)
	return fetchImageBytes(url)
}
