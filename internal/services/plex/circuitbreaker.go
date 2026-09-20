package plex

import (
	"sync"
	"time"
)

// When a Plex server stops answering, every request to it costs the full
// 60s timeout. A page that makes a handful of Plex calls then hangs for
// minutes instead of failing, which is what made the whole UI feel dead
// whenever Plex was busy. After a connection-level failure the server is
// treated as down for a short cooldown and further calls fail immediately,
// so one request pays the timeout and the rest return at once with
// something the UI can show. Ported from plex.ts's unreachableUntil map.
//
// ponytail: a plain cooldown, not a half-open probe - the first call after
// it expires is the probe. Add proper half-open state if a busy server ends
// up flapping in and out of the cooldown.
var (
	unreachableMu    sync.Mutex
	unreachableUntil = map[string]time.Time{}
)

const cooldown = 30 * time.Second

func checkUnreachable(serverURL string) (down bool, message string) {
	unreachableMu.Lock()
	defer unreachableMu.Unlock()
	until, ok := unreachableUntil[serverURL]
	if ok && time.Now().Before(until) {
		return true, "Plex server is not responding"
	}
	return false, ""
}

func markConnectionFailure(serverURL string, _ error) {
	unreachableMu.Lock()
	defer unreachableMu.Unlock()
	unreachableUntil[serverURL] = time.Now().Add(cooldown)
}

func clearUnreachable(serverURL string) {
	unreachableMu.Lock()
	defer unreachableMu.Unlock()
	delete(unreachableUntil, serverURL)
}
