package logging

import (
	"strings"
	"sync"
)

// broadcaster fans out each formatted log line to every subscribed admin
// Logs viewer (DESIGN.md §11.5/§17: live-tail instead of polling). It's an
// io.Writer so it can sit alongside the stdout/combined.log/error.log
// handlers in Setup's multiHandler - slog.NewJSONHandler already does all
// the formatting work; this just captures a copy of each line instead of
// writing it anywhere persistent.
type broadcaster struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

func newBroadcaster() *broadcaster {
	return &broadcaster{subs: map[chan string]struct{}{}}
}

// Write implements io.Writer. Never returns an error and never blocks on a
// slow/absent subscriber - a stuck admin Logs tab must not be able to stall
// or fail the application's actual logging.
func (b *broadcaster) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	if line != "" {
		b.mu.Lock()
		for ch := range b.subs {
			select {
			case ch <- line:
			default: // subscriber's buffer is full - drop rather than block logging
			}
		}
		b.mu.Unlock()
	}
	return len(p), nil
}

func (b *broadcaster) subscribe() (<-chan string, func()) {
	ch := make(chan string, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
		close(ch)
	}
}

var live = newBroadcaster()

// Subscribe returns a channel of raw JSON log lines (the same format
// combined.log's rows are in) as they're written, plus an unsubscribe func
// that must be called when the caller is done (deferred, same pattern as
// notifications.Store.Subscribe). Used by the admin Logs tab's SSE stream.
func Subscribe() (<-chan string, func()) {
	return live.subscribe()
}
