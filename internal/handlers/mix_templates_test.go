package handlers

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/drevilish/playlist-lab/internal/db"
)

func TestMixTemplateCreate_StampsSchemaVersionWhenMissing(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &MixTemplatesHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, "POST", "/mix-templates", h.create)

	form := url.Values{
		"name":          {"Test Mix"},
		"mixType":       {"mood"},
		"configuration": {`{"trackCount": 50, "moods": ["chill"]}`},
	}
	rec := authedRequest(t, sqlDB, router, user, "POST", "/mix-templates", form.Encode(), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status=%d body=%s", rec.Code, rec.Body.String())
	}

	templates, err := db.GetMixTemplates(sqlDB, user.ID)
	if err != nil || len(templates) != 1 {
		t.Fatalf("GetMixTemplates: %v templates=%v", err, templates)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(templates[0].Configuration), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg["schemaVersion"] != float64(1) {
		t.Errorf("schemaVersion = %v, want 1", cfg["schemaVersion"])
	}
}

func TestMixTemplateCreate_PreservesExistingSchemaVersion(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &MixTemplatesHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, "POST", "/mix-templates", h.create)

	form := url.Values{
		"name":          {"Test Mix"},
		"mixType":       {"mood"},
		"configuration": {`{"schemaVersion": 1, "trackCount": 50, "moods": ["chill"]}`},
	}
	authedRequest(t, sqlDB, router, user, "POST", "/mix-templates", form.Encode(), "application/x-www-form-urlencoded")

	templates, _ := db.GetMixTemplates(sqlDB, user.ID)
	var cfg map[string]any
	json.Unmarshal([]byte(templates[0].Configuration), &cfg)
	if cfg["schemaVersion"] != float64(1) {
		t.Errorf("schemaVersion = %v, want 1", cfg["schemaVersion"])
	}
}

