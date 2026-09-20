package deemix

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGWCall_PersistsSessionCookieAcrossCalls pins the bug a live test just
// caught: gw-light.php requires both the arl cookie AND whatever session
// cookie (sid) Deezer's server sets back on the first response - without a
// cookiejar, every call after login got rejected with "Invalid CSRF token"
// even though the token itself was correct, because the session cookie
// never made it onto the second request.
func TestGWCall_PersistsSessionCookieAcrossCalls(t *testing.T) {
	var sawSessionCookieOnSecondCall bool
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		arl, _ := r.Cookie("arl")
		if arl == nil || arl.Value != "test-arl" {
			t.Errorf("call %d: missing/wrong arl cookie", calls)
		}
		if calls == 1 {
			// login: hand back a session cookie the client must carry
			// forward on every later call.
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc123"})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{},
				"results": map[string]any{
					"checkForm": "token-1",
					"USER":      map[string]any{"USER_ID": "1", "OPTIONS": map[string]any{}},
				},
			})
			return
		}
		if sid, _ := r.Cookie("sid"); sid != nil && sid.Value == "abc123" {
			sawSessionCookieOnSecondCall = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":   map[string]any{},
			"results": map[string]any{"SNG_ID": "42"},
		})
	}))
	defer srv.Close()

	c := newDZClient("test-arl")
	c.gwLightURL = srv.URL // test seam - see dzclient.go
	if err := c.login(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := c.gwCall("song.getData", map[string]any{"SNG_ID": "42"}); err != nil {
		t.Fatalf("gwCall: %v", err)
	}
	if !sawSessionCookieOnSecondCall {
		t.Fatal("session cookie set on login's response never made it onto the next gw call")
	}
}
