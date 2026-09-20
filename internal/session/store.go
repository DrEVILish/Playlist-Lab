// Package session implements a SQLite-backed session store against the
// existing `sessions` table, replacing express-session + its custom
// SQLiteStore. Session rows are opaque JSON owned entirely by this package -
// there is no attempt to read/write express-session's serialization format,
// since cutover simply invalidates existing sessions (users log back in via
// Plex, which is a cheap, expected step) rather than needing wire
// compatibility with the Node server it replaces.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

const (
	CookieName = "playlist-lab.sid"
	maxAge     = 30 * 24 * time.Hour
)

var ErrNotFound = errors.New("session not found")

type Data struct {
	UserID     int64  `json:"userId,omitempty"`
	PlexUserID string `json:"plexUserId,omitempty"`

	// OAuthState is a single-use nonce set by oauthStart and checked by
	// oauthCallback (internal/handlers/cross_import.go) against the
	// `state` query param an OAuth provider echoes back, so a callback
	// can't be used to link an attacker's authorization code into a
	// victim's account (CSRF on the OAuth callback).
	OAuthState string `json:"oauthState,omitempty"`

	// UserAgent/IP/CreatedAt are captured once, at login (handlers/auth.go),
	// for Settings > Sessions' device list (DESIGN.md §11.4) - not
	// refreshed per-request since a device's identity doesn't change
	// mid-session. LastSeenAt is refreshed by touchLastSeen (below),
	// throttled rather than on every request to avoid a DB write per
	// page load under this app's single-writer SQLite connection.
	UserAgent  string `json:"userAgent,omitempty"`
	IP         string `json:"ip,omitempty"`
	CreatedAt  int64  `json:"createdAt,omitempty"`
	LastSeenAt int64  `json:"lastSeenAt,omitempty"`
}

type Store struct {
	db *sql.DB
}

func NewStore(sqlDB *sql.DB) *Store {
	return &Store{db: sqlDB}
}

// StartCleanup runs an hourly sweep of expired session rows, mirroring the
// interval the Node server's custom SQLiteStore used.
func (s *Store) StartCleanup(stop <-chan struct{}) {
	ticker := time.NewTicker(1 * time.Hour)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = s.db.Exec("DELETE FROM sessions WHERE expired < ?", time.Now().UnixMilli())
			case <-stop:
				return
			}
		}
	}()
}

func (s *Store) Get(sid string) (*Data, error) {
	var sess string
	var expired int64
	err := s.db.QueryRow("SELECT sess, expired FROM sessions WHERE sid = ?", sid).Scan(&sess, &expired)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if expired < time.Now().UnixMilli() {
		_, _ = s.db.Exec("DELETE FROM sessions WHERE sid = ?", sid)
		return nil, ErrNotFound
	}
	var data Data
	if err := json.Unmarshal([]byte(sess), &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (s *Store) Save(sid string, data *Data) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	expired := time.Now().Add(maxAge).UnixMilli()
	var userID sql.NullInt64
	if data.UserID != 0 {
		userID = sql.NullInt64{Int64: data.UserID, Valid: true}
	}
	_, err = s.db.Exec(
		`INSERT INTO sessions (sid, sess, expired, user_id) VALUES (?, ?, ?, ?)
		 ON CONFLICT(sid) DO UPDATE SET sess = excluded.sess, expired = excluded.expired, user_id = excluded.user_id`,
		sid, string(payload), expired, userID,
	)
	return err
}

func (s *Store) Destroy(sid string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE sid = ?", sid)
	return err
}

// touchLastSeenInterval throttles how often a request updates its session's
// LastSeenAt - without this, Settings > Sessions' "last active" column would
// mean a write on every single authenticated request (RequireAuth calls
// currentSessionData, which calls this), including the admin log viewer's
// 5s poll and the notification bell's SSE stream, against a database this
// app deliberately keeps to one writer connection (db.Open's
// SetMaxOpenConns(1)).
const touchLastSeenInterval = 5 * time.Minute

// TouchLastSeen updates data.LastSeenAt and persists it, but only if more
// than touchLastSeenInterval has passed since the last update - called by
// auth.Middleware.currentSessionData on every authenticated request, so it
// has to be cheap to skip. Mutates data in place so the caller's
// already-loaded copy stays consistent with what was (or wasn't) saved.
func (s *Store) TouchLastSeen(sid string, data *Data) {
	now := time.Now().Unix()
	if now-data.LastSeenAt < int64(touchLastSeenInterval.Seconds()) {
		return
	}
	data.LastSeenAt = now
	_ = s.Save(sid, data)
}

// Session is one row of Settings > Sessions' device list (DESIGN.md §11.4) -
// DisplayID is a truncated SHA-256 of the real sid, never the sid itself:
// the actual session cookie value must stay confined to its own httpOnly
// cookie, since anyone able to read it (an XSS bug, a shared screenshot of
// this settings page) could use it to impersonate that device for the rest
// of the session's 30-day life. DisplayID is safe to render and to accept
// back from a revoke request - Revoke below re-derives it server-side per
// candidate row rather than trusting a client-supplied sid.
type Session struct {
	DisplayID string
	Data      Data
	ExpiresAt int64
}

func displayID(sid string) string {
	sum := sha256.Sum256([]byte(sid))
	return hex.EncodeToString(sum[:])[:16]
}

// DisplayIDFor exposes displayID for handlers that need to recognize which
// of a ListForUser row is the request's own current session (e.g. to label
// it "this device" in Settings > Sessions) - safe to export since the hash
// itself isn't sensitive, only the raw sid it's derived from is.
func DisplayIDFor(sid string) string {
	return displayID(sid)
}

// ListForUser returns userID's active sessions, newest-created first, for
// Settings > Sessions.
func (s *Store) ListForUser(userID int64) ([]Session, error) {
	rows, err := s.db.Query(
		"SELECT sid, sess, expired FROM sessions WHERE user_id = ? AND expired >= ? ORDER BY expired DESC",
		userID, time.Now().UnixMilli(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var sid, sess string
		var expiredMs int64
		if err := rows.Scan(&sid, &sess, &expiredMs); err != nil {
			return nil, err
		}
		var data Data
		if err := json.Unmarshal([]byte(sess), &data); err != nil {
			continue // corrupt/foreign row - skip rather than fail the whole list
		}
		out = append(out, Session{DisplayID: displayID(sid), Data: data, ExpiresAt: expiredMs / 1000})
	}
	return out, rows.Err()
}

// Revoke deletes userID's session identified by id (a DisplayID from
// ListForUser, not a raw sid - see Session's doc comment). Scoped to
// user_id = ? in the same query that finds the row, so one user can never
// revoke another's session even if they guessed/forged a DisplayID.
func (s *Store) Revoke(userID int64, id string) error {
	rows, err := s.db.Query("SELECT sid FROM sessions WHERE user_id = ?", userID)
	if err != nil {
		return err
	}
	var match string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			return err
		}
		if displayID(sid) == id {
			match = sid
			break
		}
	}
	rows.Close()
	if match == "" {
		return ErrNotFound
	}
	return s.Destroy(match)
}

func newSessionID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// SetCookie writes a fresh session cookie for sid onto the response.
func SetCookie(w http.ResponseWriter, sid string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    sid,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie expires the session cookie immediately (logout).
func ClearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// NewSessionID generates a fresh random session id (exported for handlers
// that need to regenerate a session on login, e.g. to prevent session
// fixation).
func NewSessionID() (string, error) {
	return newSessionID()
}
