// download.go is QueueDownload's actual work: resolving a track's (or every
// track in an album's) download URL, streaming and decrypting it, tagging
// it, and writing it to disk - replacing what used to be a separate
// deemix-server process's job. deemix.go keeps the public Service API and
// the queue-state bookkeeping these goroutines report progress into.
package deemix

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// formatForBitrate maps a Settings.MaxBitrate value to the format string
// Deezer's get_url API expects.
func formatForBitrate(bitrate string) string {
	switch bitrate {
	case BitrateFLAC:
		return "FLAC"
	case BitrateMP3_128:
		return "MP3_128"
	default:
		return "MP3_320"
	}
}

func extForFormat(format string) string {
	if format == "FLAC" {
		return ".flac"
	}
	return ".mp3"
}

// fallbackChain lists the formats to try in order: just the preferred one,
// unless Settings.FallbackBitrate allows stepping down to a lower quality
// the account can actually stream at (e.g. a non-HiFi account requesting
// FLAC falls back to MP3_320 rather than failing outright).
func fallbackChain(preferred string, allowFallback bool) []string {
	all := []string{"FLAC", "MP3_320", "MP3_128"}
	start := 0
	for i, f := range all {
		if f == preferred {
			start = i
			break
		}
	}
	if !allowFallback {
		return []string{all[start]}
	}
	return all[start:]
}

// buildTrackMeta assembles trackMeta from a gwTrack, fetching whatever
// supplementary data (album genre/label/copyright/track+disc totals, BPM,
// lyrics) the enabled tag settings actually need - each is its own network
// call, so this only makes the ones a currently-off toggle wouldn't use
// anyway.
func (s *Service) buildTrackMeta(track gwTrack, settings Settings, album albumAssets) trackMeta {
	trackNum, _ := track.TRACK_NUMBER.Int64()
	discNum, _ := track.DISK_NUMBER.Int64()
	durationSec, _ := track.DURATION.Int64()
	explicitCode, _ := track.EXPLICIT_LYRICS.Int64()
	rank, _ := track.RANK.Int64()
	year, date := "", ""
	if len(track.PHYSICAL_RELEASE_DATE) >= 4 {
		year, date = track.PHYSICAL_RELEASE_DATE[:4], track.PHYSICAL_RELEASE_DATE
	}

	featuring := track.SNG_CONTRIBUTORS["featuring"]
	artists := []string{track.ART_NAME}
	artists = append(artists, featuring...)

	meta := trackMeta{
		Title: track.SNG_TITLE, Artists: artists, Album: track.ALB_TITLE, AlbumArtist: track.ART_NAME,
		TrackNumber: int(trackNum), DiscNumber: int(discNum), Year: year, Date: date,
		ISRC: track.ISRC, DurationSec: int(durationSec), Explicit: explicitCode == 1,
		SourceID: track.SNG_ID.String(), InvolvedPeople: track.SNG_CONTRIBUTORS,
	}
	if composer := track.SNG_CONTRIBUTORS["composer"]; len(composer) > 0 {
		meta.Composer = strings.Join(composer, ", ")
	}
	if settings.Tags.ReplayGain && track.GAIN != "" {
		meta.ReplayGain = track.GAIN + " dB"
	}
	if settings.Tags.Rating {
		meta.Rating = computeRating(int(rank))
	}

	meta.Genre, meta.Barcode = album.public.Genre, album.public.Barcode
	meta.Label, meta.Copyright = album.private.LABEL_NAME, album.private.COPYRIGHT
	trackTotal, _ := album.private.NUMBER_TRACK.Int64()
	discTotal, _ := album.private.NUMBER_DISK.Int64()
	meta.TrackTotal, meta.DiscTotal = int(trackTotal), int(discTotal)
	meta.Cover, meta.CoverMIME = album.cover, album.coverMIME
	if settings.Tags.BPM {
		meta.BPM = fetchBPM(track.SNG_ID.String())
	}
	if settings.Tags.Lyrics && track.LYRICS_ID.String() != "" && track.LYRICS_ID.String() != "0" {
		if lyrics, err := s.dz.getLyrics(track.SNG_ID.String()); err == nil {
			meta.Lyrics = lyrics.LYRICS_TEXT
		}
	}

	if settings.TitleCasing != "" && settings.TitleCasing != "nothing" {
		meta.Title = applyCasing(meta.Title, settings.TitleCasing)
	}
	if settings.ArtistCasing != "" && settings.ArtistCasing != "nothing" {
		for i := range meta.Artists {
			meta.Artists[i] = applyCasing(meta.Artists[i], settings.ArtistCasing)
		}
		meta.AlbumArtist = applyCasing(meta.AlbumArtist, settings.ArtistCasing)
	}
	if settings.RemoveDuplicateArtists {
		unique := meta.Artists[:0]
		for _, artist := range meta.Artists {
			if !slices.Contains(unique, artist) {
				unique = append(unique, artist)
			}
		}
		meta.Artists = unique
	}
	meta.FormattedDate = formatDate(meta.Date, settings.DateFormat)
	if settings.RemoveAlbumVersion {
		meta.Title = removeAlbumVersionSuffix(meta.Title)
	}
	if settings.FeaturedToTitle != "" && settings.FeaturedToTitle != FeaturesNoChange {
		meta.Title, meta.Album = applyFeaturedToTitle(meta.Title, meta.Album, featuring, settings.FeaturedToTitle)
	}

	return meta
}

