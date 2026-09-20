package db

import "testing"

func TestMixTemplateCRUD(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")

	desc := "desc"
	tmpl, err := CreateMixTemplate(sqlDB, u.ID, "My Mix", &desc, "genre", `{"foo":"bar"}`)
	if err != nil {
		t.Fatalf("CreateMixTemplate: %v", err)
	}
	if tmpl.Name != "My Mix" || tmpl.UseCount != 0 {
		t.Fatalf("unexpected template: %+v", tmpl)
	}

	newName := "Renamed Mix"
	if err := UpdateMixTemplate(sqlDB, tmpl.ID, MixTemplateUpdate{Name: &newName}); err != nil {
		t.Fatalf("UpdateMixTemplate: %v", err)
	}
	got, err := GetMixTemplateByID(sqlDB, tmpl.ID)
	if err != nil || got == nil {
		t.Fatalf("GetMixTemplateByID: %v got=%v", err, got)
	}
	if got.Name != "Renamed Mix" {
		t.Fatalf("expected renamed template, got %s", got.Name)
	}

	if err := UpdateMixTemplateUsage(sqlDB, tmpl.ID); err != nil {
		t.Fatalf("UpdateMixTemplateUsage: %v", err)
	}
	got, _ = GetMixTemplateByID(sqlDB, tmpl.ID)
	if got.UseCount != 1 || !got.LastUsedAt.Valid {
		t.Fatalf("expected use_count incremented and last_used_at set, got %+v", got)
	}

	if err := DeleteMixTemplate(sqlDB, tmpl.ID); err != nil {
		t.Fatalf("DeleteMixTemplate: %v", err)
	}
	got, err = GetMixTemplateByID(sqlDB, tmpl.ID)
	if err != nil {
		t.Fatalf("GetMixTemplateByID after delete: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil template after delete")
	}
}

func TestGetMixTemplatesIsolation(t *testing.T) {
	sqlDB := newTestDB(t)
	u1, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	u2, _ := CreateUser(sqlDB, "plex2", "u2", "tok2", "")

	if _, err := CreateMixTemplate(sqlDB, u1.ID, "Mix 1", nil, "genre", "{}"); err != nil {
		t.Fatalf("CreateMixTemplate: %v", err)
	}

	u1Templates, err := GetMixTemplates(sqlDB, u1.ID)
	if err != nil || len(u1Templates) != 1 {
		t.Fatalf("expected 1 template for user1, got %v err=%v", u1Templates, err)
	}
	u2Templates, err := GetMixTemplates(sqlDB, u2.ID)
	if err != nil || len(u2Templates) != 0 {
		t.Fatalf("expected 0 templates leaked to user2, got %v err=%v", u2Templates, err)
	}
}
