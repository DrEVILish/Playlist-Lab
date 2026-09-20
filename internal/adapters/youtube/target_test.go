package youtube

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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

func TestCleanTrackTitle(t *testing.T) {
	cases := map[string]string{
		"Bohemian Rhapsody (Remastered 2011)": "Bohemian Rhapsody",
		"Hotel California Remastered":         "Hotel California",
		"Stairway   to   Heaven":              "Stairway to Heaven",
		// The regex is \bremastered?\b (faithfully ported from the TS
		// source), which only matches "remaster" when followed by an "e" -
		// i.e. "remastered"/"remastere", not the bare 8-letter "remaster".
		"Song Remastered": "Song",
	}
	for in, want := range cases {
		if got := cleanTrackTitle(in); got != want {
			t.Errorf("cleanTrackTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSimilarity_ExactMatchIs100(t *testing.T) {
	if got := similarity("Bohemian Rhapsody", "Bohemian Rhapsody"); got != 100 {
		t.Fatalf("expected 100, got %v", got)
	}
}

func TestSimilarity_CompletelyDifferentIsLow(t *testing.T) {
	got := similarity("Bohemian Rhapsody", "Completely Different Song")
	if got >= 50 {
		t.Fatalf("expected a low score for unrelated strings, got %v", got)
	}
}

func TestSimilarity_EmptyStringIsZero(t *testing.T) {
	if got := similarity("", "anything"); got != 0 {
		t.Fatalf("expected 0 for an empty input, got %v", got)
	}
}

func TestApplyBoostsAndPenalties(t *testing.T) {
	base := 70.0
	if got := applyBoostsAndPenalties(base, "Song Official Video"); got != base+15 {
		t.Fatalf("expected +15 official boost, got %v", got)
	}
	if got := applyBoostsAndPenalties(base, "Song (Live at Wembley)"); got != base-20 {
		t.Fatalf("expected -20 live penalty, got %v", got)
	}
	if got := applyBoostsAndPenalties(base, "Song (Lyrics)"); got != base-20 {
		t.Fatalf("expected -20 lyrics penalty, got %v", got)
	}
	if got := applyBoostsAndPenalties(base, "Song (Acoustic)"); got != base-20 {
		t.Fatalf("expected -20 acoustic penalty, got %v", got)
	}
}

func TestApplyBoostsAndPenalties_ClampsToRange(t *testing.T) {
	if got := applyBoostsAndPenalties(95, "Song Official"); got != 100 {
		t.Fatalf("expected the +15 boost to clamp at 100, got %v", got)
	}
	if got := applyBoostsAndPenalties(10, "Song (Live) (Lyrics) (Acoustic)"); got != 0 {
		t.Fatalf("expected stacked penalties to clamp at 0, got %v", got)
	}
}

// GetValidAccessToken mirrors youtube-oauth.ts's getValidAccessToken(): reuse
// a token that isn't within 5 minutes of expiring, refresh one that is, and
// delete the connection entirely (forcing reconnect) if the refresh call
// itself fails.
func TestGetValidAccessToken_ValidTokenIsReusedWithoutRefresh(t *testing.T) {
	sqlDB := newTestDB(t)
	userID := newTestUser(t, sqlDB)

	encAccess, _ := crypto.Encrypt("valid-access-token", testSecret)
	encRefresh, _ := crypto.Encrypt("refresh-token", testSecret)
	expiresAt := time.Now().Add(time.Hour)
	if err := db.SaveOAuthConnection(sqlDB, userID, ServiceName, encAccess, encRefresh, &expiresAt); err != nil {
		t.Fatalf("SaveOAuthConnection: %v", err)
	}

	oauth := OAuthConfig{ClientID: "id", ClientSecret: "secret"}
	token, err := GetValidAccessToken(sqlDB, testSecret, oauth, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "valid-access-token" {
		t.Fatalf("expected the stored token to be reused untouched, got %q", token)
	}
}

func TestGetValidAccessToken_ExpiringSoonTriggersRefresh(t *testing.T) {
	sqlDB := newTestDB(t)
	userID := newTestUser(t, sqlDB)

	encAccess, _ := crypto.Encrypt("old-access-token", testSecret)
	encRefresh, _ := crypto.Encrypt("refresh-token", testSecret)
	soonExpiry := time.Now().Add(4 * time.Minute) // within the 5-minute buffer
	if err := db.SaveOAuthConnection(sqlDB, userID, ServiceName, encAccess, encRefresh, &soonExpiry); err != nil {
		t.Fatalf("SaveOAuthConnection: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"new-access-token","refresh_token":"refresh-token","expires_in":3600}`))
	}))
	defer srv.Close()
	origTokenURL := tokenURL
	tokenURL = srv.URL
	defer func() { tokenURL = origTokenURL }()

	oauth := OAuthConfig{ClientID: "id", ClientSecret: "secret"}
	token, err := GetValidAccessToken(sqlDB, testSecret, oauth, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "new-access-token" {
		t.Fatalf("expected the refreshed token, got %q", token)
	}

	conn, err := db.GetOAuthConnection(sqlDB, userID, ServiceName)
	if err != nil || conn == nil {
		t.Fatalf("expected the refreshed token to be persisted: conn=%+v err=%v", conn, err)
	}
}

func TestGetValidAccessToken_RefreshFailureDeletesConnection(t *testing.T) {
	sqlDB := newTestDB(t)
	userID := newTestUser(t, sqlDB)

	encAccess, _ := crypto.Encrypt("old-access-token", testSecret)
	encRefresh, _ := crypto.Encrypt("refresh-token", testSecret)
	pastExpiry := time.Now().Add(-time.Minute)
	if err := db.SaveOAuthConnection(sqlDB, userID, ServiceName, encAccess, encRefresh, &pastExpiry); err != nil {
		t.Fatalf("SaveOAuthConnection: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error_description":"invalid_grant"}`))
	}))
	defer srv.Close()
	origTokenURL := tokenURL
	tokenURL = srv.URL
	defer func() { tokenURL = origTokenURL }()

	oauth := OAuthConfig{ClientID: "id", ClientSecret: "secret"}
	_, err := GetValidAccessToken(sqlDB, testSecret, oauth, userID)
	if err == nil || err.Error() != "YouTube session expired. Please reconnect your YouTube account" {
		t.Fatalf("expected the reconnect error, got %v", err)
	}

	conn, err := db.GetOAuthConnection(sqlDB, userID, ServiceName)
	if err != nil {
		t.Fatalf("GetOAuthConnection: %v", err)
	}
	if conn != nil {
		t.Fatalf("expected the connection to be deleted after a failed refresh, still found: %+v", conn)
	}
}

func TestGetValidAccessToken_NoConnectionReturnsNotConnectedError(t *testing.T) {
	sqlDB := newTestDB(t)
	userID := newTestUser(t, sqlDB)

	oauth := OAuthConfig{ClientID: "id", ClientSecret: "secret"}
	_, err := GetValidAccessToken(sqlDB, testSecret, oauth, userID)
	if err == nil || err.Error() != "not connected to YouTube. Please authenticate first" {
		t.Fatalf("expected a not-connected error, got %v", err)
	}
}

func TestGetValidAccessToken_NoRefreshTokenReturnsStaleAccessTokenAsIs(t *testing.T) {
	// Mirrors the Go source's fallback branch: an expired token with no
	// refresh token to fall back on just returns the (stale) decrypted
	// access token rather than erroring - matching GetValidAccessToken's
	// current behavior exactly (there is no refresh_token to attempt a
	// refresh with).
	sqlDB := newTestDB(t)
	userID := newTestUser(t, sqlDB)

	encAccess, _ := crypto.Encrypt("stale-access-token", testSecret)
	pastExpiry := time.Now().Add(-time.Hour)
	if err := db.SaveOAuthConnection(sqlDB, userID, ServiceName, encAccess, "", &pastExpiry); err != nil {
		t.Fatalf("SaveOAuthConnection: %v", err)
	}

	oauth := OAuthConfig{ClientID: "id", ClientSecret: "secret"}
	token, err := GetValidAccessToken(sqlDB, testSecret, oauth, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "stale-access-token" {
		t.Fatalf("got %q", token)
	}
}
