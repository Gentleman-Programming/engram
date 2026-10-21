package main

import (
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestProjectsMergeCanonicalization(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "legacy-session", "acme-api", "note", "legacy-note", "content", "project")
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce a stored legacy spelling without going through current normalization.
	if _, err := s.DB().Exec(`UPDATE observations SET project = 'Acme-api'; UPDATE sessions SET project = 'Acme-api'`); err != nil {
		t.Fatal(err)
	}
	before, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	pendingBefore, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	withArgs(t, "engram", "projects", "merge", "--from", "Acme-api", "--to", "acme-api", "--dry-run")
	out, stderr := captureOutput(t, func() { cmdProjects(cfg) })
	want := "[dry-run] Source \"Acme-api\" -> target \"acme-api\": observations 1, sessions 1, prompts 0. Sync identity changes: false. No changes made. Point-in-time preview; apply revalidates and counts may differ.\n"
	if out != want || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q", out, stderr)
	}
	s, err = store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewExplicitProjectMerge("Acme-api", "acme-api"); err != nil {
		t.Fatalf("dry-run changed source: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	withArgs(t, "engram", "projects", "merge", "--from", "Acme-api", "--to", "acme-api", "--apply")
	out, stderr = captureOutput(t, func() { cmdProjects(cfg) })
	want = "Merged source \"Acme-api\" into target \"acme-api\": observations 1, sessions 1, prompts 0. Sync identity may also change.\n"
	if out != want || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q", out, stderr)
	}
	s, err = store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.PreviewExplicitProjectMerge("Acme-api", "acme-api"); err == nil {
		t.Fatal("apply left source")
	}
	after, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	before.ExportedAt, after.ExportedAt = "", ""
	for i := range before.Sessions {
		before.Sessions[i].Project = "acme-api"
	}
	for i := range before.Observations {
		p := "acme-api"
		before.Observations[i].Project = &p
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("apply changed records beyond ownership: before=%+v after=%+v", before, after)
	}
	pendingAfter, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pendingBefore, pendingAfter) {
		t.Fatal("local merge changed journal")
	}
	oldExit := exitFunc
	exitFunc = func(code int) { panic(code) }
	t.Cleanup(func() { exitFunc = oldExit })
	withArgs(t, "engram", "projects", "merge", "--from", "Acme-api", "--to", "acme-api", "--apply")
	out, stderr, recovered := captureOutputAndRecover(t, func() { cmdProjects(cfg) })
	if recovered == nil || out != "" || stderr == "" {
		t.Fatalf("repeat stdout=%q stderr=%q exit=%v", out, stderr, recovered)
	}
	repeated, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	repeated.ExportedAt = ""
	pendingRepeated, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, repeated) || !reflect.DeepEqual(pendingAfter, pendingRepeated) {
		t.Fatal("repeat rejection mutated state")
	}
	enrolled, err := s.ListEnrolledProjects()
	if err != nil || len(enrolled) != 0 {
		t.Fatalf("enrollment = %+v, %v", enrolled, err)
	}
}
