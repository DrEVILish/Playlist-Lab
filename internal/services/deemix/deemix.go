// Package deemix ports services/deemix.ts: talking to our own local
// deemix-server install (a REST wrapper around the deemix Deezer
// downloader) to search for and queue missing-track acquisitions, then
// tracking each download's progress through to a Plex library rescan and
// auto-match back into the playlist it was downloaded for.
//
// deemix-server's login is a browser-style session (cookie-based, kept in
// memory only on its side) - there is no per-request API key. So this
// package holds one shared login session for the whole app, established
// from the configured ARL and re-established whenever deemix-server
// rejects a request as logged out (or after a restart, since its session
// store is in-memory too).
package deemix

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
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
	URL string // deemix-server base URL, e.g. http://127.0.0.1:6595
	ARL string // Deezer account ARL cookie deemix-server logs in with
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

// QueueItem is deemix-server's queue entry for a uuid returned by
// QueueDownload. Status is one of inQueue|downloading|completed|withErrors|failed.
type QueueItem struct {
	Status     string `json:"status"`
	Progress   int    `json:"progress"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Size       int    `json:"size"`
	Downloaded int    `json:"downloaded"`
	Errors     []struct {
		Message string `json:"message"`
	} `json:"errors"`
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
	queueSnapshotTTL       = 2 * time.Second
	pollInterval           = 3 * time.Second
	pollMax                = 30 * time.Minute
	reconcilePollInterval  = 30 * time.Second
	reconcileMaxAttempts   = 10
	reconcileMaxConcurrent = 3 // ponytail: fixed global cap, make it per-server if one user's big batch starts starving another's.
	resumeMaxAge           = 24 * time.Hour
	scanDedupeWindow       = 60 * time.Second
)

type queueSnapshot struct {
	at    time.Time
	queue map[string]QueueItem
	err   error
	done  chan struct{}
}

// Service holds the shared deemix-server session/queue-snapshot state and
// the DB + notification store every download needs to report and resume
// progress. One instance is shared app-wide (see cmd/server/main.go).
type Service struct {
	cfg           Config
	db            *sql.DB
	notifications *notifications.Store
	http          *http.Client

	mu            sync.Mutex
	sessionCookie string
	cachedBitrate string

	queueMu  sync.Mutex
	snapshot *queueSnapshot

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
	return &Service{
		cfg: cfg, db: sqlDB, notifications: notifStore,
		http:       &http.Client{Timeout: 15 * time.Second},
		lastScanAt: map[string]time.Time{},
	}
}

func (s *Service) url(path string) string { return strings.TrimSuffix(s.cfg.URL, "/") + path }

// ResetSession forces the next QueueDownload call to log in again.
func (s *Service) ResetSession() {
	s.mu.Lock()
	s.sessionCookie = ""
	s.mu.Unlock()
}

// SetARL updates the Deezer ARL deemix-server logs in with (admin settings
// page) and drops the cached session so the next download uses it.
func (s *Service) SetARL(arl string) {
	s.mu.Lock()
	s.cfg.ARL = arl
	s.sessionCookie = ""
	s.mu.Unlock()
}

// ARL returns the currently configured Deezer ARL, for prefilling the admin
// settings form.
func (s *Service) ARL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.ARL
}

func (s *Service) login() error {
	if s.cfg.ARL == "" {
		return fmt.Errorf("Deemix ARL is not configured - set DEEMIX_ARL")
	}
	body, _ := json.Marshal(map[string]string{"arl": s.cfg.ARL})
	req, err := http.NewRequest(http.MethodPost, s.url("/api/loginArl"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		return fmt.Errorf("deemix login did not return a session cookie")
	}
	parts := make([]string, len(cookies))
	for i, c := range cookies {
		parts[i], _, _ = strings.Cut(c, ";")
	}
	cookie := strings.Join(parts, "; ")

	var data struct {
		Status int `json:"status"`
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &data)
	if data.Status == 0 {
		return fmt.Errorf("deemix rejected DEEMIX_ARL - it may have expired, get a fresh one from your Deezer account")
	}

	s.mu.Lock()
	s.sessionCookie = cookie
	s.mu.Unlock()
	return nil
}

// SearchTrack searches deemix-server's proxy of Deezer's own search
// relevance ranking for "term" - the raw top results, caller picks one.
func (s *Service) SearchTrack(term string) ([]SearchResult, error) {
	req, err := http.NewRequest(http.MethodGet, s.url("/api/search"), nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("term", term)
	q.Set("type", "track")
	q.Set("start", "0")
	q.Set("nb", "5")
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

func (s *Service) getConfiguredBitrate() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cachedBitrate != "" {
		return s.cachedBitrate
	}
	// getSettings isn't ported (no admin settings UI yet in Go - see
	// config.go's DeemixArl comment) - deemix-server's own default bitrate
	// (3 = MP3 320) is used instead of round-tripping to fetch a value
	// nothing here can change yet.
	s.cachedBitrate = "3"
	return s.cachedBitrate
}

type addToQueueResponse struct {
	Result *bool  `json:"result"`
	Errid  string `json:"errid"`
	Data   struct {
		Obj json.RawMessage `json:"obj"`
	} `json:"data"`
}

func (s *Service) postAddToQueue(trackURL, bitrate, cookie string) (*addToQueueResponse, error) {
	body, _ := json.Marshal(map[string]string{"url": trackURL, "bitrate": bitrate})
	req, err := http.NewRequest(http.MethodPost, s.url("/api/addToQueue"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data addToQueueResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

// QueueDownload queues a track (or album) URL with deemix-server.
//
// fallbackURL is queued instead if trackURL produces nothing - deemix can
// fail to build a download for a whole album while the individual track is
// fine, and since queueing the album at all is our own optimisation rather
// than what the user asked for, falling back gets them the track instead of
// an error.
func (s *Service) QueueDownload(trackURL, fallbackURL string) (QueuedTrack, error) {
	s.mu.Lock()
	cookie := s.sessionCookie
	s.mu.Unlock()
	if cookie == "" {
		if err := s.login(); err != nil {
			return QueuedTrack{}, err
		}
		s.mu.Lock()
		cookie = s.sessionCookie
		s.mu.Unlock()
	}

	bitrate := s.getConfiguredBitrate()
	data, err := s.postAddToQueue(trackURL, bitrate, cookie)
	if err != nil {
		return QueuedTrack{}, err
	}
	// deemix-server answers HTTP 200 even when it rejects the job.
	if data.Result != nil && !*data.Result && data.Errid == "NotLoggedIn" {
		if err := s.login(); err != nil {
			return QueuedTrack{}, err
		}
		s.mu.Lock()
		cookie = s.sessionCookie
		s.mu.Unlock()
		data, err = s.postAddToQueue(trackURL, bitrate, cookie)
		if err != nil {
			return QueuedTrack{}, err
		}
	}
	if data.Result != nil && !*data.Result {
		errid := data.Errid
		if errid == "" {
			errid = "unknown error"
		}
		return QueuedTrack{}, fmt.Errorf("deemix rejected the download (%s)", errid)
	}

	if queued, ok := parseQueuedObj(data.Data.Obj); ok {
		return queued, nil
	}

	// An empty obj (rather than a rejection) means deemix-server treated
	// this as "already in queue" - not a real failure. deemix's uuid scheme
	// is deterministic (`${type}_${id}_${bitrate}`), so it can be looked up
	// directly to report its actual current state.
	if typ, id, ok := parseTrackOrAlbumURL(trackURL); ok {
		uuid := fmt.Sprintf("%s_%s_%s", typ, id, bitrate)
		if existing, err := s.GetQueueItem(uuid); err == nil && existing != nil {
			slog.Info("[Deemix] Already in deemix queue, reporting its existing state", "uuid", uuid, "status", existing.Status)
			return QueuedTrack{UUID: uuid, Title: existing.Title, Artist: existing.Artist, AlreadyQueued: true}, nil
		}
	}

	if fallbackURL != "" && fallbackURL != trackURL {
		slog.Warn("[Deemix] deemix could not queue this release, retrying with the single track", "trackUrl", trackURL, "fallbackUrl", fallbackURL)
		return s.QueueDownload(fallbackURL, "")
	}

	slog.Error("[Deemix] addToQueue returned no queued item and no matching existing queue entry", "trackUrl", trackURL, "bitrate", bitrate)
	return QueuedTrack{}, fmt.Errorf("deemix could not queue %s - it is most likely unavailable on the configured Deezer account", trackURL)
}

func parseQueuedObj(raw json.RawMessage) (QueuedTrack, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return QueuedTrack{}, false
	}
	// obj is either a single object or an array with one object in it.
	var single struct {
		UUID   string `json:"uuid"`
		Title  string `json:"title"`
		Artist string `json:"artist"`
	}
	if err := json.Unmarshal(raw, &single); err == nil && single.UUID != "" {
		return QueuedTrack{UUID: single.UUID, Title: single.Title, Artist: single.Artist}, true
	}
	var arr []struct {
		UUID   string `json:"uuid"`
		Title  string `json:"title"`
		Artist string `json:"artist"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 && arr[0].UUID != "" {
		return QueuedTrack{UUID: arr[0].UUID, Title: arr[0].Title, Artist: arr[0].Artist}, true
	}
	return QueuedTrack{}, false
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

// getQueue fetches deemix-server's whole queue, sharing one in-flight
// request/response across every concurrent caller within queueSnapshotTTL -
// deemix-server has no per-item status endpoint, so N in-flight downloads
// each independently polling the whole queue is N*N JSON work per interval
// otherwise.
func (s *Service) getQueue() (map[string]QueueItem, error) {
	s.queueMu.Lock()
	if s.snapshot != nil && time.Since(s.snapshot.at) < queueSnapshotTTL {
		snap := s.snapshot
		s.queueMu.Unlock()
		<-snap.done
		return snap.queue, snap.err
	}
	snap := &queueSnapshot{at: time.Now(), done: make(chan struct{})}
	s.snapshot = snap
	s.queueMu.Unlock()

	// Deliberately sent without a session cookie - deemix-server's
	// /api/getQueue performs no login check at all.
	resp, err := http.Get(s.url("/api/getQueue"))
	if err == nil {
		defer resp.Body.Close()
		var data struct {
			Queue map[string]QueueItem `json:"queue"`
		}
		if decErr := json.NewDecoder(resp.Body).Decode(&data); decErr == nil {
			snap.queue = data.Queue
		} else {
			err = decErr
		}
	}
	snap.err = err
	close(snap.done)

	if err != nil {
		s.queueMu.Lock()
		if s.snapshot == snap {
			s.snapshot = nil
		}
		s.queueMu.Unlock()
	}
	return snap.queue, err
}

// GetQueueItem reads one item's live status/progress out of the shared
// queue snapshot.
func (s *Service) GetQueueItem(uuid string) (*QueueItem, error) {
	queue, err := s.getQueue()
	if err != nil {
		return nil, err
	}
	if item, ok := queue[uuid]; ok {
		return &item, nil
	}
	return nil, nil
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
	server, err := db.GetUserServer(s.db, userID)
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
	queue, err := s.getQueue()
	if err != nil {
		return
	}
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
