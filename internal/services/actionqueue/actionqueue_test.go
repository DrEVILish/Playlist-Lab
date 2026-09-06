package actionqueue

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

func TestQueue_CapsConcurrency(t *testing.T) {
	store := notifications.NewStore()
	q := New(store)

	const jobCount = 10
	var current, maxObserved int32
	var wg sync.WaitGroup
	wg.Add(jobCount)

	release := make(chan struct{})
	for i := 0; i < jobCount; i++ {
		q.Enqueue(1, "job", notifications.TypeAction, func(notificationID string) error {
			defer wg.Done()
			n := atomic.AddInt32(&current, 1)
			for {
				old := atomic.LoadInt32(&maxObserved)
				if n <= old || atomic.CompareAndSwapInt32(&maxObserved, old, n) {
					break
				}
			}
			<-release
			atomic.AddInt32(&current, -1)
			return nil
		})
	}

	// Give the first batch a moment to start, then confirm no more than
	// maxConcurrent are ever running at once before releasing them all.
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&maxObserved); got > maxConcurrent {
		t.Fatalf("expected at most %d concurrent jobs, observed %d", maxConcurrent, got)
	}
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&maxObserved); got == 0 || got > maxConcurrent {
		t.Fatalf("expected between 1 and %d concurrent jobs to have run, observed max %d", maxConcurrent, got)
	}
}

func TestQueue_FailedJobReportsError(t *testing.T) {
	store := notifications.NewStore()
	q := New(store)

	done := make(chan struct{})
	jobID, _ := q.Enqueue(1, "job", notifications.TypeAction, func(notificationID string) error {
		defer close(done)
		return errFailed
	})
	<-done
	time.Sleep(10 * time.Millisecond) // let run()'s deferred update land

	for _, n := range store.List(1) {
		if n.ID == jobID {
			if n.Status != notifications.StatusError {
				t.Fatalf("expected status error, got %v", n.Status)
			}
			return
		}
	}
	t.Fatal("notification not found")
}

var errFailed = &testError{"handler failed"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
