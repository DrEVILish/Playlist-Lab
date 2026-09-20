package db

import "database/sql"

// GetMatchingSettingsJSON returns the raw JSON stored in
// user_settings.matching_settings for a user, or "" if the user has no
// settings row / no value yet - callers fall back to
// matching.DefaultSettings() in that case, mirroring database.ts's
// getUserSettings().matching_settings || DEFAULT_MATCHING_SETTINGS pattern.
// (See GetMixSettingsJSON for the same shape on the mix-settings column.)
func GetMatchingSettingsJSON(sqlDB *sql.DB, userID int64) (string, error) {
	var raw sql.NullString
	err := sqlDB.QueryRow("SELECT matching_settings FROM user_settings WHERE user_id = ?", userID).Scan(&raw)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return raw.String, nil
}
