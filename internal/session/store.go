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
	"database/sql"
	"encoding/base64"
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
	_, err = s.db.Exec(
		`INSERT INTO sessions (sid, sess, expired) VALUES (?, ?, ?)
		 ON CONFLICT(sid) DO UPDATE SET sess = excluded.sess, expired = excluded.expired`,
		sid, string(payload), expired,
	)
	return err
}

func (s *Store) Destroy(sid string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE sid = ?", sid)
	return err
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