type albumAssets struct {
	public        publicAlbumInfo
	private       gwAlbum
	cover         []byte
	coverMIME     string
	separateCover []byte
}

// Album-level data is shared for one download job, including both artwork sizes.
func (s *Service) albumAssetsFor(settings Settings) func(gwTrack) albumAssets {
	var byAlbum sync.Map
	return func(track gwTrack) albumAssets {
		key := [2]string{track.ALB_ID.String(), track.ALB_PICTURE}
		load := sync.OnceValue(func() albumAssets {
			var album albumAssets
			if settings.Tags.Genre || settings.Tags.Barcode {
				album.public = fetchPublicAlbumInfo(key[0])
			}
			if settings.Tags.Label || settings.Tags.Copyright || settings.Tags.TrackTotal || settings.Tags.DiscTotal {
				if data, err := s.dz.getAlbum(key[0]); err == nil {
					album.private = *data
				} else {
					slog.Warn("[Deemix] album metadata lookup failed", "albumId", key[0], "error", err)
				}
			}
			if settings.Tags.Cover {
				raw := fetchCover(key[1], settings.EmbeddedArtworkSize)
				album.cover, album.coverMIME = processCoverImage(raw, settings.EmbeddedArtworkPNG, settings.JpegImageQuality)
				if settings.SaveArtwork && settings.LocalArtworkSize == settings.EmbeddedArtworkSize {
					album.separateCover = raw
				}
			}
			if settings.SaveArtwork && album.separateCover == nil {
				album.separateCover = fetchCover(key[1], settings.LocalArtworkSize)
			}
			return album
		})
		cached, _ := byAlbum.LoadOrStore(key, load)
		return cached.(func() albumAssets)()
	}
}

// fetchCover downloads a track/album's cover art at the given pixel size -
// Deezer serves any size via the same URL shape, so no resizing is needed
// on this end. Best-effort: a failure here shouldn't fail the whole
// download, just leave the file uncovered.
func fetchCover(md5Image string, size int) []byte {
	if md5Image == "" {
		return nil
	}
	if size <= 0 {
		size = 800
	}
	url := fmt.Sprintf("https://e-cdns-images.dzcdn.net/images/cover/%s/%dx%d-000000-80-0-0.jpg", md5Image, size, size)
	return fetchImageBytes(url)
}

// fetchImageBytes is fetchCover/fetchArtistImage's shared GET-and-read-body
// step - best-effort, nil on any failure.
func fetchImageBytes(url string) []byte {
	resp, err := metadataClient.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	return data
}

// progressReader wraps a Reader, reporting cumulative bytes read after
// every chunk - decryptStream reads through it as it streams+decrypts, so
// this is the one place download progress can be observed from outside.
type progressReader struct {
	r      io.Reader
	read   int64
	err    error
	onRead func(read int64)
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	p.read += int64(n)
	if err != nil && err != io.EOF {
		p.err = err
	}
	if p.onRead != nil {
		p.onRead(p.read)
	}
	return n, err
}

