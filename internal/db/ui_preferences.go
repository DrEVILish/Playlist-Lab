package db

import "database/sql"

// GetTextScale returns the user's saved Settings > Appearance text-size
// choice ("small"/"medium"/"large"), or "medium" if they have no
// user_settings row / no value saved yet (DESIGN.md §14).
func GetTextScale(sqlDB *sql.DB, userID int64) (string, error) {
	var scale sql.NullString
	err := sqlDB.QueryRow("SELECT text_scale FROM user_settings WHERE user_id = ?", userID).Scan(&scale)
	if err == sql.ErrNoRows || scale.String == "" {
		return "medium", nil
	}
	if err != nil {
		return "medium", err
	}
	return scale.String, nil
}

// GetTheme returns the user's saved Settings > Appearance theme slug, or
// "none" (this app's own look, no theme stylesheet) if they have no
// user_settings row / no value saved yet. "none" is the default so an
// existing install renders exactly as it did before themes existed.
func GetTheme(sqlDB *sql.DB, userID int64) (string, error) {
	var slug sql.NullString
	err := sqlDB.QueryRow("SELECT theme FROM user_settings WHERE user_id = ?", userID).Scan(&slug)
	if err == sql.ErrNoRows || slug.String == "" {
		return "none", nil
	}
	if err != nil {
		return "none", err
	}
	return slug.String, nil
}

func SaveTheme(sqlDB *sql.DB, userID int64, slug string) error {
	if err := ensureUserSettingsRow(sqlDB, userID); err != nil {
		return err
	}
	_, err := sqlDB.Exec("UPDATE user_settings SET theme = ? WHERE user_id = ?", slug, userID)
	return err
}

func SaveTextScale(sqlDB *sql.DB, userID int64, scale string) error {
	if err := ensureUserSettingsRow(sqlDB, userID); err != nil {
		return err
	}
	_, err := sqlDB.Exec("UPDATE user_settings SET text_scale = ? WHERE user_id = ?", scale, userID)
	return err
}

// GetEditorColumnsJSON returns the raw JSON stored in
// user_settings.editor_columns for a user, or "" if they have no
// user_settings row / no value saved yet - the caller (templates/editor.html)
// falls back to showing every column in its default order in that case,
// same pattern as GetMixSettingsJSON.
func GetEditorColumnsJSON(sqlDB *sql.DB, userID int64) (string, error) {
	var raw sql.NullString
	err := sqlDB.QueryRow("SELECT editor_columns FROM user_settings WHERE user_id = ?", userID).Scan(&raw)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return raw.String, nil
}

func SaveEditorColumnsJSON(sqlDB *sql.DB, userID int64, raw string) error {
	if err := ensureUserSettingsRow(sqlDB, userID); err != nil {
		return err
	}
	_, err := sqlDB.Exec("UPDATE user_settings SET editor_columns = ? WHERE user_id = ?", raw, userID)
	return err
}
