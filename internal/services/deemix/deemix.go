// Package deemix searches, downloads, decrypts, and tags missing tracks
// directly from Deezer - a Go port of deemix-server's own core (the
// deemix/deezer-js npm packages), rather than talking to a separate Node
// process over HTTP. See dzclient.go for the Deezer API client,
// decrypt.go for the Blowfish stream cipher, tag.go for ID3/FLAC tagging,
// and download.go for the queue/download orchestration; this file keeps
// the original public Service API (search, queue, poll, reconcile into a
// Plex playlist) that the rest of the app already depends on.
package deemix

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/matching"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

// Config is the subset of config.Config this package needs - passed in
// directly rather than importing the config package, so this stays testable
// without a real environment.
type Config struct {
	ARL string // Deezer account ARL cookie this app logs in with
}

type SearchResult struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Link   string `json:"link"`
	Artist struct {
		Name string `json:"name"`
	} `json:"artist"`
	Album struct {
		Title string `json:"title"`
		ID    int    `json:"id"`
	} `json:"album"`
}

type queueError struct {
	Message string `json:"message"`
}

// QueueItem is one in-progress or finished download's state, keyed by uuid
// in Service.queue. Status is one of inQueue|downloading|completed|withErrors|failed.
type QueueItem struct {
	Status     string       `json:"status"`
	Progress   int          `json:"progress"`
	Title      string       `json:"title"`
	Artist     string       `json:"artist"`
	Size       int          `json:"size"`
	Downloaded int          `json:"downloaded"`
	Errors     []queueError `json:"errors"`
}

type QueuedTrack struct {
	UUID   string
	Title  string
	Artist string
	// AlreadyQueued is true when this wasn't a fresh queue add - deemix-server
	// already had this exact track+bitrate queued.
	AlreadyQueued bool
}

// ReconcileContext is what's needed to resolve a completed download back to
// the missing_tracks row it was downloaded for.
type ReconcileContext struct {
	MissingTrackID int64
	ServerURL      string
	PlexToken      string
	LibraryID      string
	ServerClientID string
}

// DownloadRequest starts tracking one queued download.
type DownloadRequest struct {
	UserID        int64
	Title         string
	Detail        string
	UUID          string
	AlreadyQueued bool
	// Reconcile is nil for downloads with no single missing_tracks row to
	// resolve back to (there is no such caller in this port yet, but the
	// field mirrors deemix.ts's optional reconcile for parity).
	Reconcile *ReconcileContext
}

const (
	pollInterval           = 3 * time.Second
	pollMax                = 30 * time.Minute
	reconcilePollInterval  = 30 * time.Second
	reconcileMaxAttempts   = 10
	reconcileMaxConcurrent = 3 // ponytail: fixed global cap, make it per-server if one user's big batch starts starving another's.
	resumeMaxAge           = 24 * time.Hour
	scanDedupeWindow       = 60 * time.Second
)

// Service holds the shared Deezer client/queue state and the DB +
// notification store every download needs to report and resume progress.
// One instance is shared app-wide (see cmd/server/main.go).
type Service struct {
	cfg           Config
	db            *sql.DB
	notifications *notifications.Store
	dz            *dzClient

	settingsMu sync.RWMutex
	settings   Settings

	queueMu sync.Mutex
	queue   map[string]QueueItem

	reconcileMu      sync.Mutex
	reconcileActive  int
	reconcileWaiting []chan struct{}

	scanMu     sync.Mutex
	lastScanAt map[string]time.Time

	lastArlMu    sync.Mutex
	lastArlCheck *ArlCheckResult
}

type ArlCheckResult struct {
	OK    bool
	Error string
	At    time.Time
}

