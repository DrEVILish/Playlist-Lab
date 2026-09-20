package db

import "testing"

func TestUserSettingsCRUD(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")

	if err := SaveMatchingSettingsJSON(sqlDB, u.ID, `{"minMatchScore":0.9}`); err != nil {
		t.Fatalf("SaveMatchingSettingsJSON: %v", err)
	}
	var raw string
	if err := sqlDB.QueryRow("SELECT matching_settings FROM user_settings WHERE user_id = ?", u.ID).Scan(&raw); err != nil {
		t.Fatalf("select matching_settings: %v", err)
	}
	if raw != `{"minMatchScore":0.9}` {
		t.Fatalf("unexpected matching_settings: %s", raw)
	}

	// Overwrite should update, not duplicate the row (user_id is PRIMARY KEY).
	if err := SaveMatchingSettingsJSON(sqlDB, u.ID, `{"minMatchScore":0.5}`); err != nil {
		t.Fatalf("SaveMatchingSettingsJSON overwrite: %v", err)
	}
	var count int
	sqlDB.QueryRow("SELECT COUNT(*) FROM user_settings WHERE user_id = ?", u.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 user_settings row, got %d", count)
	}

	if err := SaveMixSettingsJSON(sqlDB, u.ID, `{"foo":"bar"}`); err != nil {
		t.Fatalf("SaveMixSettingsJSON: %v", err)
	}
}

func TestAISettingsDefaultsAndCRUD(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")

	// No user_settings row yet -> defaults.
	settings, err := GetAISettings(sqlDB, u.ID)
	if err != nil {
		t.Fatalf("GetAISettings: %v", err)
	}
	if settings.Provider != "gemini" {
		t.Fatalf("expected default provider 'gemini', got %q", settings.Provider)
	}

	if err := SaveGeminiAPIKey(sqlDB, u.ID, "gemini-key"); err != nil {
		t.Fatalf("SaveGeminiAPIKey: %v", err)
	}
	if err := SaveGrokAPIKey(sqlDB, u.ID, "grok-key"); err != nil {
		t.Fatalf("SaveGrokAPIKey: %v", err)
	}
	if err := SaveAIProvider(sqlDB, u.ID, "grok"); err != nil {
		t.Fatalf("SaveAIProvider: %v", err)
	}

	settings, err = GetAISettings(sqlDB, u.ID)
	if err != nil {
		t.Fatalf("GetAISettings: %v", err)
	}
	if settings.GeminiAPIKey != "gemini-key" || settings.GrokAPIKey != "grok-key" || settings.Provider != "grok" {
		t.Fatalf("unexpected AI settings: %+v", settings)
	}
}

// TestUserSettingsIsolation matches data-isolation.property.test.ts's
// "Property 5: Independent User Settings".
func TestUserSettingsIsolation(t *testing.T) {
	sqlDB := newTestDB(t)
	u1, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	u2, _ := CreateUser(sqlDB, "plex2", "u2", "tok2", "")

	if err := SaveMatchingSettingsJSON(sqlDB, u1.ID, `{"minMatchScore":0.9}`); err != nil {
		t.Fatalf("SaveMatchingSettingsJSON u1: %v", err)
	}
	if err := SaveMatchingSettingsJSON(sqlDB, u2.ID, `{"minMatchScore":0.1}`); err != nil {
		t.Fatalf("SaveMatchingSettingsJSON u2: %v", err)
	}

	var raw1, raw2 string
	sqlDB.QueryRow("SELECT matching_settings FROM user_settings WHERE user_id = ?", u1.ID).Scan(&raw1)
	sqlDB.QueryRow("SELECT matching_settings FROM user_settings WHERE user_id = ?", u2.ID).Scan(&raw2)

	if raw1 == raw2 {
		t.Fatal("expected independent matching_settings per user")
	}
	if raw1 != `{"minMatchScore":0.9}` || raw2 != `{"minMatchScore":0.1}` {
		t.Fatalf("settings leaked or overwritten across users: u1=%s u2=%s", raw1, raw2)
	}
}

func TestAdminConfigCRUD(t *testing.T) {
	sqlDB := newTestDB(t)

	if _, ok, err := GetAdminConfig(sqlDB, "deemix_arl"); err != nil || ok {
		t.Fatalf("expected unset key to return ok=false, got ok=%v err=%v", ok, err)
	}

	if err := SetAdminConfig(sqlDB, "deemix_arl", "arl-value"); err != nil {
		t.Fatalf("SetAdminConfig: %v", err)
	}
	val, ok, err := GetAdminConfig(sqlDB, "deemix_arl")
	if err != nil || !ok || val != "arl-value" {
		t.Fatalf("GetAdminConfig: val=%s ok=%v err=%v", val, ok, err)
	}

	// Upsert should update in place.
	if err := SetAdminConfig(sqlDB, "deemix_arl", "new-value"); err != nil {
		t.Fatalf("SetAdminConfig (update): %v", err)
	}
	val, _, _ = GetAdminConfig(sqlDB, "deemix_arl")
	if val != "new-value" {
		t.Fatalf("expected updated value, got %s", val)
	}
}

