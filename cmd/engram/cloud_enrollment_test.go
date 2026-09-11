package main

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func TestCmdCloudUnenrollIsIdempotentAndPreservesPendingRows(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.EnrollProject("project_one"); err != nil {
		_ = s.Close()
		t.Fatalf("seed enrollment: %v", err)
	}
	if _, err := s.DB().Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`, constants.TargetKeyCloud, store.SyncEntityObservation, "observation-1", store.SyncOpUpsert, `{"sync_id":"observation-1"}`, store.SyncSourceLocal, "project_one"); err != nil {
		_ = s.Close()
		t.Fatalf("seed pending mutation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}

	for range 2 {
		withArgs(t, "engram", "cloud", "unenroll", "project_one")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("unenroll = stdout %q stderr %q panic %v", stdout, stderr, recovered)
		}
		if !strings.Contains(stdout, "unenrolled from future cloud sync") {
			t.Fatalf("unenroll output = %q", stdout)
		}
	}

	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = s.Close() }()
	enrolled, err := s.IsProjectEnrolled("project_one")
	if err != nil {
		t.Fatalf("check enrollment: %v", err)
	}
	if enrolled {
		t.Fatal("project remains enrolled after unenroll")
	}
	var pending int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = ? AND acked_at IS NULL`, "project_one").Scan(&pending); err != nil {
		t.Fatalf("count pending rows: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending rows = %d, want 1 preserved row", pending)
	}
}

func TestCmdCloudStatusKeepsEnvTokenVisibleWithoutServer(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "env-token")

	withArgs(t, "engram", "cloud", "status")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud status = stdout %q stderr %q panic %v", stdout, stderr, recovered)
	}
	for _, want := range []string{
		"Cloud status: not configured (no effective server URL)",
		"Auth status: ready (token provided via ENGRAM_CLOUD_TOKEN)",
		"Sync readiness: blocked (no effective server URL)",
		"Project enrollment: not checked (use --project <name>)",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("cloud status missing %q: %q", want, stdout)
		}
	}
	if strings.Contains(stdout, "Sync readiness: ready") {
		t.Fatalf("cloud status must not report readiness without a server: %q", stdout)
	}
}

func TestCmdCloudStatusReportsEffectiveStateAndProjectEnrollment(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://env-cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "env-token")
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.EnrollProject("project_one"); err != nil {
		_ = s.Close()
		t.Fatalf("seed enrollment: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}

	withArgs(t, "engram", "cloud", "status", "--project", "project_one")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud status = stdout %q stderr %q panic %v", stdout, stderr, recovered)
	}
	for _, want := range []string{
		"Server source: ENGRAM_CLOUD_SERVER",
		"Auth status: ready (token provided via ENGRAM_CLOUD_TOKEN)",
		"Project enrollment: enrolled (project_one)",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("cloud status missing %q: %q", want, stdout)
		}
	}
}
