package db

import "testing"

func TestCollectionCRUD(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	srv, err := AddUserServer(sqlDB, u.ID, "My Server", "client1", "http://localhost:32400", "1", "Movies", "", true)
	if err != nil {
		t.Fatalf("SaveUserServer: %v", err)
	}

	rulesJSON := `[{"field":"genre","operator":"is","value":"Action"}]`
	coll, err := CreateCollection(sqlDB, u.ID, srv.ID, "1", "movie", "Action Movies", "", "smart", rulesJSON, "", "sync")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if coll.BuilderType != "smart" || coll.LibraryType != "movie" || len(coll.ParsedRules()) != 1 {
		t.Fatalf("unexpected collection: %+v rules=%+v", coll, coll.ParsedRules())
	}
	if coll.ParsedRules()[0].Field != "genre" {
		t.Fatalf("unexpected rule: %+v", coll.ParsedRules()[0])
	}

	newName := "Action & Adventure"
	if err := UpdateCollection(sqlDB, coll.ID, CollectionUpdate{Name: &newName}); err != nil {
		t.Fatalf("UpdateCollection: %v", err)
	}
	got, err := GetCollectionByID(sqlDB, coll.ID)
	if err != nil || got == nil || got.Name != newName {
		t.Fatalf("GetCollectionByID after update: %v got=%+v", err, got)
	}

	if err := UpdateCollectionPlexID(sqlDB, coll.ID, "rk-collection-1"); err != nil {
		t.Fatalf("UpdateCollectionPlexID: %v", err)
	}
	got, _ = GetCollectionByID(sqlDB, coll.ID)
	if !got.PlexCollectionID.Valid || got.PlexCollectionID.String != "rk-collection-1" {
		t.Fatalf("expected plex_collection_id to be set, got %+v", got)
	}

	all, err := GetUserCollections(sqlDB, u.ID)
	if err != nil || len(all) != 1 {
		t.Fatalf("GetUserCollections: %v (all=%v)", err, all)
	}

	if err := DeleteCollection(sqlDB, coll.ID); err != nil {
		t.Fatalf("DeleteCollection: %v", err)
	}
	got, err = GetCollectionByID(sqlDB, coll.ID)
	if err != nil {
		t.Fatalf("GetCollectionByID after delete: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil collection after delete")
	}
}