func TestUserServerCRUDMultiple(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")

	if got, err := GetUserServers(sqlDB, u.ID); err != nil || len(got) != 0 {
		t.Fatalf("expected no servers before any add, got %v err=%v", got, err)
	}
	if got, err := GetUserMusicServer(sqlDB, u.ID); err != nil || got != nil {
		t.Fatalf("expected nil music server before any add, got %v err=%v", got, err)
	}

	srv1, err := AddUserServer(sqlDB, u.ID, "My Server", "client123", "http://localhost:32400", "lib1", "Music", "", false)
	if err != nil {
		t.Fatalf("AddUserServer: %v", err)
	}
	if srv1.ServerName != "My Server" || srv1.LibraryID.String != "lib1" || !srv1.IsDefault {
		t.Fatalf("unexpected first server (should auto-default): %+v", srv1)
	}

	// Adding a second server (DESIGN.md §11.11 multi-server support) must
	// accumulate, not replace - a user can link a movies/TV server and a
	// music server at the same time.
	srv2, err := AddUserServer(sqlDB, u.ID, "Server 2", "client2", "http://server2", "", "", "", false)
	if err != nil {
		t.Fatalf("AddUserServer (second): %v", err)
	}
	if srv2.IsDefault {
		t.Fatalf("expected the second server to NOT be default, got %+v", srv2)
	}

	all, err := GetUserServers(sqlDB, u.ID)
	if err != nil || len(all) != 2 {
		t.Fatalf("expected 2 servers, got %v err=%v", all, err)
	}
	if all[0].ID != srv1.ID {
		t.Fatalf("expected the default server to sort first, got %+v", all[0])
	}

	// GetUserMusicServer resolves to the only server with a library
	// configured (srv1 - srv2 has none, e.g. a movies/TV-only server).
	music, err := GetUserMusicServer(sqlDB, u.ID)
	if err != nil || music == nil || music.ID != srv1.ID {
		t.Fatalf("expected GetUserMusicServer to resolve srv1: %v got=%v", err, music)
	}

	// SetDefaultServer promotes srv2.
	if err := SetDefaultServer(sqlDB, u.ID, srv2.ID); err != nil {
		t.Fatalf("SetDefaultServer: %v", err)
	}
	all, _ = GetUserServers(sqlDB, u.ID)
	if all[0].ID != srv2.ID || !all[0].IsDefault {
		t.Fatalf("expected srv2 to be the new default, got %+v", all[0])
	}
	if all[1].IsDefault {
		t.Fatalf("expected srv1 to no longer be default, got %+v", all[1])
	}

	// RemoveUserServer of the current default promotes the remaining one.
	if err := RemoveUserServer(sqlDB, u.ID, srv2.ID); err != nil {
		t.Fatalf("RemoveUserServer: %v", err)
	}
	all, _ = GetUserServers(sqlDB, u.ID)
	if len(all) != 1 || all[0].ID != srv1.ID || !all[0].IsDefault {
		t.Fatalf("expected only srv1 left and promoted to default, got %+v", all)
	}
}

// TestAddUserServerRollbackOnFailedInsert matches database.test.ts's
// "saveUserServer should leave the previous server intact if the insert
// fails" - a failed insert (FK violation on a nonexistent user) must not
// leave partial state or touch an unrelated user's existing row.
func TestAddUserServerRollbackOnFailedInsert(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")

	if _, err := AddUserServer(sqlDB, u.ID, "Server 1", "client1", "http://server1", "", "", "", false); err != nil {
		t.Fatalf("AddUserServer: %v", err)
	}

	nonexistentUser := u.ID + 999
	if _, err := AddUserServer(sqlDB, nonexistentUser, "Server 2", "client2", "http://server2", "", "", "", false); err == nil {
		t.Fatal("expected AddUserServer to fail for nonexistent user_id (FK violation)")
	}

	got, err := GetUserServers(sqlDB, u.ID)
	if err != nil || len(got) != 1 || got[0].ServerName != "Server 1" {
		t.Fatalf("expected original server unchanged: %v got=%v", err, got)
	}
}

func TestCopyServerConfig(t *testing.T) {
	sqlDB := newTestDB(t)
	u1, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	u2, _ := CreateUser(sqlDB, "plex2", "u2", "tok2", "")

	if _, err := AddUserServer(sqlDB, u1.ID, "Shared Server", "client1", "http://server1", "lib1", "Music", "secret-token", false); err != nil {
		t.Fatalf("AddUserServer: %v", err)
	}
	// A second linked server - CopyServerConfig should copy both, not just
	// the default (DESIGN.md §11.11).
	if _, err := AddUserServer(sqlDB, u1.ID, "Movies Server", "client2", "http://server2", "", "", "", false); err != nil {
		t.Fatalf("AddUserServer (second): %v", err)
	}

	if err := CopyServerConfig(sqlDB, u1.ID, u2.ID); err != nil {
		t.Fatalf("CopyServerConfig: %v", err)
	}

	got, err := GetUserServers(sqlDB, u2.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("expected both servers copied to u2: %v got=%v", err, got)
	}
	if got[0].ServerName != "Shared Server" || got[0].LibraryID.String != "lib1" || !got[0].IsDefault {
		t.Fatalf("unexpected copied default server: %+v", got[0])
	}
	// access_token deliberately not copied.
	if got[0].AccessToken.Valid {
		t.Fatalf("expected access_token NOT to be copied, got %v", got[0].AccessToken)
	}
}
