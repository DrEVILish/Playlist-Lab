package db

import "database/sql"

type UserServer struct {
	ID             int64
	UserID         int64
	ServerName     string
	ServerClientID string
	ServerURL      string
	LibraryID      sql.NullString
	LibraryName    sql.NullString
	AccessToken    sql.NullString
}

// SaveUserServer replaces a user's server configuration atomically - if the
// insert fails after the delete, the transaction rolls back rather than
// leaving the user with zero server rows. Ports database.ts's
// saveUserServer.
func SaveUserServer(sqlDB *sql.DB, userID int64, serverName, serverClientID, serverURL, libraryID, libraryName, accessToken string) (*UserServer, error) {
	tx, err := sqlDB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM user_servers WHERE user_id = ?", userID); err != nil {
		return nil, err
	}
	res, err := tx.Exec(
		`INSERT INTO user_servers (user_id, server_name, server_client_id, server_url, library_id, library_name, access_token)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, serverName, serverClientID, serverURL, nullIfEmpty(libraryID), nullIfEmpty(libraryName), nullIfEmpty(accessToken),
	)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetUserServerByID(sqlDB, id)
}

func GetUserServerByID(sqlDB *sql.DB, id int64) (*UserServer, error) {
	return scanUserServer(sqlDB.QueryRow(
		"SELECT id, user_id, server_name, server_client_id, server_url, library_id, library_name, access_token FROM user_servers WHERE id = ?", id,
	))
}

func GetUserServer(sqlDB *sql.DB, userID int64) (*UserServer, error) {
	return scanUserServer(sqlDB.QueryRow(
		"SELECT id, user_id, server_name, server_client_id, server_url, library_id, library_name, access_token FROM user_servers WHERE user_id = ?", userID,
	))
}

func scanUserServer(row *sql.Row) (*UserServer, error) {
	var s UserServer
	err := row.Scan(&s.ID, &s.UserID, &s.ServerName, &s.ServerClientID, &s.ServerURL, &s.LibraryID, &s.LibraryName, &s.AccessToken)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// CopyServerConfig gives toUserID the same Plex server/library as
// fromUserID, if fromUserID has one - ports database.ts's copyServerConfig,
// used both at login-time auto-approval and admin's manual "Enable" so a
// newly-approved user doesn't have to run the server/library picker
// themselves. access_token is deliberately not copied: it's the source
// user's own Plex auth, not something the new user should inherit.
func CopyServerConfig(sqlDB *sql.DB, fromUserID, toUserID int64) error {
	src, err := GetUserServer(sqlDB, fromUserID)
	if err != nil || src == nil {
		return err
	}
	_, err = SaveUserServer(sqlDB, toUserID, src.ServerName, src.ServerClientID, src.ServerURL, src.LibraryID.String, src.LibraryName.String, "")
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