func New(cfg Config, sqlDB *sql.DB, notifStore *notifications.Store) *Service {
	settings := DefaultSettings()
	if sqlDB != nil {
		settings = LoadSettings(sqlDB)
	}
	return &Service{
		cfg: cfg, db: sqlDB, notifications: notifStore,
		dz:         newDZClient(cfg.ARL),
		settings:   settings,
		queue:      map[string]QueueItem{},
		lastScanAt: map[string]time.Time{},
	}
}

// ResetSession forces the next QueueDownload call to log in again.
func (s *Service) ResetSession() {
	s.dz.setARL(s.dz.currentARL())
}

// SetARL updates the Deezer ARL this app logs in with (admin settings page)
// and drops the cached session so the next download uses it.
func (s *Service) SetARL(arl string) {
	s.cfg.ARL = arl
	s.dz.setARL(arl)
}

// ARL returns the currently configured Deezer ARL, for prefilling the admin
// settings form.
func (s *Service) ARL() string {
	return s.dz.currentARL()
}

// Settings returns a copy of the currently active download settings - used
// both to prefill the admin settings form and internally wherever a
// download needs a consistent snapshot to work from.
func (s *Service) Settings() Settings {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.settings
}

// ReloadSettings re-reads the admin-saved Deezer download settings (naming
// templates, tag toggles, bitrate, ...) from the DB - called after the admin
// settings form saves so a running process picks up the change without a
// restart, mirroring how SetARL applies immediately.
func (s *Service) ReloadSettings() {
	if s.db == nil {
		return
	}
	settings := LoadSettings(s.db)
	s.settingsMu.Lock()
	s.settings = settings
	s.settingsMu.Unlock()
}

func (s *Service) login() error {
	return s.dz.login()
}

