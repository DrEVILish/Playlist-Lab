package db

import "testing"

func TestTextScaleDefaultsAndCRUD(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")

	// No user_settings row yet -> "medium" default.
	scale, err := GetTextScale(sqlDB, u.ID)
	if err != nil {
		t.Fatalf("GetTextScale: %v", err)
	}
	if scale != "medium" {
		t.Fatalf("expected default text scale 'medium', got %q", scale)
	}

	if err := SaveTextScale(sqlDB, u.ID, "large"); err != nil {
		t.Fatalf("SaveTextScale: %v", err)
	}
	scale, err = GetTextScale(sqlDB, u.ID)
	if err != nil {
		t.Fatalf("GetTextScale after save: %v", err)
	}
	if scale != "large" {
		t.Fatalf("expected saved text scale 'large', got %q", scale)
	}

	// Overwrite should update, not duplicate the row (user_id is PRIMARY KEY).
	if err := SaveTextScale(sqlDB, u.ID, "small"); err != nil {
		t.Fatalf("SaveTextScale overwrite: %v", err)
	}
	var count int
	sqlDB.QueryRow("SELECT COUNT(*) FROM user_settings WHERE user_id = ?", u.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 user_settings row, got %d", count)
	}
}

func TestEditorColumnsCRUD(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")

	// No user_settings row yet -> "" (caller falls back to default order).
	raw, err := GetEditorColumnsJSON(sqlDB, u.ID)
	if err != nil {
		t.Fatalf("GetEditorColumnsJSON: %v", err)
	}
	if raw != "" {
		t.Fatalf("expected no saved editor columns, got %q", raw)
	}

	want := `{"order":["artist","title"],"hidden":["codec"]}`
	if err := SaveEditorColumnsJSON(sqlDB, u.ID, want); err != nil {
		t.Fatalf("SaveEditorColumnsJSON: %v", err)
	}
	raw, err = GetEditorColumnsJSON(sqlDB, u.ID)
	if err != nil {
		t.Fatalf("GetEditorColumnsJSON after save: %v", err)
	}
	if raw != want {
		t.Fatalf("unexpected editor_columns: got %q, want %q", raw, want)
	}
}
