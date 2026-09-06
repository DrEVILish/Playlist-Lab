// Package actionqueue ports services/action-queue.ts: a shared, cross-user
// FIFO queue for server-side actions that do real Plex/API work (playlist
// reorders, AI generation, cross-service imports, ...), bounded to a small
// worker pool so a burst of requests from multiple users can't all run at
// once, while each user can see how far down the queue their own request
// is. In-memory only - these actions are seconds-to-low-minutes and
// trivially re-clickable, so losing a queued/in-flight one on a rare
// restart is an acceptable tradeoff (same one the notifications package
// already makes).
package actionqueue

import (
	"log/slog"
	"strconv"
	"sync"

	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

const maxConcurrent = 3

type job struct {
	id      string
	userID  int64
	handler func(notificationID string) error
}

type Queue struct {
	notifications *notifications.Store

	mu     sync.Mutex
	queued []job
	active int
}

func New(store *notifications.Store) *Queue {
	return &Queue{notifications: store}
}

// Enqueue queues an action and returns immediately with the caller's own
// position (0 = about to run). handler does the real work and is
// responsible for updating its own notification to report progress and
// the final outcome, the same way every other background job does.
func (q *Queue) Enqueue(userID int64, title string, notifType notifications.Type, handler func(notificationID string) error) (jobID string, position int) {
	n := q.notifications.Add(userID, notifType, title, "Queued", notifications.StatusInProgress, nil)
	j := job{id: n.ID, userID: userID, handler: handler}

	q.mu.Lock()
	q.queued = append(q.queued, j)
	position = len(q.queued) - 1
	q.refreshQueuedPositionsLocked()
	q.mu.Unlock()

	q.processQueue()
	return j.id, position
}

// refreshQueuedPositionsLocked must be called with q.mu held.
func (q *Queue) refreshQueuedPositionsLocked() {
	for i, j := range q.queued {
		detail := "Up next"
		if i > 0 {
			detail = "Queued - position " + strconv.Itoa(i+1)
		}
		q.notifications.Update(j.userID, j.id, notifications.Patch{Detail: &detail})
	}
}

func (q *Queue) processQueue() {
	for {
		q.mu.Lock()
		if q.active >= maxConcurrent || len(q.queued) == 0 {
			q.mu.Unlock()
			return
		}
		j := q.queued[0]
		q.queued = q.queued[1:]
		q.active++
		processing := "Processing..."
		q.notifications.Update(j.userID, j.id, notifications.Patch{Detail: &processing})
		q.refreshQueuedPositionsLocked()
		q.mu.Unlock()

		go q.run(j)
	}
}

func (q *Queue) run(j job) {
	defer func() {
		q.mu.Lock()
		q.active--
		q.mu.Unlock()
		q.processQueue()
	}()

	// Handlers report their own success/error via their notification - this
	// only catches one that returned an error (or panicked) without doing
	// so itself, so the notification doesn't get stuck on "Processing..."
	// forever.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("action queue job panicked", "jobId", j.id, "userId", j.userID, "panic", r)
			status, detail := notifications.StatusError, "Action failed"
			q.notifications.Update(j.userID, j.id, notifications.Patch{Status: &status, Detail: &detail})
		}
	}()

	if err := j.handler(j.id); err != nil {
		slog.Error("action queue job failed unexpectedly", "jobId", j.id, "userId", j.userID, "error", err)
		status, detail := notifications.StatusError, err.Error()
		q.notifications.Update(j.userID, j.id, notifications.Patch{Status: &status, Detail: &detail})
	}
}