// SearchTrack searches Deezer's public track-search API for "term" - the
// raw top results, caller picks one. No login required (deezer-js's api.js
// search_track is unauthenticated), matching this method's original
// deemix-server-proxied behavior.
func (s *Service) SearchTrack(term string) ([]SearchResult, error) {
	if s.Settings().LogSearched {
		slog.Info("[Deemix] searching", "term", term)
	}
	req, err := http.NewRequest(http.MethodGet, "https://api.deezer.com/search/track", nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("q", term)
	q.Set("index", "0")
	q.Set("limit", "5")
	req.URL.RawQuery = q.Encode()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data struct {
		Data []SearchResult `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Data, nil
}

// ScoredMatch is one FindBestMatches result: the raw hit plus the score it
// got against the searched-for track.
type ScoredMatch struct {
	Match SearchResult
	Score float64
}

// FindBestMatches picks the best of a search's top hits, scored the same
// way a Plex candidate is (via matching.ScorePlexCandidate on the fields
// title/artist/album), or returns nothing when none of them clears the
// user's minMatchScore gate. A missing track is often missing because it
// isn't in Deezer's catalogue either, and downloading the closest unrelated
// hit would pollute the library permanently - taking the raw top hit
// unconditionally is exactly wrong here.
func (s *Service) FindBestMatches(title, artist string, settings matching.Settings, limit int) ([]ScoredMatch, error) {
	results, err := s.SearchTrack(strings.TrimSpace(artist + " " + title))
	if err != nil {
		return nil, err
	}
	minScore := settings.MinMatchScore
	if minScore <= 1 {
		minScore *= 100
	}

	var scored []ScoredMatch
	for _, m := range results {
		sourceArtist := artist
		if sourceArtist == "" {
			sourceArtist = m.Artist.Name
		}
		source := matching.Track{Title: title, Artist: sourceArtist, Album: m.Album.Title}
		candidate := plex.Track{Title: m.Title, GrandparentTitle: m.Artist.Name, ParentTitle: m.Album.Title}

		variants := matching.BuildAllVariants(source)
		effective, gate := matching.PickEffectiveVariant(variants, candidate, settings)
		if !gate.Passes {
			continue
		}
		score := matching.ScorePlexCandidate(effective.Title, effective.Artist, candidate, settings).Score
		if score >= minScore {
			scored = append(scored, ScoredMatch{Match: m, Score: score})
		}
	}

	// Highest score first.
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0 && scored[j].Score > scored[j-1].Score; j-- {
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}
	if limit > 0 && len(scored) > limit {
		scored = scored[:limit]
	}
	return scored, nil
}

// getAlbumTrackCount reads Deezer's own public catalogue API (no auth, same
// one deemix reads metadata from) to check whether a match's release is a
// real multi-track album/EP. Returns (0, false) on any failure so the
// caller falls back to downloading just the matched track.
func (s *Service) getAlbumTrackCount(albumID int) (int, bool) {
	resp, err := http.Get(fmt.Sprintf("https://api.deezer.com/album/%d", albumID))
	if err != nil {
		slog.Warn("[Deemix] Failed to fetch album track count, downloading matched track only", "albumId", albumID, "error", err)
		return 0, false
	}
	defer resp.Body.Close()
	var data struct {
		NbTracks int `json:"nb_tracks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, false
	}
	return data.NbTracks, true
}

// ResolveDownloadURL picks what to queue for a search match: the track
// itself, unless it's one of several tracks on a real album/EP, in which
// case the whole album is queued so the rest downloads along with it.
func (s *Service) ResolveDownloadURL(match SearchResult) string {
	count, ok := 0, false
	if match.Album.ID != 0 {
		count, ok = s.getAlbumTrackCount(match.Album.ID)
	}
	return resolveDownloadURL(match, count, ok)
}

// resolveDownloadURL is ResolveDownloadURL's decision logic, split out so it
// can be tested without a real call to Deezer's public API.
func resolveDownloadURL(match SearchResult, trackCount int, trackCountKnown bool) string {
	if match.Album.ID == 0 {
		return match.Link
	}
	if trackCountKnown && trackCount > 1 {
		return fmt.Sprintf("https://www.deezer.com/album/%d", match.Album.ID)
	}
	return match.Link
}

func parseTrackOrAlbumURL(u string) (typ, id string, ok bool) {
	for _, t := range []string{"track", "album"} {
		marker := "/" + t + "/"
		if idx := strings.Index(u, marker); idx != -1 {
			rest := u[idx+len(marker):]
			end := strings.IndexAny(rest, "/?")
			if end == -1 {
				end = len(rest)
			}
			id = rest[:end]
			if id != "" {
				if _, err := strconv.Atoi(id); err == nil {
					return t, id, true
				}
			}
		}
	}
	return "", "", false
}

// setQueueItem writes uuid's current state into the in-memory queue - the
// only writer besides QueueDownload's initial insert is download.go's
// per-track progress reporting.
func (s *Service) setQueueItem(uuid string, item QueueItem) {
	s.queueMu.Lock()
	s.queue[uuid] = item
	s.queueMu.Unlock()
}

// GetQueueItem reads one item's live status/progress out of the in-memory
// queue - nil, nil if it was never queued or has already been removed
// (StartDownload's poller treats that as "vanished").
func (s *Service) GetQueueItem(uuid string) (*QueueItem, error) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if item, ok := s.queue[uuid]; ok {
		return &item, nil
	}
	return nil, nil
}

