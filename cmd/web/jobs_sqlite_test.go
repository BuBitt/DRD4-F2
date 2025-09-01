package main

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

func TestSaveLoadJobs_SQLite(t *testing.T) {
	// use a temp file
	f := "test_jobs.db"
	_ = os.Remove(f)
	defer os.Remove(f)

	// initialize sqlite store
	jobsStore = "sqlite"
	jobsPath = f

	// create DB
	var err error
	jobsDB, err = openSQLite(f)
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	defer jobsDB.Close()

	if _, err := jobsDB.Exec(`CREATE TABLE IF NOT EXISTS jobs (
        id TEXT PRIMARY KEY,
        variant_code TEXT,
        remote_uuid TEXT,
        state TEXT,
        message TEXT,
        created_at TEXT,
        updated_at TEXT
    )`); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	jobs := []PsipredJob{{ID: "j1", VariantCode: "V1", State: "queued", CreatedAt: now, UpdatedAt: now}}
	if err := saveJobs(f, jobs); err != nil {
		t.Fatalf("saveJobs failed: %v", err)
	}
	loaded, err := loadJobs(f)
	if err != nil {
		t.Fatalf("loadJobs failed: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != "j1" {
		t.Fatalf("unexpected loaded jobs: %#v", loaded)
	}
}

// openSQLite is a thin helper so tests can initialize the package-level jobsDB
func openSQLite(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path)
}
