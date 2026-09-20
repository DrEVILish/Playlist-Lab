// Package crossimport tracks in-flight cross-service import sessions
// (Plex playlist -> YouTube, source/target picked by the caller through
// internal/adapters.Registry). It ports routes/cross-import.ts's
// matchSessions/matchProgressState/cancelledSessions maps, collapsed into
// one struct per session instead of three parallel maps keyed the same way.
//
// Unlike mixes.go (phase 6b), which reuses actionqueue+notifications for
// its whole progress story, cross-import keeps its own small session store:
// after matching finishes the user reviews the full per-track match list
// and can override or skip individual tracks before the playlist is
// actually created - that reviewed, mutated result set has to live
// somewhere addressable by session ID between the "matching done" and
// "user clicked execute" requests, which the notification feed (a status
// string + one detail line) has no room for. Progress *during* matching is
// still surfaced through the same notification bell as everything else;
// this store exists only for the extra review-state need.
package crossimport

import (
	"sync"

	"github.com/drevilish/playlist-lab/internal/adapters"
)

type Phase string

const (
	PhaseFetching Phase = "fetching"
	PhaseMatching Phase = "matching"
	PhaseReview   Phase = "review"
	PhaseError    Phase = "error"
	PhaseDone     Phase = "done"
)

type Progress struct {
	Phase        Phase
	Current      int
	Total        int
	PlaylistName string
	CurrentTrack string
	Message      string
}

// Session is one cross-import run: source playlist -> target service.
// Results is the live, mutable review state - overrides/skips from the
// review page are written directly into it by index.
type Session struct {
	mu sync.Mutex

	JobID        int64
	TargetID     string
	TargetConfig adapters.TargetConfig
	PlaylistName string

	Progress  Progress
	Results   []adapters.MatchResult
	Cancelled bool
}

func (s *Session) SetProgress(p Progress) {
	s.mu.Lock()
	s.Progress = p
	s.mu.Unlock()
}

func (s *Session) GetProgress() Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Progress
}

func (s *Session) SetPlaylistName(name string) {
	s.mu.Lock()
	s.PlaylistName = name
	s.mu.Unlock()
}

func (s *Session) GetPlaylistName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.PlaylistName
}

func (s *Session) SetResults(results []adapters.MatchResult) {
	s.mu.Lock()
	s.Results = results
	s.Progress.Phase = PhaseReview
	s.mu.Unlock()
}

func (s *Session) GetResults() []adapters.MatchResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]adapters.MatchResult, len(s.Results))
	copy(out, s.Results)
	return out
}

// Update mutates one track's review state (override or skip) by index -
// the review table posts back the row index it rendered.
func (s *Session) Update(index int, fn func(*adapters.MatchResult)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.Results) {
		return false
	}
	fn(&s.Results[index])
	return true
}

func (s *Session) Cancel() {
	s.mu.Lock()
	s.Cancelled = true
	s.mu.Unlock()
}

func (s *Session) IsCancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Cancelled
}

// Store is a plain in-memory session map, same durability tradeoff as
// notifications.Store: a session lost on restart is trivially re-run by the
// user (matching hasn't been persisted anywhere durable in the Node app
// either - only the counts on cross_import_jobs survive).
type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func NewStore() *Store {
	return &Store{sessions: map[string]*Session{}}
}

func (s *Store) New(sessionID string, jobID int64, targetID string, cfg adapters.TargetConfig) *Session {
	sess := &Session{JobID: jobID, TargetID: targetID, TargetConfig: cfg}
	s.mu.Lock()
	s.sessions[sessionID] = sess
	s.mu.Unlock()
	return sess
}

func (s *Store) Get(sessionID string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	return sess, ok
}

func (s *Store) Delete(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
}
