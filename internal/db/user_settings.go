package db

import "database/sql"

// ensureUserSettingsRow makes sure a user_settings row exists before an
// UPDATE targets it - GetMatchingSettingsJSON/GetMixSettingsJSON already
// tolerate a missing row (empty string), but writes need somewhere to land.
func ensureUserSettingsRow(sqlDB *sql.DB, userID int64) error {
	_, err := sqlDB.Exec("INSERT OR IGNORE INTO user_settings (user_id) VALUES (?)", userID)
	return err
}

func SaveMatchingSettingsJSON(sqlDB *sql.DB, userID int64, raw string) error {
	if err := ensureUserSettingsRow(sqlDB, userID); err != nil {
		return err
	}
	_, err := sqlDB.Exec("UPDATE user_settings SET matching_settings = ? WHERE user_id = ?", raw, userID)
	return err
}

func SaveMixSettingsJSON(sqlDB *sql.DB, userID int64, raw string) error {
	if err := ensureUserSettingsRow(sqlDB, userID); err != nil {
		return err
	}
	_, err := sqlDB.Exec("UPDATE user_settings SET mix_settings = ? WHERE user_id = ?", raw, userID)
	return err
}

// AISettings mirrors the ai_provider/gemini_api_key/grok_api_key columns of
// user_settings - the per-user AI configuration surfaced on Settings > AI.
type AISettings struct {
	GeminiAPIKey string
	GrokAPIKey   string
	Provider     string
}

func GetAISettings(sqlDB *sql.DB, userID int64) (AISettings, error) {
	var s AISettings
	var gemini, grok, provider sql.NullString
	err := sqlDB.QueryRow(
		"SELECT gemini_api_key, grok_api_key, ai_provider FROM user_settings WHERE user_id = ?", userID,
	).Scan(&gemini, &grok, &provider)
	if err == sql.ErrNoRows {
		return AISettings{Provider: "gemini"}, nil
	}
	if err != nil {
		return s, err
	}
	s.GeminiAPIKey = gemini.String
	s.GrokAPIKey = grok.String
	s.Provider = provider.String
	if s.Provider == "" {
		s.Provider = "gemini"
	}
	return s, nil
}

func SaveGeminiAPIKey(sqlDB *sql.DB, userID int64, key string) error {
	if err := ensureUserSettingsRow(sqlDB, userID); err != nil {
		return err
	}
	_, err := sqlDB.Exec("UPDATE user_settings SET gemini_api_key = ? WHERE user_id = ?", nullIfEmpty(key), userID)
	return err
}

func SaveGrokAPIKey(sqlDB *sql.DB, userID int64, key string) error {
	if err := ensureUserSettingsRow(sqlDB, userID); err != nil {
		return err
	}
	_, err := sqlDB.Exec("UPDATE user_settings SET grok_api_key = ? WHERE user_id = ?", nullIfEmpty(key), userID)
	return err
}

func SaveAIProvider(sqlDB *sql.DB, userID int64, provider string) error {
	if err := ensureUserSettingsRow(sqlDB, userID); err != nil {
		return err
	}
	_, err := sqlDB.Exec("UPDATE user_settings SET ai_provider = ? WHERE user_id = ?", provider, userID)
	return err
}