// downloadAndDecrypt streams url (a Deezer BF_CBC_STRIPE-ciphered track
// URL) straight into destPath, decrypting as it goes so the whole file
// never needs to fit in memory.
func downloadAndDecrypt(url, trackID string, dst io.Writer, onProgress func(read, total int64)) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", dzUserAgent)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading track: status %d", resp.StatusCode)
	}

	total := resp.ContentLength
	src := &progressReader{r: resp.Body}
	if onProgress != nil {
		src.onRead = func(read int64) { onProgress(read, total) }
	}
	if err := decryptStream(dst, src, trackID); err != nil {
		return err
	}
	if src.err != nil {
		return fmt.Errorf("reading track download: %w", src.err)
	}
	if src.read == 0 || total >= 0 && src.read != total {
		return fmt.Errorf("incomplete track download: received %d bytes, expected %d", src.read, total)
	}
	return nil
}

// downloadOneTrack resolves track's download URL (falling back down the
// bitrate chain, and to track.FALLBACK's alternate release, same as
// deemix's own getPreferredBitrate/Track.checkAndRenewTrackToken combo)
// then downloads, decrypts, and tags it. Returns the final file path.
func (s *Service) downloadOneTrack(track gwTrack, bitrate string, settings Settings, albumTrack bool, loadAlbum func(gwTrack) albumAssets, onProgress func(read, total int64)) (string, error) {
	preferred := formatForBitrate(bitrate)
	chain := fallbackChain(preferred, settings.FallbackBitrate)

	var downloadURL, usedFormat string
	var lastErr error
	for _, format := range chain {
		u, err := s.dz.getTrackURL(track.TRACK_TOKEN, format)
		if err == nil && u != "" {
			downloadURL, usedFormat = u, format
			break
		}
		lastErr = err
	}

	if downloadURL == "" && settings.FeelingLucky {
		if u, ok := legacyCryptedStreamURL(track.SNG_ID.String(), track.MD5_ORIGIN, track.MEDIA_VERSION.String(), bitrate); ok {
			slog.Info("[Deemix] get_url failed, trying the legacy CDN URL scheme (feelingLucky)", "trackId", track.SNG_ID.String())
			downloadURL, usedFormat = u, formatForBitrate(bitrate)
		}
	}

	if downloadURL == "" {
		if track.FALLBACK != nil {
			if fbID := track.FALLBACK.SNG_ID.String(); fbID != "" && fbID != "0" {
				if fbTrack, err := s.dz.getTrack(fbID); err == nil {
					slog.Warn("[Deemix] track unavailable, trying its fallback release", "trackId", track.SNG_ID.String(), "fallbackId", fbID)
					// A fallback release is the last attempt. Its own fallback can cycle back here.
					fbTrack.FALLBACK = nil
					return s.downloadOneTrack(*fbTrack, bitrate, settings, albumTrack, loadAlbum, onProgress)
				}
			}
		}
		if lastErr == nil {
			lastErr = errNoTrackURL
		}
		return "", lastErr
	}

	album := loadAlbum(track)
	meta := s.buildTrackMeta(track, settings, album)
	dest := trackFilePath(settings, meta, extForFormat(usedFormat), albumTrack)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("creating download directory: %w", err)
	}
	installed, err := installTrack(dest, settings.OverwriteFile, func(tmp *os.File) error {
		if err := downloadAndDecrypt(downloadURL, track.SNG_ID.String(), tmp, onProgress); err != nil {
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := tagFile(tmp.Name(), usedFormat, meta, settings.Tags); err != nil {
			slog.Warn("[Deemix] downloaded but failed to tag", "path", dest, "error", err)
		}
		return nil
	})
	if err != nil || !installed {
		return dest, err
	}

	destDir := filepath.Dir(dest)
	if len(album.separateCover) > 0 {
		name := settings.CoverImageTemplate
		if name == "" {
			name = "cover"
		}
		if err := saveImageFile(destDir, name, album.separateCover, settings.LocalArtworkFormat); err != nil {
			slog.Warn("[Deemix] failed to save separate cover image file", "error", err)
		}
	}
	if settings.SaveArtworkArtist {
		if artistImg := fetchArtistImage(track.artistPictureHash(), settings.LocalArtworkSize); len(artistImg) > 0 {
			name := settings.ArtistImageTemplate
			if name == "" {
				name = "folder"
			}
			if err := saveImageFile(destDir, name, artistImg, settings.LocalArtworkFormat); err != nil {
				slog.Warn("[Deemix] failed to save artist image file", "error", err)
			}
		}
	}

	if settings.ExecuteCommand != "" {
		runPostDownloadCommand(settings.ExecuteCommand, dest)
	}

	return dest, nil
}

