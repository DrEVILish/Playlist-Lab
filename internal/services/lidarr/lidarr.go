// Package lidarr ports services/lidarr.ts. Lidarr manages artist/album
// *monitoring* and triggers indexer/download-client searches - it has no
// "download this one track" endpoint like deemix-server does. So the flow
// here is: find (or add-and-monitor) the artist, find (or skip) the
// matching album, then trigger a search command and poll it - Lidarr's own
// indexers/download client (configured by the user in Lidarr itself, not by
// this app) do the actual downloading.
package lidarr

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/matching"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

type Config struct {
	URL    string
	APIKey string
}

type Service struct {
	cfg           Config
	cfgMu         sync.Mutex
	db            *sql.DB
	notifications *notifications.Store
	http          *http.Client
}

func New(cfg Config, sqlDB *sql.DB, notifStore *notifications.Store) *Service {
	return &Service{cfg: cfg, db: sqlDB, notifications: notifStore, http: &http.Client{Timeout: 15 * time.Second}}
}

// SetConfig updates the Lidarr URL/API key from the admin settings page.
func (s *Service) SetConfig(url, apiKey string) {
	s.cfgMu.Lock()
	s.cfg = Config{URL: strings.TrimSuffix(strings.TrimSpace(url), "/"), APIKey: strings.TrimSpace(apiKey)}
	s.cfgMu.Unlock()
}

// GetConfig returns the currently configured URL/API key, for prefilling the
// admin settings form.
func (s *Service) GetConfig() Config {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.cfg
}

func (s *Service) configured() error {
	if s.cfg.URL == "" || s.cfg.APIKey == "" {
		return fmt.Errorf("Lidarr is not configured - set LIDARR_URL and LIDARR_API_KEY")
	}
	return nil
}

func (s *Service) do(method, path string, query url.Values, body any, out any) error {
	if err := s.configured(); err != nil {
		return err
	}
	full := strings.TrimSuffix(s.cfg.URL, "/") + "/api/v1" + path
	if query != nil {
		full += "?" + query.Encode()
	}
	var reader *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(b))
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequest(method, full, reader)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", s.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Lidarr request failed: status %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type ArtistLookup struct {
	ArtistName      string `json:"artistName"`
	ForeignArtistID string `json:"foreignArtistId"`
	// ID is only present/non-zero if this artist is already added to Lidarr.
	ID int `json:"id"`
}

// FindArtist searches MusicBrainz (via Lidarr's own lookup proxy) for an
// artist by name - the same source Lidarr's "Add New Artist" UI search uses.
func (s *Service) FindArtist(name string) (*ArtistLookup, error) {
	var results []ArtistLookup
	if err := s.do(http.MethodGet, "/artist/lookup", url.Values{"term": {name}}, nil, &results); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return &results[0], nil
}

func (s *Service) firstOrErr(path, label string, out any) error {
	// ponytail: uses whichever root folder / quality profile / metadata
	// profile Lidarr lists first - this app has no per-artist configuration
	// UI for them. Fine for a single-root-folder/single-profile Lidarr
	// setup (the common case); add a settings picker if that stops being true.
	return s.do(http.MethodGet, path, nil, nil, out)
}

// AddAndMonitorArtist adds and monitors an artist found via FindArtist, or
// returns the existing Lidarr artist id if it's already there.
func (s *Service) AddAndMonitorArtist(lookup ArtistLookup) (int, error) {
	if lookup.ID != 0 {
		return lookup.ID, nil
	}

	var rootFolders []struct {
		Path string `json:"path"`
	}
	if err := s.firstOrErr("/rootfolder", "root folder", &rootFolders); err != nil {
		return 0, err
	}
	if len(rootFolders) == 0 {
		return 0, fmt.Errorf("Lidarr has no root folder configured")
	}
	var qualityProfiles []struct {
		ID int `json:"id"`
	}
	if err := s.firstOrErr("/qualityprofile", "quality profile", &qualityProfiles); err != nil {
		return 0, err
	}
	if len(qualityProfiles) == 0 {
		return 0, fmt.Errorf("Lidarr has no quality profile configured")
	}
	var metadataProfiles []struct {
		ID int `json:"id"`
	}
	if err := s.firstOrErr("/metadataprofile", "metadata profile", &metadataProfiles); err != nil {
		return 0, err
	}
	if len(metadataProfiles) == 0 {
		return 0, fmt.Errorf("Lidarr has no metadata profile configured")
	}

	payload := map[string]any{
		"artistName":        lookup.ArtistName,
		"foreignArtistId":   lookup.ForeignArtistID,
		"qualityProfileId":  qualityProfiles[0].ID,
		"metadataProfileId": metadataProfiles[0].ID,
		"rootFolderPath":    rootFolders[0].Path,
		"monitored":         true,
		"addOptions":        map[string]any{"monitor": "all", "searchForMissingAlbums": false},
	}
	var created struct {
		ID int `json:"id"`
	}
	if err := s.do(http.MethodPost, "/artist", nil, payload, &created); err != nil {
		return 0, err
	}
	return created.ID, nil
}

type Album struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