// QueueDownload resolves trackURL (a deezer.com track or album link) against
// Deezer, then starts downloading it in the background - StartDownload's
// poller (see trackDownload) picks the resulting uuid's progress up from
// GetQueueItem exactly as it did when a separate deemix-server process ran
// the download, so no caller elsewhere in this package needed to change.
//
// fallbackURL is queued instead if trackURL produces nothing - a whole
// album can fail to resolve while the individual track is fine, and since
// queueing the album at all is our own optimisation rather than what the
// user asked for, falling back gets them the track instead of an error.
func (s *Service) QueueDownload(trackURL, fallbackURL string) (QueuedTrack, error) {
	typ, id, ok := parseTrackOrAlbumURL(trackURL)
	if !ok {
		return QueuedTrack{}, fmt.Errorf("not a deezer track or album URL: %s", trackURL)
	}
	if err := s.login(); err != nil {
		return QueuedTrack{}, err
	}

	bitrate := s.Settings().MaxBitrate
	uuid := fmt.Sprintf("%s_%s_%s", typ, id, bitrate)

	if existing, _ := s.GetQueueItem(uuid); existing != nil {
		slog.Info("[Deemix] Already in queue, reporting its existing state", "uuid", uuid, "status", existing.Status)
		return QueuedTrack{UUID: uuid, Title: existing.Title, Artist: existing.Artist, AlreadyQueued: true}, nil
	}

	switch typ {
	case "track":
		track, err := s.dz.getTrack(id)
		if err != nil {
			if fallbackURL != "" && fallbackURL != trackURL {
				return s.QueueDownload(fallbackURL, "")
			}
			return QueuedTrack{}, fmt.Errorf("deemix could not resolve %s: %w", trackURL, err)
		}
		s.setQueueItem(uuid, QueueItem{Status: "inQueue", Title: track.SNG_TITLE, Artist: track.ART_NAME, Size: 1})
		go s.runTrackDownload(uuid, *track, bitrate)
		return QueuedTrack{UUID: uuid, Title: track.SNG_TITLE, Artist: track.ART_NAME}, nil

	case "album":
		tracks, err := s.dz.getAlbumTracks(id)
		if err != nil || len(tracks) == 0 {
			if fallbackURL != "" && fallbackURL != trackURL {
				slog.Warn("[Deemix] could not resolve album, retrying with the single track", "albumUrl", trackURL, "fallbackUrl", fallbackURL, "error", err)
				return s.QueueDownload(fallbackURL, "")
			}
			if err == nil {
				err = fmt.Errorf("album has no tracks")
			}
			return QueuedTrack{}, fmt.Errorf("deemix could not resolve %s: %w", trackURL, err)
		}
		title, artist := tracks[0].ALB_TITLE, tracks[0].ART_NAME
		s.setQueueItem(uuid, QueueItem{Status: "inQueue", Title: title, Artist: artist, Size: len(tracks)})
		go s.runAlbumDownload(uuid, tracks, bitrate)
		return QueuedTrack{UUID: uuid, Title: title, Artist: artist}, nil
	}

	return QueuedTrack{}, fmt.Errorf("unsupported deezer URL: %s", trackURL)
}

// StartDownload is the one entry point for tracking a queued download:
// opens the notification the header shows, records the download so it
// survives a restart (see ResumeDownloads), and starts the progress poller.
func (s *Service) StartDownload(req DownloadRequest) {
	notification := s.notifications.Add(req.UserID, notifications.TypeDeemix, req.Title, req.Detail, notifications.StatusInProgress, nil)
	var missingTrackID *int64
	if req.Reconcile != nil {
		missingTrackID = &req.Reconcile.MissingTrackID
	}
	downloadID, err := db.AddDeemixDownload(s.db, req.UserID, req.UUID, req.Title, req.Detail, missingTrackID)
	if err != nil {
		slog.Error("[Deemix] Failed to persist in-flight download", "error", err, "uuid", req.UUID)
	}
	go s.trackDownload(trackOptions{
		userID: req.UserID, notificationID: notification.ID, uuid: req.UUID,
		checkImmediately: req.AlreadyQueued, reconcile: req.Reconcile, downloadID: downloadID,
	})
}