func installTrack(dest, mode string, write func(*os.File) error) (bool, error) {
	overwrite := mode == OverwriteYes
	if !overwrite {
		if existing, err := os.Stat(dest); err == nil {
			if !existing.Mode().IsRegular() {
				return false, fmt.Errorf("download destination is not a regular file: %s", dest)
			}
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	tmpPath := filepath.Join(filepath.Dir(dest), ".playlist-lab-"+uuid.NewString()+".part")
	tmp, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := write(tmp); err != nil {
		return false, err
	}
	if overwrite {
		err = os.Rename(tmp.Name(), dest)
	} else {
		err = os.Link(tmp.Name(), dest)
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
	}
	return err == nil, err
}

// runPostDownloadCommand runs Settings.ExecuteCommand (admin-only, same
// trust level as the admin panel already having systemd-restart power over
// deemix-server's old config) with the finished file's path as its one
// argument. Limit how long it can hold the download worker.
func runPostDownloadCommand(command, path string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, path)
	if err := cmd.Run(); err != nil {
		slog.Warn("[Deemix] post-download command failed", "command", command, "path", path, "error", err)
	}
}

// runTrackDownload is QueueDownload's background half for a single track:
// download+tag, reporting progress into the in-memory queue trackDownload
// (deemix.go) polls.
func (s *Service) runTrackDownload(uuid string, track gwTrack, bitrate string) {
	title, artist := track.SNG_TITLE, track.ART_NAME
	report := func(status string, progress int, errMsg string) {
		item := QueueItem{Status: status, Progress: progress, Title: title, Artist: artist, Size: 1}
		if status == "completed" {
			item.Downloaded = 1
		}
		if errMsg != "" {
			item.Errors = []queueError{{Message: errMsg}}
		}
		s.setQueueItem(uuid, item)
	}

	report("downloading", 0, "")
	settings := s.Settings()
	_, err := s.downloadOneTrack(track, bitrate, settings, false, s.albumAssetsFor(settings), func(read, total int64) {
		pct := 0
		if total > 0 {
			pct = int(read * 100 / total)
		}
		report("downloading", pct, "")
	})
	if err != nil {
		slog.Error("[Deemix] download failed", "uuid", uuid, "title", title, "error", err)
		report("failed", 0, err.Error())
		return
	}
	report("completed", 100, "")
}

// runAlbumDownload is QueueDownload's background half for a whole album:
// every track downloads with up to Settings.QueueConcurrency running at
// once, and the queue item's Downloaded/Size track how many are done for
// trackDownload's "%d/%d tracks" progress detail.
func (s *Service) runAlbumDownload(uuid string, tracks []gwTrack, bitrate string) {
	title, artist := tracks[0].ALB_TITLE, tracks[0].ART_NAME
	total := len(tracks)

	var mu sync.Mutex
	var downloaded, failed int
	report := func() {
		mu.Lock()
		defer mu.Unlock()
		d, f := downloaded, failed
		status, progress := "downloading", 0
		if total > 0 {
			progress = d * 100 / total
		}
		if d+f >= total {
			progress = 100
			switch {
			case f == total:
				status = "failed"
			case f > 0:
				status = "withErrors"
			default:
				status = "completed"
			}
		}
		s.setQueueItem(uuid, QueueItem{Status: status, Progress: progress, Title: title, Artist: artist, Size: total, Downloaded: d})
	}
	report()

	settings := s.Settings()
	loadAlbum := s.albumAssetsFor(settings)
	concurrency := settings.QueueConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, track := range tracks {
		wg.Add(1)
		sem <- struct{}{}
		go func(track gwTrack) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := s.downloadOneTrack(track, bitrate, settings, true, loadAlbum, nil); err != nil {
				if settings.LogErrors {
					slog.Warn("[Deemix] album track failed", "uuid", uuid, "track", track.SNG_TITLE, "error", err)
				}
				mu.Lock()
				failed++
				mu.Unlock()
			} else {
				mu.Lock()
				downloaded++
				mu.Unlock()
			}
			report()
		}(track)
	}
	wg.Wait()
}