// FindAlbumForArtist finds an already-monitored album by title (loose
// match), so a search can target just that album instead of the artist's
// whole discography.
func (s *Service) FindAlbumForArtist(artistID int, albumTitle string) (*Album, error) {
	if albumTitle == "" {
		return nil, nil
	}
	var albums []Album
	q := url.Values{"artistId": {fmt.Sprintf("%d", artistID)}}
	if err := s.do(http.MethodGet, "/album", q, nil, &albums); err != nil {
		return nil, err
	}
	normalize := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	target := normalize(albumTitle)
	for _, a := range albums {
		if normalize(a.Title) == target {
			return &a, nil
		}
	}
	for _, a := range albums {
		if strings.Contains(normalize(a.Title), target) {
			return &a, nil
		}
	}
	return nil, nil
}

// TriggerSearch triggers an AlbumSearch (if an album was resolved) or a
// broader ArtistSearch across Lidarr's configured indexers, returning the
// command id to poll.
func (s *Service) TriggerSearch(artistID int, albumID int) (int, error) {
	var payload map[string]any
	if albumID != 0 {
		payload = map[string]any{"name": "AlbumSearch", "albumIds": []int{albumID}}
	} else {
		payload = map[string]any{"name": "ArtistSearch", "artistId": artistID}
	}
	var created struct {
		ID int `json:"id"`
	}
	if err := s.do(http.MethodPost, "/command", nil, payload, &created); err != nil {
		return 0, err
	}
	return created.ID, nil
}

type command struct {
	Status string `json:"status"` // queued|started|completed|failed
	Result string `json:"result"`
}

func (s *Service) getCommand(commandID int) (*command, error) {
	var c command
	if err := s.do(http.MethodGet, fmt.Sprintf("/command/%d", commandID), nil, nil, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

const (
	pollInterval          = 10 * time.Second
	pollMax               = 15 * time.Minute
	reconcilePollInterval = 30 * time.Second
	reconcileMaxAttempts  = 10 // ponytail: same fixed-attempt heuristic as deemix - neither Lidarr's grab/import timing nor Plex's scan interval are known here.
)

// ReconcileContext is what's needed to resolve a Lidarr-triggered download
// back to the missing_tracks row it was searched for, once the file has
// arrived and Plex has scanned it - same idea as deemix's ReconcileContext.
type ReconcileContext struct {
	MissingTrackID int64
	ServerURL      string
	PlexToken      string
	LibraryID      string
	ServerClientID string
}

// TrackSearch polls a Lidarr search command until it finishes (Lidarr only
// reports "search command done", not "file downloaded" - the actual
// grab/import happens asynchronously via Lidarr's download client), then
// hands off to the same bounded "wait for Plex to find the file" reconcile
// loop the Deemix path uses. Meant to be run in its own goroutine.
func (s *Service) TrackSearch(userID int64, notificationID string, commandID int, reconcile ReconcileContext) {
	start := time.Now()
	for time.Since(start) < pollMax {
		time.Sleep(pollInterval)
		cmd, err := s.getCommand(commandID)
		if err != nil {
			slog.Warn("[Lidarr] Failed to poll command", "commandId", commandID, "error", err)
			continue
		}
		if cmd.Status == "completed" {
			detail := "Search complete - waiting for Lidarr to grab and import it"
			s.notifications.Update(userID, notificationID, notifications.Patch{Detail: &detail})
			s.reconcileDownload(userID, notificationID, reconcile)
			return
		}
		if cmd.Status == "failed" {
			status := notifications.StatusError
			detail := cmd.Result
			if detail == "" {
				detail = "Lidarr search failed"
			}
			s.notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
			return
		}
		detail := fmt.Sprintf("Searching (%s)...", cmd.Status)
		s.notifications.Update(userID, notificationID, notifications.Patch{Detail: &detail})
	}
	slog.Error("[Lidarr] Timed out waiting for search command to finish", "commandId", commandID, "userId", userID)
	status, detail := notifications.StatusError, "Timed out waiting for Lidarr"
	s.notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
}

func (s *Service) reconcileDownload(userID int64, notificationID string, reconcile ReconcileContext) {
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
			return // Resolved another way while this was searching.
		}

		status, progress := notifications.StatusSuccess, 100
		detail := fmt.Sprintf("Waiting for Lidarr to grab and Plex to find it (%d/%d)", attempt, reconcileMaxAttempts)
		s.notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Progress: &progress, Detail: &detail})

		time.Sleep(reconcilePollInterval)

		settingsJSON, _ := db.GetMatchingSettingsJSON(s.db, userID)
		settings := matching.SettingsFromJSON(settingsJSON)
		matched, err := matching.MatchPlaylist(
			[]matching.Track{{Title: track.Title, Artist: track.Artist, Album: track.Album.String}},
			client, reconcile.LibraryID, settings, nil, nil, nil,
		)
		if err != nil {
			slog.Warn("[Lidarr] Reconciliation attempt failed, retrying", "error", err, "missingTrackId", reconcile.MissingTrackID, "attempt", attempt)
			continue
		}
		if len(matched) > 0 && matched[0].Matched && matched[0].PlexRatingKey != "" {
			target := matching.PlaylistTarget{ServerClientID: reconcile.ServerClientID, LibraryID: reconcile.LibraryID}
			if matching.InsertMatchedTrackIntoPlaylist(s.db, client, target, *track, matched[0].PlexRatingKey) {
				status, detail, progress := notifications.StatusSuccess, "Downloaded and matched via Lidarr", 100
				s.notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Progress: &progress, Detail: &detail})
				return
			}
		}
	}

	status, detail, progress := notifications.StatusSuccess, "Lidarr search sent - could not auto-match yet, use Retry", 100
	s.notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Progress: &progress, Detail: &detail})
}
