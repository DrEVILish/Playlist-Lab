// mix_templates.go ports routes/mix-templates.ts's db.getMixTemplates /
// getMixTemplateById / createMixTemplate / updateMixTemplate /
// deleteMixTemplate / updateMixTemplateUsage - a plain CRUD table, no
// hand-rolled cache needed (unlike playlists, these aren't hit on every
// request).
package db

import (
	"database/sql"
	"time"
)

type MixTemplate struct {
	ID            int64
	UserID        int64
	Name          string
	Description   sql.NullString
	MixType       string
	Configuration string // raw JSON, decoded by the handler
	CreatedAt     int64
	UpdatedAt     int64
	LastUsedAt    sql.NullInt64
	UseCount      int
}

const mixTemplateColumns = "id, user_id, name, description, mix_type, configuration, created_at, updated_at, last_used_at, use_count"

func scanMixTemplate(row *sql.Row) (*MixTemplate, error) {
	var t MixTemplate
	err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Description, &t.MixType, &t.Configuration, &t.CreatedAt, &t.UpdatedAt, &t.LastUsedAt, &t.UseCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func GetMixTemplates(sqlDB *sql.DB, userID int64) ([]MixTemplate, error) {
	rows, err := sqlDB.Query(
		"SELECT "+mixTemplateColumns+" FROM mix_templates WHERE user_id = ? ORDER BY last_used_at DESC, updated_at DESC", userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MixTemplate
	for rows.Next() {
		var t MixTemplate
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Description, &t.MixType, &t.Configuration, &t.CreatedAt, &t.UpdatedAt, &t.LastUsedAt, &t.UseCount); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func GetMixTemplateByID(sqlDB *sql.DB, id int64) (*MixTemplate, error) {
	return scanMixTemplate(sqlDB.QueryRow("SELECT "+mixTemplateColumns+" FROM mix_templates WHERE id = ?", id))
}

func CreateMixTemplate(sqlDB *sql.DB, userID int64, name string, description *string, mixType, configuration string) (*MixTemplate, error) {
	now := time.Now().Unix()
	res, err := sqlDB.Exec(
		`INSERT INTO mix_templates (user_id, name, description, mix_type, configuration, created_at, updated_at, use_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 0)`,
		userID, name, description, mixType, configuration, now, now,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetMixTemplateByID(sqlDB, id)
}

// MixTemplateUpdate holds optional field updates; a nil pointer means "leave
// unchanged", matching the TS route's undefined-skips-the-field semantics.
type MixTemplateUpdate struct {
	Name          *string
	Description   *string // a non-nil pointer to "" clears the description
	Configuration *string
}

func UpdateMixTemplate(sqlDB *sql.DB, id int64, update MixTemplateUpdate) error {
	if update.Name != nil {
		if _, err := sqlDB.Exec("UPDATE mix_templates SET name = ?, updated_at = ? WHERE id = ?", *update.Name, time.Now().Unix(), id); err != nil {
			return err
		}
	}
	if update.Description != nil {
		if _, err := sqlDB.Exec("UPDATE mix_templates SET description = ?, updated_at = ? WHERE id = ?", nullIfEmpty(*update.Description), time.Now().Unix(), id); err != nil {
			return err
		}
	}
	if update.Configuration != nil {
		if _, err := sqlDB.Exec("UPDATE mix_templates SET configuration = ?, updated_at = ? WHERE id = ?", *update.Configuration, time.Now().Unix(), id); err != nil {
			return err
		}
	}
	return nil
}

func DeleteMixTemplate(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("DELETE FROM mix_templates WHERE id = ?", id)
	return err
}

func UpdateMixTemplateUsage(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("UPDATE mix_templates SET last_used_at = ?, use_count = use_count + 1 WHERE id = ?", time.Now().Unix(), id)
	return err
}
