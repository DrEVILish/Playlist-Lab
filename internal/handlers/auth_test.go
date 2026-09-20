package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
)

// fakePlexHomeServer stands in for plex.tv's /home/users and /api/users
// endpoints (GetHomeUsers/GetFriends), so handleLogin/reverifyMembership's
// membership-approval branching can be exercised without a real network
// call. failHome makes the home-users request fail outright, matching the
// "Plex Home lookup errored" path.
func fakePlexHomeServer(t *testing.T, homeUserIDs, friendIDs []int, failHome bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/home/users", func(w http.ResponseWriter, r *http.Request) {
		if failHome {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		users := make([]map[string]int, len(homeUserIDs))
		for i, id := range homeUserIDs {
			users[i] = map[string]int{"id": id}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"users": users})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte("<MediaContainer>"))
		for _, id := range friendIDs {
			fmt.Fprintf(w, `<User id="%d"/>`, id)
		}
		w.Write([]byte("</MediaContainer>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	restore := auth.SetPlexAPIBaseForTest(srv.URL, srv.URL+"/")
	t.Cleanup(restore)
	return srv
}

func newAuthHandler(sqlDB *sql.DB) *AuthHandler {
	return &AuthHandler{DB: sqlDB, Plex: auth.NewPlexClient("test-client-id", "Playlist Lab")}
}

// TestHandleLogin_FirstUserIsAutoPromotedAndEnabled covers the bootstrap
// case: the very first account ever created becomes admin and is enabled
// unconditionally, without ever needing (or being able) to check Plex Home
// membership against itself.
func TestHandleLogin_FirstUserIsAutoPromotedAndEnabled(t *testing.T) {
	sqlDB := newTestDB(t)
	h := newAuthHandler(sqlDB)

	user, enabled, err := h.handleLogin(&auth.PlexUser{ID: 111, Username: "owner"}, "tok-owner")
	if err != nil {
		t.Fatalf("handleLogin: %v", err)
	}
	if !enabled {
		t.Fatal("first user must be enabled")
	}
	if isAdmin, _ := db.IsAdmin(sqlDB, user.ID); !isAdmin {
		t.Error("first user must be auto-promoted to admin")
	}
}

// TestHandleLogin_SecondUserApprovedViaPlexHome covers the common
// non-admin path: a second account that shows up in the admin's Plex Home
// list gets enabled and inherits the admin's server config.
func TestHandleLogin_SecondUserApprovedViaPlexHome(t *testing.T) {
	sqlDB := newTestDB(t)
	h := newAuthHandler(sqlDB)

	admin, _, err := h.handleLogin(&auth.PlexUser{ID: 1, Username: "admin"}, "tok-admin")
	if err != nil {
		t.Fatalf("handleLogin(admin): %v", err)
	}
	if _, err := db.AddUserServer(sqlDB, admin.ID, "Admin Server", "client-1", "http://plex.example", "1", "Music", "srv-token", false); err != nil {
		t.Fatalf("SaveUserServer: %v", err)
	}

	fakePlexHomeServer(t, []int{222}, nil, false)

	user, enabled, err := h.handleLogin(&auth.PlexUser{ID: 222, Username: "member"}, "tok-member")
	if err != nil {
		t.Fatalf("handleLogin(member): %v", err)
	}
	if !enabled {
		t.Error("a user present in the admin's Plex Home must be enabled")
	}
	servers, err := db.GetUserServers(sqlDB, user.ID)
	if err != nil || len(servers) != 1 {
		t.Fatal("expected the admin's server config to be copied to the newly approved user")
	}
	if servers[0].ServerURL != "http://plex.example" {
		t.Errorf("copied server URL = %q, want the admin's http://plex.example", servers[0].ServerURL)
	}
}

// TestHandleLogin_NewUserNotInHomeStaysDisabled covers the default-deny
// case: someone who isn't in the admin's Plex Home or friends list must not
// be auto-enabled just by successfully logging into Plex.
func TestHandleLogin_NewUserNotInHomeStaysDisabled(t *testing.T) {
	sqlDB := newTestDB(t)
	h := newAuthHandler(sqlDB)

	if _, _, err := h.handleLogin(&auth.PlexUser{ID: 1, Username: "admin"}, "tok-admin"); err != nil {
		t.Fatalf("handleLogin(admin): %v", err)
	}
	fakePlexHomeServer(t, []int{999}, nil, false) // 333 is in neither list below

	_, enabled, err := h.handleLogin(&auth.PlexUser{ID: 333, Username: "stranger"}, "tok-stranger")
	if err != nil {
		t.Fatalf("handleLogin(stranger): %v", err)
	}
	if enabled {
		t.Error("a user absent from Plex Home and friends must stay disabled")
	}
}

// TestHandleLogin_PlexHomeLookupFailsForNewUserStaysDisabled covers the
// error-swallowing contract for a brand-new account: a failed membership
// lookup must not crash login, but a never-verified new account is left
// disabled rather than defaulting to enabled.
func TestHandleLogin_PlexHomeLookupFailsForNewUserStaysDisabled(t *testing.T) {
	sqlDB := newTestDB(t)
	h := newAuthHandler(sqlDB)

	if _, _, err := h.handleLogin(&auth.PlexUser{ID: 1, Username: "admin"}, "tok-admin"); err != nil {
		t.Fatalf("handleLogin(admin): %v", err)
	}
	fakePlexHomeServer(t, nil, nil, true) // GetHomeUsers errors

	_, enabled, err := h.handleLogin(&auth.PlexUser{ID: 444, Username: "newbie"}, "tok-newbie")
	if err != nil {
		t.Fatalf("handleLogin(newbie): %v", err)
	}
	if enabled {
		t.Error("a brand-new account must not be enabled when membership can't be verified")
	}
}

// TestHandleLogin_PlexHomeLookupFailsForExistingUserLeavesAccessUntouched
// covers the other half of that same contract: an already-approved,
// already-enabled user must keep their access if a later login's membership
// re-check errors out - a flaky plex.tv call must not lock out an existing
// user.
func TestHandleLogin_PlexHomeLookupFailsForExistingUserLeavesAccessUntouched(t *testing.T) {
	sqlDB := newTestDB(t)
	h := newAuthHandler(sqlDB)

	if _, _, err := h.handleLogin(&auth.PlexUser{ID: 1, Username: "admin"}, "tok-admin"); err != nil {
		t.Fatalf("handleLogin(admin): %v", err)
	}
	fakePlexHomeServer(t, []int{555}, nil, false)
	if _, enabled, err := h.handleLogin(&auth.PlexUser{ID: 555, Username: "member"}, "tok-member"); err != nil || !enabled {
		t.Fatalf("initial approval failed: enabled=%v err=%v", enabled, err)
	}

	fakePlexHomeServer(t, nil, nil, true) // now the lookup starts failing
	_, enabled, err := h.handleLogin(&auth.PlexUser{ID: 555, Username: "member"}, "tok-member-2")
	if err != nil {
		t.Fatalf("handleLogin(member) second time: %v", err)
	}
	if !enabled {
		t.Error("an existing enabled user must stay enabled when a later membership re-check errors")
	}
}

// TestHandleLogin_ExistingUserLosesHomeMembershipGetsDisabled covers
// revocation: a previously approved user removed from the admin's Plex Home
// (and not a friend either) must be disabled on their next login.
func TestHandleLogin_ExistingUserLosesHomeMembershipGetsDisabled(t *testing.T) {
	sqlDB := newTestDB(t)
	h := newAuthHandler(sqlDB)

	if _, _, err := h.handleLogin(&auth.PlexUser{ID: 1, Username: "admin"}, "tok-admin"); err != nil {
		t.Fatalf("handleLogin(admin): %v", err)
	}
	fakePlexHomeServer(t, []int{666}, nil, false)
	if _, enabled, err := h.handleLogin(&auth.PlexUser{ID: 666, Username: "member"}, "tok-member"); err != nil || !enabled {
		t.Fatalf("initial approval failed: enabled=%v err=%v", enabled, err)
	}

	fakePlexHomeServer(t, nil, nil, false) // removed from Home, no friends either
	_, enabled, err := h.handleLogin(&auth.PlexUser{ID: 666, Username: "member"}, "tok-member-2")
	if err != nil {
		t.Fatalf("handleLogin(member) second time: %v", err)
	}
	if enabled {
		t.Error("a user removed from Plex Home/friends must be disabled on next login")
	}
}
