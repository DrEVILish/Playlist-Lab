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

// Session holds one in-progress import's editable review state between
// POST /import/preview and POST /import/confirm/:sessionId.
type Session struct {
	ID               string
	Source           string
	SourceIdentifier string
	PlaylistName     string
	CoverURL         string
	CreatedAt        time.Time

	mu     sync.Mutex
	tracks []Track
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

func (s *Store) New(source, sourceIdentifier, playlistName, coverURL string) *Session {
	sess := &Session{
		ID: uuid.NewString(), Source: source, SourceIdentifier: sourceIdentifier,
		PlaylistName: playlistName, CoverURL: coverURL, CreatedAt: time.Now(),
	}
	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()
	return sess
}

func (s *Store) Get(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
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