// ResumeDownloads restarts the progress poller for every download still
// recorded as in flight - called once at startup so a restart doesn't
// abandon files deemix is (or was) still happily downloading.
func (s *Service) ResumeDownloads() {
	rows, err := db.GetActiveDeemixDownloads(s.db, resumeMaxAge)
	if err != nil {
		slog.Error("[Deemix] Failed to read in-flight downloads to resume", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	for _, row := range rows {
		detail := row.Detail.String
		notification := s.notifications.Add(row.UserID, notifications.TypeDeemix, row.Title, detail, notifications.StatusInProgress, nil)
		var reconcile *ReconcileContext
		if row.MissingTrackID.Valid {
			reconcile = s.rebuildReconcileContext(row.UserID, row.MissingTrackID.Int64)
		}
		go s.trackDownload(trackOptions{
			userID: row.UserID, notificationID: notification.ID, uuid: row.UUID,
			checkImmediately: true, reconcile: reconcile, downloadID: row.ID,
		})
	}
	slog.Info("[Deemix] Resumed in-flight downloads after restart", "count", len(rows))
}

func (s *Service) rebuildReconcileContext(userID, missingTrackID int64) *ReconcileContext {
	user, err := db.GetUserByID(s.db, userID)
	if err != nil {
		return nil
	}
	server, err := db.GetUserMusicServer(s.db, userID)
	if err != nil || server == nil {
		return nil
	}
	return &ReconcileContext{
		MissingTrackID: missingTrackID,
		ServerURL:      server.ServerURL,
		PlexToken:      plex.ResolveToken(user.PlexToken, server.AccessToken.String),
		LibraryID:      server.LibraryID.String,
		ServerClientID: server.ServerClientID,
	}
}

type trackOptions struct {
	userID           int64
	notificationID   string
	uuid             string
	checkImmediately bool
	reconcile        *ReconcileContext
	downloadID       int64
}

// trackDownload polls deemix-server's queue for one download's progress and
// mirrors it into its notification until it finishes, fails, or has been
// polling too long to be worth continuing.
func (s *Service) trackDownload(opt trackOptions) {
	defer func() {
		if opt.downloadID != 0 {
			_ = db.DeleteDeemixDownload(s.db, opt.downloadID)
		}
	}()

	start := time.Now()
	first := true
	for time.Since(start) < pollMax {
		if !(first && opt.checkImmediately) {
			time.Sleep(pollInterval)
		}
		first = false

		item, err := s.GetQueueItem(opt.uuid)
		if err != nil {
			slog.Warn("[Deemix] Failed to poll queue item, retrying", "error", err, "uuid", opt.uuid)
			continue
		}
		if item == nil {
			slog.Warn("[Deemix] Queue item disappeared before finishing", "uuid", opt.uuid, "userId", opt.userID)
			if opt.reconcile != nil {
				s.reconcileDownloadedTrack(opt.userID, opt.notificationID, opt.reconcile, "vanished")
			} else {
				status, detail := notifications.StatusError, "Disappeared from the deemix queue before finishing"
				s.notifications.Update(opt.userID, opt.notificationID, notifications.Patch{Status: &status, Detail: &detail})
			}
			return
		}
		if item.Status == "completed" {
			if opt.reconcile != nil {
				s.reconcileDownloadedTrack(opt.userID, opt.notificationID, opt.reconcile, "completed")
			} else {
				status, progress := notifications.StatusSuccess, 100
				s.notifications.Update(opt.userID, opt.notificationID, notifications.Patch{Status: &status, Progress: &progress})
			}
			return
		}
		if item.Status == "failed" || item.Status == "withErrors" {
			errMsg := "Download failed"
			if len(item.Errors) > 0 && item.Errors[0].Message != "" {
				errMsg = item.Errors[0].Message
			}
			slog.Error("[Deemix] Download failed", "uuid", opt.uuid, "userId", opt.userID, "status", item.Status, "error", errMsg)
			status := notifications.StatusError
			s.notifications.Update(opt.userID, opt.notificationID, notifications.Patch{Status: &status, Detail: &errMsg})
			return
		}
		statusWord := "Downloading"
		if item.Status == "inQueue" {
			statusWord = "Queued"
		}
		detail := statusWord
		if item.Size > 1 {
			detail = fmt.Sprintf("%d/%d tracks - %s", item.Downloaded, item.Size, strings.ToLower(statusWord))
		}
		progress := item.Progress
		s.notifications.Update(opt.userID, opt.notificationID, notifications.Patch{Progress: &progress, Detail: &detail})
	}
	slog.Error("[Deemix] Timed out waiting for download to finish", "uuid", opt.uuid, "userId", opt.userID)
	status, detail := notifications.StatusError, "Timed out waiting for deemix"
	s.notifications.Update(opt.userID, opt.notificationID, notifications.Patch{Status: &status, Detail: &detail})
}

func (s *Service) acquireReconcileSlot() {
	s.reconcileMu.Lock()
	if s.reconcileActive < reconcileMaxConcurrent {
		s.reconcileActive++
		s.reconcileMu.Unlock()
		return
	}
	ch := make(chan struct{})
	s.reconcileWaiting = append(s.reconcileWaiting, ch)
	s.reconcileMu.Unlock()
	<-ch
	s.reconcileMu.Lock()
	s.reconcileActive++
	s.reconcileMu.Unlock()
}

func (s *Service) releaseReconcileSlot() {
	s.reconcileMu.Lock()
	s.reconcileActive--
	var next chan struct{}
	if len(s.reconcileWaiting) > 0 {
		next = s.reconcileWaiting[0]
		s.reconcileWaiting = s.reconcileWaiting[1:]
	}
	s.reconcileMu.Unlock()
	if next != nil {
		close(next)
	}
}

// scanPlexIfQueueDrained triggers a Plex library scan once deemix's whole
// queue is drained (not per completed download - mid-batch, still-
// downloading tracks aren't on disk yet, so scanning every time would just
// make Plex re-walk the whole library for no gain).
func (s *Service) scanPlexIfQueueDrained(reconcile *ReconcileContext) {
	if reconcile.LibraryID == "" {
		return
	}
	s.queueMu.Lock()
	queue := make(map[string]QueueItem, len(s.queue))
	for k, v := range s.queue {
		queue[k] = v
	}
	s.queueMu.Unlock()
	for _, item := range queue {
		if item.Status == "inQueue" || item.Status == "downloading" {
			return
		}
	}

	key := reconcile.ServerURL + ":" + reconcile.LibraryID
	s.scanMu.Lock()
	if time.Since(s.lastScanAt[key]) < scanDedupeWindow {
		s.scanMu.Unlock()
		return
	}
	s.lastScanAt[key] = time.Now()
	s.scanMu.Unlock()

	client := plex.NewClient(reconcile.ServerURL, reconcile.PlexToken, "", "Playlist Lab")
	if err := client.ScanLibrary(reconcile.LibraryID, ""); err != nil {
		slog.Warn("[Deemix] Could not trigger a Plex library scan", "error", err)
		return
	}
	slog.Info("[Deemix] deemix queue drained - triggered a Plex library scan", "libraryId", reconcile.LibraryID)
}

// reconcileDownloadedTrack asks Plex to scan, then polls for a match on the
// originally-missing track a few times, inserting it into its playlist as
// soon as it resolves.
func (s *Service) reconcileDownloadedTrack(userID int64, notificationID string, reconcile *ReconcileContext, outcome string) {
	label := "Downloaded"
	if outcome == "vanished" {
		label = "Left the deemix queue unfinished"
	}
	waitingDetail := label + " - waiting to check Plex"
	progress := 100
	s.notifications.Update(userID, notificationID, notifications.Patch{Progress: &progress, Detail: &waitingDetail})

	s.scanPlexIfQueueDrained(reconcile)
	s.acquireReconcileSlot()
	defer s.releaseReconcileSlot()
	s.runReconcileAttempts(userID, notificationID, reconcile, label, outcome)
}

func (s *Service) runReconcileAttempts(userID int64, notificationID string, reconcile *ReconcileContext, label, outcome string) {
	client := plex.NewClient(reconcile.ServerURL, reconcile.PlexToken, "", "Playlist Lab")

	for attempt := 1; attempt <= reconcileMaxAttempts; attempt++ {
		tracks, err := db.GetUserMissingTracks(s.db, userID)
		if err != nil {
			time.Sleep(reconcilePollInterval)
			continue
		}
		var track *db.MissingTrack
		for i := range tracks {
			if tracks[i].ID == reconcile.MissingTrackID {
				track = &tracks[i]
				break
			}
		}
		if track == nil {
			status, detail, progress := notifications.StatusSuccess, label+" - already matched", 100
			s.notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Progress: &progress, Detail: &detail})
			return
		}

		detail := fmt.Sprintf("%s - waiting for Plex to find it (%d/%d)", label, attempt, reconcileMaxAttempts)
		progress := 100
		s.notifications.Update(userID, notificationID, notifications.Patch{Progress: &progress, Detail: &detail})

		time.Sleep(reconcilePollInterval)

		settingsJSON, _ := db.GetMatchingSettingsJSON(s.db, userID)
		settings := matching.SettingsFromJSON(settingsJSON)
		matched, err := matching.MatchPlaylist(
			[]matching.Track{{Title: track.Title, Artist: track.Artist, Album: track.Album.String}},
			client, reconcile.LibraryID, settings, nil, nil, nil,
		)
		if err != nil {
			slog.Warn("[Deemix] Reconciliation attempt failed, retrying", "error", err, "missingTrackId", reconcile.MissingTrackID, "attempt", attempt)
			continue
		}
		if len(matched) > 0 && matched[0].Matched && matched[0].PlexRatingKey != "" {
			target := matching.PlaylistTarget{ServerClientID: reconcile.ServerClientID, LibraryID: reconcile.LibraryID}
			if matching.InsertMatchedTrackIntoPlaylist(s.db, client, target, *track, matched[0].PlexRatingKey) {
				status, detail, progress := notifications.StatusSuccess, label+" and matched", 100
				s.notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Progress: &progress, Detail: &detail})
				return
			}
		}
	}

	status := notifications.StatusSuccess
	if outcome != "completed" {
		status = notifications.StatusError
	}
	detail := label + " - could not auto-match yet, use Retry"
	progress := 100
	s.notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Progress: &progress, Detail: &detail})
}

