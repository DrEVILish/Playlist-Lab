package db

import "database/sql"

// GetAdminConfig reads one admin-editable server-wide setting (see
// schema.sql's admin_config table), returning ("", false, nil) if unset.
func GetAdminConfig(sqlDB *sql.DB, key string) (string, bool, error) {
	var value string
	err := sqlDB.QueryRow("SELECT value FROM admin_config WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// SetAdminConfig upserts one admin-editable server-wide setting.
func SetAdminConfig(sqlDB *sql.DB, key, value string) error {
	_, err := sqlDB.Exec(
		`INSERT INTO admin_config (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}
