// dynamic_collections.go stores Dynamic Collection set definitions
// (Kometa's "dynamic_collections" feature - see
// https://kometa.wiki/en/latest/files/dynamic/): one definition expands
// into many real `collections` rows, one per distinct value of a library
// facet. See internal/services/scheduler/dynamic_collections.go for the
// generation logic that realizes a set into rows.
package db

import (
	"database/sql"
	"encoding/json"
	"time"
)

// DynamicCollection mirrors the dynamic_collections table.
type DynamicCollection struct {
	ID               int64
	UserID           int64
	ServerID         int64
	LibrarySectionID string
	LibraryType      string
	Name             string
	FacetType        string         // "genre" | "decade" | "year" | "content_rating" | "studio" | "actor" | "mood" | "style"
	TitleFormat      string         // contains the literal placeholder "<<key_name>>"
	IncludeKeys      sql.NullString // JSON []string
	ExcludeKeys      sql.NullString // JSON []string
	SyncMode         string         // "sync" | "add_only"
	CreatedAt        int64
	UpdatedAt        int64
}

func (d DynamicCollection) ParsedInclude() []string { return parseStringList(d.IncludeKeys) }
func (d DynamicCollection) ParsedExclude() []string { return parseStringList(d.ExcludeKeys) }

func parseStringList(v sql.NullString) []string {
	var out []string
	if v.Valid {
		_ = json.Unmarshal([]byte(v.String), &out)
	}
	return out
}

const dynamicCollectionCols = "id, user_id, server_id, library_section_id, library_type, name, facet_type, title_format, include_keys, exclude_keys, sync_mode, created_at, updated_at"

func scanDynamicCollection(row interface{ Scan(...any) error }) (*DynamicCollection, error) {
	var d DynamicCollection
	err := row.Scan(&d.ID, &d.UserID, &d.ServerID, &d.LibrarySectionID, &d.LibraryType, &d.Name, &d.FacetType, &d.TitleFormat, &d.IncludeKeys, &d.ExcludeKeys, &d.SyncMode, &d.CreatedAt, &d.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func CreateDynamicCollection(sqlDB *sql.DB, userID, serverID int64, librarySectionID, libraryType, name, facetType, titleFormat, includeJSON, excludeJSON, syncMode string) (*DynamicCollection, error) {
	now := time.Now().Unix()
	res, err := sqlDB.Exec(
		`INSERT INTO dynamic_collections (user_id, server_id, library_section_id, library_type, name, facet_type, title_format, include_keys, exclude_keys, sync_mode, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, serverID, librarySectionID, libraryType, name, facetType, titleFormat, nullIfEmpty(includeJSON), nullIfEmpty(excludeJSON), syncMode, now, now,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetDynamicCollectionByID(sqlDB, id)
}

func GetDynamicCollectionByID(sqlDB *sql.DB, id int64) (*DynamicCollection, error) {
	return scanDynamicCollection(sqlDB.QueryRow("SELECT "+dynamicCollectionCols+" FROM dynamic_collections WHERE id = ?", id))
}

func GetUserDynamicCollections(sqlDB *sql.DB, userID int64) ([]DynamicCollection, error) {
	rows, err := sqlDB.Query("SELECT "+dynamicCollectionCols+" FROM dynamic_collections WHERE user_id = ? ORDER BY created_at DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DynamicCollection
	for rows.Next() {
		d, err := scanDynamicCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// DynamicCollectionUpdate is a partial update - nil fields are left
// untouched, mirroring CollectionUpdate's convention. FacetType/library are
// immutable after creation (same reasoning as Collection.BuilderType):
// changing what a set generates from out from under its already-generated
// rows would leave them orphaned rather than cleanly regenerated.
type DynamicCollectionUpdate struct {
	Name        *string
	TitleFormat *string
	IncludeKeys *string // raw JSON, "" clears it
	ExcludeKeys *string
	SyncMode    *string
}

func UpdateDynamicCollection(sqlDB *sql.DB, id int64, u DynamicCollectionUpdate) error {
	fields := []string{}
	values := []any{}
	if u.Name != nil {
		fields = append(fields, "name = ?")
		values = append(values, *u.Name)
	}
	if u.TitleFormat != nil {
		fields = append(fields, "title_format = ?")
		values = append(values, *u.TitleFormat)
	}
	if u.IncludeKeys != nil {
		fields = append(fields, "include_keys = ?")
		values = append(values, nullIfEmpty(*u.IncludeKeys))
	}
	if u.ExcludeKeys != nil {
		fields = append(fields, "exclude_keys = ?")
		values = append(values, nullIfEmpty(*u.ExcludeKeys))
	}
	if u.SyncMode != nil {
		fields = append(fields, "sync_mode = ?")
		values = append(values, *u.SyncMode)
	}
	if len(fields) == 0 {
		return nil
	}
	fields = append(fields, "updated_at = ?")
	values = append(values, time.Now().Unix())
	values = append(values, id)
	q := "UPDATE dynamic_collections SET "
	for i, f := range fields {
		if i > 0 {
			q += ", "
		}
		q += f
	}
	q += " WHERE id = ?"
	_, err := sqlDB.Exec(q, values...)
	return err
}

func DeleteDynamicCollection(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("DELETE FROM dynamic_collections WHERE id = ?", id)
	return err
}
