package main

import (
	"os"
	"testing"
	"time"
)

func TestJSONSaveLoadJobs(t *testing.T) {
	tmp := "test_jobs.json"
	defer os.Remove(tmp)
	jobs := []PsipredJob{{ID: "j1", VariantCode: "V1", State: "queued", CreatedAt: time.Now(), UpdatedAt: time.Now()}}
	if err := saveJobs(tmp, jobs); err != nil {
		t.Fatalf("saveJobs failed: %v", err)
	}
	got, err := loadJobs(tmp)
	if err != nil {
		t.Fatalf("loadJobs failed: %v", err)
	}
	if len(got) != 1 || got[0].ID != "j1" {
		t.Fatalf("unexpected jobs loaded: %#v", got)
	}
}