func TestMixTemplateCreate_RejectsMissingTrackCount(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &MixTemplatesHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, "POST", "/mix-templates", h.create)

	form := url.Values{
		"name":          {"Test Mix"},
		"mixType":       {"mood"},
		"configuration": {`{"moods": ["chill"]}`},
	}
	rec := authedRequest(t, sqlDB, router, user, "POST", "/mix-templates", form.Encode(), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestMixTemplateCreate_RejectsInvalidMixType(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &MixTemplatesHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, "POST", "/mix-templates", h.create)

	form := url.Values{
		"name":          {"Test Mix"},
		"mixType":       {"not-a-real-type"},
		"configuration": {`{"trackCount": 50}`},
	}
	rec := authedRequest(t, sqlDB, router, user, "POST", "/mix-templates", form.Encode(), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestMixTemplateCreate_RejectsEmptyName(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &MixTemplatesHandler{DB: sqlDB, Tmpl: nopTemplates()}
	router := testRouter(sqlDB, "POST", "/mix-templates", h.create)

	form := url.Values{
		"name":          {""},
		"mixType":       {"mood"},
		"configuration": {`{"trackCount": 50}`},
	}
	rec := authedRequest(t, sqlDB, router, user, "POST", "/mix-templates", form.Encode(), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestMixTemplateUpdate_GranularFieldsMergePreservesOtherFields automates
// the create -> edit-granular-fields -> read-back invariant that was
// previously only checked by hand with curl: EditTemplateModal.tsx's form
// only exposes a handful of scalar fields (trackCount, sortBy, ...), so
// editing one of them must merge onto the existing configuration rather
// than replacing it outright - fields the form doesn't expose (artistIds)
// must survive untouched.
func TestMixTemplateUpdate_GranularFieldsMergePreservesOtherFields(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &MixTemplatesHandler{DB: sqlDB, Tmpl: nopTemplates()}

	tpl, err := db.CreateMixTemplate(sqlDB, user.ID, "Artist Mix", nil, "artist",
		`{"trackCount": 30, "artistIds": ["a1", "a2"], "schemaVersion": 1}`)
	if err != nil {
		t.Fatalf("CreateMixTemplate: %v", err)
	}

	router := testRouter(sqlDB, "PUT", "/mix-templates/{id}", h.update)
	form := url.Values{"trackCount": {"75"}, "sortBy": {"rating"}}
	rec := authedRequest(t, sqlDB, router, user, "PUT", "/mix-templates/"+itoa(tpl.ID), form.Encode(), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status=%d body=%s", rec.Code, rec.Body.String())
	}

	updated, err := db.GetMixTemplateByID(sqlDB, tpl.ID)
	if err != nil || updated == nil {
		t.Fatalf("GetMixTemplateByID: %v", err)
	}
	var cfg map[string]any
	json.Unmarshal([]byte(updated.Configuration), &cfg)

	if cfg["trackCount"] != float64(75) {
		t.Errorf("trackCount = %v, want 75", cfg["trackCount"])
	}
	if cfg["sortBy"] != "rating" {
		t.Errorf("sortBy = %v, want rating", cfg["sortBy"])
	}
	artistIDs, _ := cfg["artistIds"].([]any)
	if len(artistIDs) != 2 {
		t.Errorf("artistIds not preserved by granular edit: %v", cfg["artistIds"])
	}
}

func TestMixTemplateUpdate_RejectsZeroTrackCountFromGranularEdit(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &MixTemplatesHandler{DB: sqlDB, Tmpl: nopTemplates()}

	tpl, _ := db.CreateMixTemplate(sqlDB, user.ID, "Mood Mix", nil, "mood", `{"trackCount": 30, "moods": ["chill"]}`)

	router := testRouter(sqlDB, "PUT", "/mix-templates/{id}", h.update)
	form := url.Values{"trackCount": {"0"}}
	rec := authedRequest(t, sqlDB, router, user, "PUT", "/mix-templates/"+itoa(tpl.ID), form.Encode(), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestMixTemplateUpdate_ForbiddenForOtherUsersTemplate(t *testing.T) {
	sqlDB := newTestDB(t)
	owner := newTestUser(t, sqlDB)
	attacker, err := db.CreateUser(sqlDB, "plex-attacker", "attacker", "tok", "")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	h := &MixTemplatesHandler{DB: sqlDB, Tmpl: nopTemplates()}
	tpl, _ := db.CreateMixTemplate(sqlDB, owner.ID, "Owner's Mix", nil, "mood", `{"trackCount": 30, "moods": ["chill"]}`)

	router := testRouter(sqlDB, "PUT", "/mix-templates/{id}", h.update)
	form := url.Values{"name": {"Hijacked"}}
	rec := authedRequest(t, sqlDB, router, attacker, "PUT", "/mix-templates/"+itoa(tpl.ID), form.Encode(), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}

	unchanged, _ := db.GetMixTemplateByID(sqlDB, tpl.ID)
	if unchanged.Name != "Owner's Mix" {
		t.Errorf("template was modified despite 403: name=%q", unchanged.Name)
	}
}

func TestMixTemplateDelete_RoundTrip(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &MixTemplatesHandler{DB: sqlDB, Tmpl: nopTemplates()}
	tpl, _ := db.CreateMixTemplate(sqlDB, user.ID, "Delete Me", nil, "mood", `{"trackCount": 30, "moods": ["chill"]}`)

	router := testRouter(sqlDB, "DELETE", "/mix-templates/{id}", h.delete)
	rec := authedRequest(t, sqlDB, router, user, "DELETE", "/mix-templates/"+itoa(tpl.ID), "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: status=%d body=%s", rec.Code, rec.Body.String())
	}

	gone, err := db.GetMixTemplateByID(sqlDB, tpl.ID)
	if err != nil {
		t.Fatalf("GetMixTemplateByID: %v", err)
	}
	if gone != nil {
		t.Errorf("template still present after delete")
	}
}
