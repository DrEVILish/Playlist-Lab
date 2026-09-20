package db

import (
	"testing"
	"time"
)

// TestCacheTimestampAndReuse covers Property 9 (cache timestamp storage) and
// Property 38 (cached playlist reuse): saving once and reading back must
// include a scraped_at timestamp, and repeated reads return the same data.
func TestCacheTimestampAndReuse(t *testing.T) {
	sqlDB := newTestDB(t)

	tracks := []CachedTrack{{Title: "T1", Artist: "A1", Album: "Al1"}}
	before := time.Now().Unix()
	if err := SaveCachedPlaylist(sqlDB, "spotify", "src1", "My Playlist", "desc", tracks, "http://cover"); err != nil {
		t.Fatalf("SaveCachedPlaylist: %v", err)
	}
	after := time.Now().Unix()

	cached, err := GetCachedPlaylist(sqlDB, "spotify", "src1")
	if err != nil || cached == nil {
		t.Fatalf("GetCachedPlaylist: %v got=%v", err, cached)
	}
	if cached.ScrapedAt < before || cached.ScrapedAt > after {
		t.Fatalf("expected scraped_at in [%d,%d], got %d", before, after, cached.ScrapedAt)
	}
	if len(cached.Tracks) != 1 || cached.Tracks[0].Title != "T1" {
		t.Fatalf("unexpected tracks: %+v", cached.Tracks)
	}

	// Multiple "users" reading the same cache key get identical data (reuse).
	second, err := GetCachedPlaylist(sqlDB, "spotify", "src1")
	if err != nil || second == nil {
		t.Fatalf("second GetCachedPlaylist: %v got=%v", err, second)
	}
	if second.ScrapedAt != cached.ScrapedAt || second.Name != cached.Name {
		t.Fatalf("expected identical cached data on reuse, got %+v vs %+v", cached, second)
	}
}

func TestGetCachedPlaylistMiss(t *testing.T) {
	sqlDB := newTestDB(t)
	got, err := GetCachedPlaylist(sqlDB, "spotify", "nonexistent")
	if err != nil {
		t.Fatalf("GetCachedPlaylist: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing cache entry, got %+v", got)
	}
}

// TestSaveCachedPlaylistUpsert exercises the UNIQUE(source, source_id)
// constraint's ON CONFLICT upsert path.
func TestSaveCachedPlaylistUpsert(t *testing.T) {
	sqlDB := newTestDB(t)
	tracks := []CachedTrack{{Title: "T1", Artist: "A1"}}

	if err := SaveCachedPlaylist(sqlDB, "spotify", "src1", "Name 1", "d1", tracks, ""); err != nil {
		t.Fatalf("SaveCachedPlaylist: %v", err)
	}
	if err := SaveCachedPlaylist(sqlDB, "spotify", "src1", "Name 2", "d2", tracks, ""); err != nil {
		t.Fatalf("SaveCachedPlaylist (upsert): %v", err)
	}

	var count int
	sqlDB.QueryRow("SELECT COUNT(*) FROM cached_playlists WHERE source = ? AND source_id = ?", "spotify", "src1").Scan(&count)
	if count != 1 {
		t.Fatalf("expected upsert to replace, not duplicate, row: got %d rows", count)
	}

	got, _ := GetCachedPlaylist(sqlDB, "spotify", "src1")
	if got.Name != "Name 2" {
		t.Fatalf("expected upserted name, got %s", got.Name)
	}
}

// TestDeleteOldCache is Property 10 (cache staleness) via the deletion path
// this Go codebase actually exposes (DeleteOldCache); there is no separate
// getStaleCache reader here, so we assert the age-based cutoff directly.
func TestDeleteOldCache(t *testing.T) {
	sqlDB := newTestDB(t)
	now := time.Now().Unix()

	fresh := now - 3600     // 1 hour old
	stale := now - 3*86400  // 3 days old

	insert := func(sourceID string, scrapedAt int64) {
		_, err := sqlDB.Exec(
			`INSERT INTO cached_playlists (source, source_id, name, tracks, scraped_at) VALUES (?, ?, ?, ?, ?)`,
			"spotify", sourceID, "name", "[]", scrapedAt,
		)
		if err != nil {
			t.Fatalf("insert cache row: %v", err)
		}
	}
	insert("fresh", fresh)
	insert("stale", stale)

	deleted, err := DeleteOldCache(sqlDB, 1) // older than 1 day
	if err != nil {
		t.Fatalf("DeleteOldCache: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 row deleted, got %d", deleted)
	}

	if got, _ := GetCachedPlaylist(sqlDB, "spotify", "fresh"); got == nil {
		t.Fatal("expected fresh cache entry to survive")
	}
	if got, _ := GetCachedPlaylist(sqlDB, "spotify", "stale"); got != nil {
		t.Fatal("expected stale cache entry to be deleted")
	}
}