// CheckArl verifies the configured ARL still works - Deezer ARLs expire
// every few months, and without a check the only signal used to be a user
// clicking Deemix and getting an error.
func (s *Service) CheckArl() ArlCheckResult {
	var result ArlCheckResult
	if err := s.login(); err != nil {
		result = ArlCheckResult{OK: false, Error: err.Error(), At: time.Now()}
	} else {
		result = ArlCheckResult{OK: true, At: time.Now()}
	}
	s.lastArlMu.Lock()
	s.lastArlCheck = &result
	s.lastArlMu.Unlock()
	return result
}

func (s *Service) LastArlCheck() *ArlCheckResult {
	s.lastArlMu.Lock()
	defer s.lastArlMu.Unlock()
	return s.lastArlCheck
}

// CheckArlAndNotifyAdmins checks the ARL and, when it fails, tells every
// admin - the ARL is server-wide config rather than any one user's.
func (s *Service) CheckArlAndNotifyAdmins() {
	result := s.CheckArl()
	if result.OK {
		return
	}
	slog.Error("[Deemix] ARL check failed", "error", result.Error)
	adminIDs, err := db.GetAdminUserIDs(s.db)
	if err != nil {
		slog.Error("[Deemix] Failed to list admins for ARL check notice", "error", err)
		return
	}
	for _, adminID := range adminIDs {
		s.notifications.Add(adminID, notifications.TypeDeemix, "Deemix login failed",
			result.Error+" - update DEEMIX_ARL.", notifications.StatusError, nil)
	}
}
