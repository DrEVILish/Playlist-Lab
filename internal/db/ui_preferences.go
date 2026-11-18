package db

import (
	"database/sql"
	"encoding/json"
)

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

func SaveTextScale(sqlDB *sql.DB, userID int64, scale string) error {
	if err := ensureUserSettingsRow(sqlDB, userID); err != nil {
		return err
	}
	_, err := sqlDB.Exec("UPDATE user_settings SET text_scale = ? WHERE user_id = ?", scale, userID)
	return err
}

// GetTheme returns the user's saved Settings > Appearance theme choice - an
// ftl-themes slug (static/themes/themes.json, e.g. "blue-future"), or "" for
// the app's default theme (no user_settings row / no value saved yet, same
// as GetTextScale's "no value" case).
func GetTheme(sqlDB *sql.DB, userID int64) (string, error) {
	var theme sql.NullString
	err := sqlDB.QueryRow("SELECT theme FROM user_settings WHERE user_id = ?", userID).Scan(&theme)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return theme.String, nil
}

func SaveTheme(sqlDB *sql.DB, userID int64, theme string) error {
	if err := ensureUserSettingsRow(sqlDB, userID); err != nil {
		return err
	}
	_, err := sqlDB.Exec("UPDATE user_settings SET theme = ? WHERE user_id = ?", theme, userID)
	return err
}

// GetThemeVariant returns the saved palette variant id of the user's theme
// (ftl-themes' data-variant), or "" for the theme's base palette.
func GetThemeVariant(sqlDB *sql.DB, userID int64) (string, error) {
	var v sql.NullString
	err := sqlDB.QueryRow("SELECT theme_variant FROM user_settings WHERE user_id = ?", userID).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v.String, err
}

func SaveThemeVariant(sqlDB *sql.DB, userID int64, variant string) error {
	if err := ensureUserSettingsRow(sqlDB, userID); err != nil {
		return err
	}
	_, err := sqlDB.Exec("UPDATE user_settings SET theme_variant = ? WHERE user_id = ?", variant, userID)
	return err
}

// GetThemeTints returns the user's tint colours keyed by theme slug - stored
// per theme (ftl-themes CONTRACT.md "Theme tint": persist the colour against
// the theme slug), so switching away and back keeps each theme's colour.
func GetThemeTints(sqlDB *sql.DB, userID int64) (map[string]string, error) {
	var raw sql.NullString
	err := sqlDB.QueryRow("SELECT theme_tints FROM user_settings WHERE user_id = ?", userID).Scan(&raw)
	tints := map[string]string{}
	if err == sql.ErrNoRows || raw.String == "" {
		return tints, nil
	}
	if err != nil {
		return tints, err
	}
	return tints, json.Unmarshal([]byte(raw.String), &tints)
}

// SaveThemeTint sets (or, for color "", clears) one theme's tint colour.
func SaveThemeTint(sqlDB *sql.DB, userID int64, slug, color string) error {
	tints, err := GetThemeTints(sqlDB, userID)
	if err != nil {
		return err
	}
	if color == "" {
		delete(tints, slug)
	} else {
		tints[slug] = color
	}
	raw, err := json.Marshal(tints)
	if err != nil {
		return err
	}
	if err := ensureUserSettingsRow(sqlDB, userID); err != nil {
		return err
	}
	_, err = sqlDB.Exec("UPDATE user_settings SET theme_tints = ? WHERE user_id = ?", string(raw), userID)
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
