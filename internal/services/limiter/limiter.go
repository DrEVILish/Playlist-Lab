// Package limiter provides the app-wide concurrency caps ported from
// task-queues.ts: one in-flight call per resource type (Plex, other
// external services, matching), regardless of which feature triggered it,
// so a burst of unrelated background work can't stack its own local caps
// and overload a shared resource. This is a concurrency cap, not a
// requests-per-second rate - a semaphore is the right primitive, not
// golang.org/x/time/rate (which throttles by time, not by how many calls
// are in flight at once).
package limiter

// Semaphore bounds how many callers may hold it at once.
type Semaphore chan struct{}

func New(max int) Semaphore {
	return make(Semaphore, max)
}

func (s Semaphore) Acquire() {
	s <- struct{}{}
}

func (s Semaphore) Release() {
	<-s
}

// Run acquires the semaphore, runs fn, and always releases - the Go
// equivalent of Limiter.run() in task-queues.ts.
func Run[T any](s Semaphore, fn func() (T, error)) (T, error) {
	s.Acquire()
	defer s.Release()
	return fn()
}

// One in-flight call per resource type, app-wide - see task-queues.ts for
// why this is capped to strictly one rather than a small pool.
var (
	Plex     = New(1)
	External = New(1)
	Matching = New(1)
)
