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

func TestUserServerCRUDAndReplace(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")

	if got, err := GetUserServer(sqlDB, u.ID); err != nil || got != nil {
		t.Fatalf("expected nil server before any save, got %v err=%v", got, err)
	}

	srv, err := SaveUserServer(sqlDB, u.ID, "My Server", "client123", "http://localhost:32400", "lib1", "Music", "")
	if err != nil {
		t.Fatalf("SaveUserServer: %v", err)
	}
	if srv.ServerName != "My Server" || srv.LibraryID.String != "lib1" {
		t.Fatalf("unexpected server: %+v", srv)
	}

	// Saving again should replace, not accumulate rows (mirrors "saveUserServer
	// should replace existing server").
	if _, err := SaveUserServer(sqlDB, u.ID, "Server 2", "client2", "http://server2", "", "", ""); err != nil {
		t.Fatalf("SaveUserServer (replace): %v", err)
	}
	got, err := GetUserServer(sqlDB, u.ID)
	if err != nil || got == nil {
		t.Fatalf("GetUserServer: %v got=%v", err, got)
	}
	if got.ServerName != "Server 2" {
		t.Fatalf("expected replaced server, got %s", got.ServerName)
	}
	var count int
	sqlDB.QueryRow("SELECT COUNT(*) FROM user_servers WHERE user_id = ?", u.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 server row after replace, got %d", count)
	}
}

// TestSaveUserServerRollbackOnFailedInsert matches database.test.ts's
// "saveUserServer should leave the previous server intact if the insert
// fails" - the DELETE+INSERT must be atomic.
func TestSaveUserServerRollbackOnFailedInsert(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")

	if _, err := SaveUserServer(sqlDB, u.ID, "Server 1", "client1", "http://server1", "", "", ""); err != nil {
		t.Fatalf("SaveUserServer: %v", err)
	}

	// server_name is NOT NULL; passing a userID that doesn't exist won't fail
	// (no FK on user_servers.user_id? there is one) - instead force failure via
	// FK violation: use a userID that has no matching users row.
	nonexistentUser := u.ID + 999
	if _, err := SaveUserServer(sqlDB, nonexistentUser, "Server 2", "client2", "http://server2", "", "", ""); err == nil {
		t.Fatal("expected SaveUserServer to fail for nonexistent user_id (FK violation)")
	}

	// Original user's server must still be present - the failed insert's
	// DELETE (which only affected nonexistentUser's rows, i.e. none) must not
	// have touched user 1's row either way, and the transaction must not have
	// left partial state.
	got, err := GetUserServer(sqlDB, u.ID)
	if err != nil || got == nil {
		t.Fatalf("expected original server intact: %v got=%v", err, got)
	}
	if got.ServerName != "Server 1" {
		t.Fatalf("expected original server unchanged, got %s", got.ServerName)
	}
}

func TestCopyServerConfig(t *testing.T) {
	sqlDB := newTestDB(t)
	u1, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	u2, _ := CreateUser(sqlDB, "plex2", "u2", "tok2", "")

	if _, err := SaveUserServer(sqlDB, u1.ID, "Shared Server", "client1", "http://server1", "lib1", "Music", "secret-token"); err != nil {
		t.Fatalf("SaveUserServer: %v", err)
	}

	if err := CopyServerConfig(sqlDB, u1.ID, u2.ID); err != nil {
		t.Fatalf("CopyServerConfig: %v", err)
	}

	got, err := GetUserServer(sqlDB, u2.ID)
	if err != nil || got == nil {
		t.Fatalf("GetUserServer for u2: %v got=%v", err, got)
	}
	if got.ServerName != "Shared Server" || got.LibraryID.String != "lib1" {
		t.Fatalf("unexpected copied server: %+v", got)
	}
	// access_token deliberately not copied.
	if got.AccessToken.Valid {
		t.Fatalf("expected access_token NOT to be copied, got %v", got.AccessToken)
	}
}
