package db

import "database/sql"

// GetMixSettingsJSON returns the raw JSON stored in user_settings.mix_settings
// for a user, or "" if the user has no settings row / no mix_settings value
// yet - the caller (handlers/mixes.go) falls back to hard-coded defaults in
// that case, matching database.ts's getUserSettings().mix_settings ||
// DEFAULT_MIX_SETTINGS pattern.
func GetMixSettingsJSON(sqlDB *sql.DB, userID int64) (string, error) {
	var raw sql.NullString
	err := sqlDB.QueryRow("SELECT mix_settings FROM user_settings WHERE user_id = ?", userID).Scan(&raw)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return raw.String, nil
}
