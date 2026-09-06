package spotify

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/crypto"
	"github.com/drevilish/playlist-lab/internal/db"
)

const testSecret = "test-secret"

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return sqlDB
}

func newTestUser(t *testing.T, sqlDB *sql.DB) int64 {
	t.Helper()
	res, err := sqlDB.Exec(
		`INSERT INTO users (plex_user_id, plex_username, plex_token, created_at, last_login) VALUES (?, ?, ?, ?, ?)`,
		"plex-1", "tester", "tok", time.Now().UnixMilli(), time.Now().UnixMilli(),
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// GetToken's "null means not connected" contract (spotify-auth.ts's
// getSpotifyToken): a user who has never gone through Spotify OAuth must get
// back an empty token and a nil error, not an error, so callers can tell
// "not connected" apart from "something went wrong".
func TestGetToken_NeverConnected_ReturnsEmptyNoError(t *testing.T) {
	sqlDB := newTestDB(t)
	userID := newTestUser(t, sqlDB)

	token, err := GetToken(sqlDB, testSecret, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "" {
		t.Fatalf("expected an empty token for a never-connected user, got %q", token)
	}
}

func TestGetToken_ValidUnexpiredToken_DecryptsAndReturns(t *testing.T) {
	sqlDB := newTestDB(t)
	userID := newTestUser(t, sqlDB)

	encrypted, err := crypto.Encrypt("real-access-token", testSecret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if err := db.SaveSpotifyTokens(sqlDB, userID, encrypted, "", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SaveSpotifyTokens: %v", err)
	}

	token, err := GetToken(sqlDB, testSecret, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "real-access-token" {
		t.Fatalf("got %q, want the decrypted access token", token)
	}
}

// An expired token with no refresh token to fall back on is the same "not
// connected" case as never having authenticated - no error, just nothing
// usable to hand the caller.
func TestGetToken_ExpiredWithNoRefreshToken_ReturnsEmptyNoError(t *testing.T) {
	sqlDB := newTestDB(t)
	userID := newTestUser(t, sqlDB)

	encrypted, err := crypto.Encrypt("stale-access-token", testSecret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if err := db.SaveSpotifyTokens(sqlDB, userID, encrypted, "", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("SaveSpotifyTokens: %v", err)
	}

	token, err := GetToken(sqlDB, testSecret, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "" {
		t.Fatalf("expected an empty token for an expired, unrefreshable session, got %q", token)
	}
}

// scoreResult/toMatchResult are the two pure functions this adapter uses to
// turn a raw Spotify search hit into the shared MatchResult shape; the
// match/no-match threshold (>=50 in MatchTracks) and artist-picking (first
// credited artist) are worth pinning independent of any network call.
func TestScoreResult_ExactMatchScoresHigh(t *testing.T) {
	item := spotifyTrack{Name: "Yesterday", Artists: []struct {
		Name string `json:"name"`
	}{{Name: "The Beatles"}}}
	if score := scoreResult("Yesterday", "The Beatles", item); score < 90 {
		t.Fatalf("expected an exact title+artist match to score highly, got %v", score)
	}
}

func TestToMatchResult_UsesFirstArtistOnly(t *testing.T) {
	item := spotifyTrack{URI: "spotify:track:1", Name: "Song", Artists: []struct {
		Name string `json:"name"`
	}{{Name: "Primary"}, {Name: "Featured"}}}
	result := toMatchResult(adapters.TrackInfo{Title: "Song", Artist: "Primary"}, item, 90, true)
	if result.TargetArtist != "Primary" {
		t.Fatalf("expected the first credited artist, got %q", result.TargetArtist)
	}
}
