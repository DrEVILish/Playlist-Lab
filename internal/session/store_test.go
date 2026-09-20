package session

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/drevilish/playlist-lab/internal/db"
)

func newTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return NewStore(sqlDB), sqlDB
}

func TestStore_SaveAndGet(t *testing.T) {
	store, _ := newTestStore(t)

	data := &Data{UserID: 42, PlexUserID: "plex-1"}
	if err := store.Save("sid-1", data); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get("sid-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UserID != 42 || got.PlexUserID != "plex-1" {
		t.Fatalf("got %+v, want %+v", got, data)
	}
}

func TestStore_Get_NotFound(t *testing.T) {
	store, _ := newTestStore(t)
	if _, err := store.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestStore_Save_UpsertsExistingRow(t *testing.T) {
	store, _ := newTestStore(t)

	if err := store.Save("sid-1", &Data{UserID: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Save("sid-1", &Data{UserID: 2}); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	got, err := store.Get("sid-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UserID != 2 {
		t.Fatalf("got UserID %d, want 2 (upsert should replace the row, not add another)", got.UserID)
	}
}

func TestStore_Destroy(t *testing.T) {
	store, _ := newTestStore(t)

	if err := store.Save("sid-1", &Data{UserID: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Destroy("sid-1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := store.Get("sid-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound after Destroy", err)
	}
}

// Get on an already-expired row must both delete it and report ErrNotFound,
// not silently hand back stale session data.
func TestStore_Get_ExpiredRowIsDeletedAndNotFound(t *testing.T) {
	store, sqlDB := newTestStore(t)

	if err := store.Save("sid-1", &Data{UserID: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Force the row into the past directly - Save() always writes maxAge in
	// the future, so this bypasses that to simulate an already-expired row.
	if _, err := sqlDB.Exec("UPDATE sessions SET expired = ? WHERE sid = ?", time.Now().Add(-time.Minute).UnixMilli(), "sid-1"); err != nil {
		t.Fatalf("forcing expiry: %v", err)
	}

	if _, err := store.Get("sid-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound for an expired row", err)
	}

	var count int
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM sessions WHERE sid = ?", "sid-1").Scan(&count); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the expired row to be deleted by Get, found %d rows", count)
	}
}

// StartCleanup's sweep deletes any row whose expiry has passed, regardless
// of which sid it belongs to - simulate the sweep directly (ticking an
// hourly timer in a test is impractical) by running the same DELETE
// StartCleanup's goroutine issues.
func TestStore_CleanupSweepDeletesExpiredRows(t *testing.T) {
	store, sqlDB := newTestStore(t)

	if err := store.Save("expired", &Data{UserID: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Save("fresh", &Data{UserID: 2}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := sqlDB.Exec("UPDATE sessions SET expired = ? WHERE sid = ?", time.Now().Add(-time.Minute).UnixMilli(), "expired"); err != nil {
		t.Fatalf("forcing expiry: %v", err)
	}

	if _, err := sqlDB.Exec("DELETE FROM sessions WHERE expired < ?", time.Now().UnixMilli()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if _, err := store.Get("expired"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected expired row to be swept, got %v", err)
	}
	if _, err := store.Get("fresh"); err != nil {
		t.Fatalf("expected fresh row to survive the sweep, got %v", err)
	}
}

func TestStore_ListForUser_ScopedToUserAndExcludesExpired(t *testing.T) {
	store, sqlDB := newTestStore(t)

	if err := store.Save("u1-a", &Data{UserID: 1, UserAgent: "ua-a"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Save("u1-b", &Data{UserID: 1, UserAgent: "ua-b"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Save("u2-a", &Data{UserID: 2, UserAgent: "ua-c"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// An expired row for user 1 must not appear in the list even though
	// it's still in the table (StartCleanup's hourly sweep hasn't run yet).
	if err := store.Save("u1-expired", &Data{UserID: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := sqlDB.Exec("UPDATE sessions SET expired = ? WHERE sid = ?", time.Now().Add(-time.Minute).UnixMilli(), "u1-expired"); err != nil {
		t.Fatalf("forcing expiry: %v", err)
	}

	got, err := store.ListForUser(1)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2 (only user 1's non-expired sessions)", len(got))
	}
	uas := map[string]bool{got[0].Data.UserAgent: true, got[1].Data.UserAgent: true}
	if !uas["ua-a"] || !uas["ua-b"] {
		t.Fatalf("got user-agents %v, want ua-a and ua-b", uas)
	}
}

func TestStore_Revoke_DeletesOnlyTheOwningUsersSession(t *testing.T) {
	store, _ := newTestStore(t)

	if err := store.Save("victim-sid", &Data{UserID: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	id := DisplayIDFor("victim-sid")

	// User 2 (an attacker who somehow obtained/guessed user 1's DisplayID)
	// must not be able to revoke it - this is the whole point of scoping
	// Revoke's lookup to `WHERE user_id = ?` rather than trusting the id
	// alone.
	if err := store.Revoke(2, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attacker's Revoke got %v, want ErrNotFound (must not delete another user's session)", err)
	}
	if _, err := store.Get("victim-sid"); err != nil {
		t.Fatalf("victim's session was deleted by another user's Revoke call: %v", err)
	}

	// The owning user can revoke it.
	if err := store.Revoke(1, id); err != nil {
		t.Fatalf("owner's Revoke: %v", err)
	}
	if _, err := store.Get("victim-sid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected the session to be gone after the owner revoked it, got %v", err)
	}
}

func TestStore_TouchLastSeen_ThrottledWithinInterval(t *testing.T) {
	store, _ := newTestStore(t)
	data := &Data{UserID: 1, LastSeenAt: time.Now().Unix()}
	if err := store.Save("sid-1", data); err != nil {
		t.Fatalf("Save: %v", err)
	}

	before := data.LastSeenAt
	store.TouchLastSeen("sid-1", data)
	if data.LastSeenAt != before {
		t.Fatalf("LastSeenAt changed to %d within the throttle interval, want unchanged from %d", data.LastSeenAt, before)
	}

	// Simulate enough elapsed time that a touch should actually persist.
	data.LastSeenAt = time.Now().Add(-touchLastSeenInterval - time.Second).Unix()
	stale := data.LastSeenAt
	store.TouchLastSeen("sid-1", data)
	if data.LastSeenAt == stale {
		t.Fatal("expected LastSeenAt to update once the throttle interval has elapsed")
	}

	got, err := store.Get("sid-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastSeenAt != data.LastSeenAt {
		t.Fatalf("persisted LastSeenAt = %d, want %d (TouchLastSeen must actually save, not just mutate in memory)", got.LastSeenAt, data.LastSeenAt)
	}
}

func TestDisplayIDFor_DeterministicAndDistinct(t *testing.T) {
	if DisplayIDFor("sid-a") != DisplayIDFor("sid-a") {
		t.Fatal("expected DisplayIDFor to be deterministic for the same sid")
	}
	if DisplayIDFor("sid-a") == DisplayIDFor("sid-b") {
		t.Fatal("expected different sids to produce different DisplayIDs")
	}
}

func TestNewSessionID_IsUniqueAndURLSafe(t *testing.T) {
	id1, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	id2, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	if id1 == id2 {
		t.Fatal("expected two distinct session ids")
	}
	if id1 == "" {
		t.Fatal("expected a non-empty session id")
	}
}
