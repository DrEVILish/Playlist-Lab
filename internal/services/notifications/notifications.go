// Package notifications ports services/job-notifications.ts: an in-memory,
// per-user feed of background-job status (deemix downloads, schedule runs,
// imports, ...) for the header's live notification bell. Lost on restart by
// design - the same durability tradeoff the Node service makes, since
// nothing here needs to survive one.
package notifications

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusInProgress Status = "in-progress"
	StatusSuccess    Status = "success"
	StatusError      Status = "error"
)

type Type string

const (
	TypeDeemix     Type = "deemix"
	TypeLidarr     Type = "lidarr"
	TypeRetryMatch Type = "retry-match"
	TypeSchedule   Type = "schedule"
	TypeImport     Type = "import"
	TypeVanished   Type = "track-vanished"
	TypeAction     Type = "action"
	TypeMix        Type = "mix"
)

type Notification struct {
	ID        string
	Type      Type
	Title     string
	Detail    string
	Status    Status
	Progress  *int // nil = not yet known, matching the TS optional field
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Patch is a set of optional field updates - a nil pointer/empty string
// means "no new value", not "clear it", matching updateNotification()'s
// undefined-skips-the-field semantics (a progress event that hasn't
// resolved a field yet must never blank out a previously-reported value).
type Patch struct {
	Status   *Status
	Detail   *string
	Progress *int
}

const maxPerUser = 50

type Store struct {
	mu   sync.Mutex
	byID map[int64][]Notification

	subMu     sync.Mutex
	subs      map[int]chan int64
	nextSubID int
}

func NewStore() *Store {
	return &Store{byID: map[int64][]Notification{}, subs: map[int]chan int64{}}
}

// Subscribe registers a listener for "this user's feed changed" events (one
// per connected SSE tab, matching onNotificationsChanged's per-listener
// design). The returned channel is buffered and never closed by the store;
// call the returned unsubscribe func when the connection ends.
func (s *Store) Subscribe() (<-chan int64, func()) {
	ch := make(chan int64, 16)
	s.subMu.Lock()
	id := s.nextSubID
	s.nextSubID++
	s.subs[id] = ch
	s.subMu.Unlock()

	return ch, func() {
		s.subMu.Lock()
		delete(s.subs, id)
		s.subMu.Unlock()
	}
}

func (s *Store) notify(userID int64) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- userID:
		default: // a slow/dead subscriber must never block a notification write
		}
	}
}

func (s *Store) Add(userID int64, typ Type, title, detail string, status Status, progress *int) Notification {
	n := Notification{
		ID: uuid.NewString(), Type: typ, Title: title, Detail: detail, Status: status, Progress: progress,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	s.mu.Lock()
	list := append([]Notification{n}, s.byID[userID]...)
	if len(list) > maxPerUser {
		list = list[:maxPerUser]
	}
	s.byID[userID] = list
	s.mu.Unlock()
	s.notify(userID)
	return n
}

func (s *Store) Update(userID int64, id string, patch Patch) {
	s.mu.Lock()
	list := s.byID[userID]
	found := false
	for i := range list {
		if list[i].ID != id {
			continue
		}
		if patch.Status != nil {
			list[i].Status = *patch.Status
		}
		if patch.Detail != nil {
			list[i].Detail = *patch.Detail
		}
		if patch.Progress != nil {
			list[i].Progress = patch.Progress
		}
		list[i].UpdatedAt = time.Now()
		found = true
		break
	}
	s.mu.Unlock()
	if found {
		s.notify(userID)
	}
}

func (s *Store) List(userID int64) []Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Notification, len(s.byID[userID]))
	copy(out, s.byID[userID])
	return out
}

func (s *Store) Dismiss(userID int64, id string) {
	s.mu.Lock()
	list := s.byID[userID]
	out := list[:0]
	for _, n := range list {
		if n.ID != id {
			out = append(out, n)
		}
	}
	s.byID[userID] = out
	s.mu.Unlock()
	s.notify(userID)
}

// Clear removes finished notifications - an in-progress job is never
// cleared (its entry is the only place its progress is reported).
// completedOnly keeps failures too, so "Clear complete" leaves exactly the
// things still worth looking at.
func (s *Store) Clear(userID int64, completedOnly bool) {
	s.mu.Lock()
	list := s.byID[userID]
	out := list[:0]
	for _, n := range list {
		if n.Status == StatusInProgress || (completedOnly && n.Status == StatusError) {
			out = append(out, n)
		}
	}
	s.byID[userID] = out
	s.mu.Unlock()
	s.notify(userID)
}
