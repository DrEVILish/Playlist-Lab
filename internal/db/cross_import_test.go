package db

import "testing"

func TestCrossImportJobLifecycle(t *testing.T) {
	sqlDB := newTestDB(t)
	u, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")

	jobID, err := CreateCrossImportJob(sqlDB, u.ID, "spotify", "My Playlist", "deezer")
	if err != nil {
		t.Fatalf("CreateCrossImportJob: %v", err)
	}

	if err := UpdateCrossImportJobPlaylist(sqlDB, jobID, "My Playlist", 10); err != nil {
		t.Fatalf("UpdateCrossImportJobPlaylist: %v", err)
	}
	if err := CompleteCrossImportJob(sqlDB, jobID, u.ID, "Target Playlist", 8, 1, 1, 10, "[]"); err != nil {
		t.Fatalf("CompleteCrossImportJob: %v", err)
	}

	jobs, err := GetCrossImportJobs(sqlDB, u.ID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("GetCrossImportJobs: %v got=%v", err, jobs)
	}
	j := jobs[0]
	if j.Status != "complete" || j.MatchedCount != 8 || j.TargetPlaylistName.String != "Target Playlist" {
		t.Fatalf("unexpected completed job: %+v", j)
	}

	if err := DeleteCrossImportJob(sqlDB, jobID); err != nil {
		t.Fatalf("DeleteCrossImportJob: %v", err)
	}
	jobs, _ = GetCrossImportJobs(sqlDB, u.ID)
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs after delete, got %d", len(jobs))
	}
}

func TestGetCrossImportJobsIsolation(t *testing.T) {
	sqlDB := newTestDB(t)
	u1, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	u2, _ := CreateUser(sqlDB, "plex2", "u2", "tok2", "")

	if _, err := CreateCrossImportJob(sqlDB, u1.ID, "spotify", "P1", "deezer"); err != nil {
		t.Fatalf("CreateCrossImportJob: %v", err)
	}

	u1Jobs, err := GetCrossImportJobs(sqlDB, u1.ID)
	if err != nil || len(u1Jobs) != 1 {
		t.Fatalf("expected 1 job for user1, got %v err=%v", u1Jobs, err)
	}
	u2Jobs, err := GetCrossImportJobs(sqlDB, u2.ID)
	if err != nil || len(u2Jobs) != 0 {
		t.Fatalf("expected 0 jobs leaked to user2, got %v err=%v", u2Jobs, err)
	}
}

// CompleteCrossImportJob is scoped by both id AND user_id - verify that a
// mismatched user_id (someone else's job id guessed/enumerated) is a no-op
// rather than letting a wrong caller complete another user's job.
func TestCompleteCrossImportJobWrongUserIsNoop(t *testing.T) {
	sqlDB := newTestDB(t)
	u1, _ := CreateUser(sqlDB, "plex1", "u1", "tok1", "")
	u2, _ := CreateUser(sqlDB, "plex2", "u2", "tok2", "")

	jobID, err := CreateCrossImportJob(sqlDB, u1.ID, "spotify", "P1", "deezer")
	if err != nil {
		t.Fatalf("CreateCrossImportJob: %v", err)
	}

	if err := CompleteCrossImportJob(sqlDB, jobID, u2.ID, "Hijacked", 1, 0, 0, 1, "[]"); err != nil {
		t.Fatalf("CompleteCrossImportJob: %v", err)
	}

	jobs, _ := GetCrossImportJobs(sqlDB, u1.ID)
	if len(jobs) != 1 || jobs[0].Status != "matching" {
		t.Fatalf("expected job untouched by wrong-user completion, got %+v", jobs)
	}
}
