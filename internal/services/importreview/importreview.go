// Package importreview backs the plain-import preview/review/confirm flow
// (routes/import.ts's POST /preview, /match, /confirm), ported for the
// first time in this rewrite: until now, submitting an import ran
// importsvc.ImportPlaylist and created the Plex playlist immediately with
// no chance to see or fix the match results first - the human-in-the-loop
// review step ImportPage.tsx's handlePreview/handleConfirmUpdated give
// every import in the real app.
//
// Modeled directly on internal/services/crossimport's session/store, which
// solves the identical "hold matched-but-not-yet-committed results, keyed
// by an opaque id, so a follow-up request can edit one row and re-render"
// problem for cross-import's own review step. Kept as a separate,
// parallel implementation rather than generalizing crossimport's own
// types: those are shaped around adapters.MatchResult/TargetConfig
// (cross-service target matching), not matching.MatchedTrack (matching
// against this user's own Plex library), and forcing one shape to serve
// both would be a bigger, riskier change than duplicating ~100 lines of
// session bookkeeping.
package importreview

import (
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/drevilish/playlist-lab/internal/services/matching"
)

// Track is one row in the review list: the source track plus whatever Plex
// match currently applies to it, editable independently of the others via
// Session.Update/SetSkipped.
type Track struct {
	Title, Artist, Album string
	Matched              bool
	PlexRatingKey        string
	PlexTitle            string
	PlexArtist           string
	PlexAlbum            string
	Skipped              bool
}

// Progress is the review session's fetch/match status while
// importsvc.ImportPlaylist is still running in the background - a small
// duplicate of crossimport.Progress's shape (Phase/Current/Total), not a
// shared type, for the same reason Track above duplicates
// matching.MatchedTrack instead of reusing crossimport's own type.
type Progress struct {
	Phase   string // "scraping", "matching", "review", or "error"
	Current int
	Total   int
	Message string // set when Phase is "error"
}

// Session holds one in-progress import's editable review state between
// POST /import/preview and POST /import/confirm/:sessionId.
type Session struct {
	ID               string
	UserID           int64
	Source           string
	SourceIdentifier string
	CreatedAt        time.Time

	mu           sync.Mutex
	playlistName string
	coverURL     string
	tracks       []Track
	progress     Progress
}

// PlaylistName and CoverURL are read a lot (templates, confirmImport) - kept
// as plain accessor methods rather than exported fields now that they're
// mutable after a session is created bare (see Store.New/SetPlaylistName).
func (s *Session) PlaylistName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playlistName
}

func (s *Session) CoverURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.coverURL
}

func (s *Session) SetPlaylistName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.playlistName = name
}

func (s *Session) SetCoverURL(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coverURL = url
}

func (s *Session) SetProgress(p Progress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress = p
}

func (s *Session) GetProgress() Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progress
}

func (s *Session) SetTracks(tracks []Track) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracks = tracks
}

func (s *Session) Tracks() []Track {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Track, len(s.tracks))
	copy(out, s.tracks)
	return out
}

// Update mutates the track at index via fn, returning false if index is out
// of range. fn runs under the session's lock so a concurrent read (e.g. a
// second browser tab re-rendering the review page) can't observe a
// half-applied edit.
func (s *Session) Update(index int, fn func(*Track)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.tracks) {
		return false
	}
	fn(&s.tracks[index])
	return true
}

// Move reorders the track currently at fromIndex to sit right after the
// track currently at afterIndex (afterIndex -1 means "move to the front"),
// same "move relative to a stable-at-render-time position" idea as
// playlists.go's moveTrack (which uses a Plex item id instead of an index
// since its rows already exist in Plex - these don't yet, so a plain slice
// index is the local equivalent). Returns false if either index is out of
// range.
func (s *Session) Move(fromIndex, afterIndex int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.tracks)
	if fromIndex < 0 || fromIndex >= n || afterIndex < -1 || afterIndex >= n || fromIndex == afterIndex {
		return false
	}
	moved := s.tracks[fromIndex]
	rest := append(s.tracks[:fromIndex:fromIndex], s.tracks[fromIndex+1:]...)

	insertAfter := afterIndex
	if afterIndex > fromIndex {
		insertAfter--
	}
	insertPos := insertAfter + 1

	out := make([]Track, 0, n)
	out = append(out, rest[:insertPos]...)
	out = append(out, moved)
	out = append(out, rest[insertPos:]...)
	s.tracks = out
	return true
}

// Counts tallies the review summary bar's four numbers in one pass.
func (s *Session) Counts() (total, matched, unmatched, skipped int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	total = len(s.tracks)
	for _, t := range s.tracks {
		switch {
		case t.Skipped:
			skipped++
		case t.Matched:
			matched++
		default:
			unmatched++
		}
	}
	return
}

// FromMatchedTracks seeds a session's tracks from importsvc.ImportPlaylist's
// Result.Matched/Unmatched - both are matching.MatchedTrack, distinguished
// only by their own Matched field, so this just flattens both slices in
// their original (Matched-first) order.
func FromMatchedTracks(matched, unmatched []matching.MatchedTrack) []Track {
	out := make([]Track, 0, len(matched)+len(unmatched))
	for _, m := range append(append([]matching.MatchedTrack{}, matched...), unmatched...) {
		out = append(out, Track{
			Title: m.Title, Artist: m.Artist, Album: m.Album,
			Matched: m.Matched, PlexRatingKey: m.PlexRatingKey,
			PlexTitle: m.PlexTitle, PlexArtist: m.PlexArtist, PlexAlbum: m.PlexAlbum,
		})
	}
	return out
}

// Store is a process-wide, in-memory registry of open review sessions -
// same lost-on-restart tradeoff as crossimport.Store and the notification
// store: these are minutes-long interactive flows, not data anyone expects
// to survive a restart.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func NewStore() *Store {
	return &Store{sessions: map[string]*Session{}}
}

func (s *Store) New(source, sourceIdentifier, playlistName, coverURL string, userID int64) *Session {
	sess := s.NewBare(source, sourceIdentifier, userID)
	sess.playlistName, sess.coverURL = playlistName, coverURL
	return sess
}

// NewBare creates a session before the fetch/match results (playlist name,
// cover, tracks) are known - used by the live-progress preview path, which
// needs a session id to poll against while importsvc.ImportPlaylist is
// still running in a background goroutine. SetPlaylistName/SetCoverURL/
// SetTracks fill it in once that finishes.
func (s *Store) NewBare(source, sourceIdentifier string, userID int64) *Session {
	sess := &Session{
		ID: uuid.NewString(), UserID: userID, Source: source, SourceIdentifier: sourceIdentifier,
		CreatedAt: time.Now(),
	}
	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()
	return sess
}

// Get returns id's session only if it belongs to userID - a mismatched or
// unknown id both report "not found" so a guessed/leaked session id can't
// be used to distinguish "exists but isn't yours" from "doesn't exist".
func (s *Store) Get(id string, userID int64) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok || sess.UserID != userID {
		return nil, false
	}
	return sess, ok
}

// Count is the number of open sessions, for tests to assert a preview
// actually created (or a confirm actually cleaned up) one.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}
