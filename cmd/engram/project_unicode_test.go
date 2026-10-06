package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestUnicodeProjectsCLIRepair(t *testing.T) {
	const source, canonical = "cafe\u0301", "café"
	for _, command := range []string{"merge", "consolidate"} {
		t.Run(command, func(t *testing.T) {
			cfg := testConfig(t)
			mustSeedObservation(t, cfg, "canonical-session", canonical, "note", "unicode canonical", "canonical content", "project")
			mustSeedObservation(t, cfg, "legacy-session", "legacy-source", "note", "unicode legacy", "legacy content", "project")
			mustSeedPrompt(t, cfg, "legacy-session", "legacy-source")
			rewriteLegacyProjectName(t, cfg, "legacy-source", source)
			s, err := store.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			before, err := s.Export()
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"engram", "projects", "merge", "--from", source, "--to", canonical, "--dry-run"}
			run := func() { cmdProjectsMerge(cfg) }
			if command == "consolidate" {
				args = []string{"engram", "projects", "consolidate", "--all", "--dry-run"}
				run = func() { cmdProjectsConsolidate(cfg) }
				old := scanInputLine
				t.Cleanup(func() { scanInputLine = old })
				scanInputLine = func(a ...any) (int, error) { *a[0].(*string) = "all"; return 1, nil }
			}
			withArgs(t, args...)
			stdout, stderr, code := captureExitPanic(t, run)
			if code != 0 || stderr != "" {
				t.Fatalf("dry-run exit=%d stderr=%q", code, stderr)
			}
			if command == "merge" {
				want := fmt.Sprintf("[dry-run] Source %q -> target %q: observations 1, sessions 1, prompts 1. Sync identity changes: false. No changes made. Point-in-time preview; apply revalidates and counts may differ.\n", source, canonical)
				if stdout != want {
					t.Fatalf("stdout = %q, want %q", stdout, want)
				}
			} else if !strings.Contains(stdout, `Suggested canonical: "café"`) || !strings.Contains(stdout, `[dry-run] Would merge into "café"`) {
				t.Fatalf("Unicode group missing: %q", stdout)
			}
			after, err := s.Export()
			if err != nil {
				t.Fatal(err)
			}
			before.ExportedAt = ""
			after.ExportedAt = ""
			if !reflect.DeepEqual(before, after) {
				t.Fatal("dry-run changed stored data or counters")
			}
			if command == "merge" {
				args[len(args)-1] = "--apply"
			} else {
				args = args[:len(args)-1]
			}
			withArgs(t, args...)
			stdout, stderr, code = captureExitPanic(t, run)
			if code != 0 || stderr != "" {
				t.Fatalf("apply exit=%d stderr=%q", code, stderr)
			}
			want := "Merged: 1 obs, 1 sessions, 1 prompts"
			if command == "merge" {
				want = fmt.Sprintf("Merged source %q into target %q: observations 1, sessions 1, prompts 1. Sync identity may also change.\n", source, canonical)
			}
			if !strings.Contains(stdout, want) {
				t.Fatalf("apply report = %q, want %q", stdout, want)
			}
			data, err := s.Export()
			if err != nil {
				t.Fatal(err)
			}
			if len(data.Observations) != 2 || len(data.Sessions) != 2 || len(data.Prompts) != 1 {
				t.Fatalf("records lost: %+v", data)
			}
			for _, obs := range data.Observations {
				if obs.Project == nil || *obs.Project != canonical {
					t.Fatalf("observation not canonical: %+v", obs)
				}
			}
			for _, session := range data.Sessions {
				if session.Project != canonical {
					t.Fatalf("session not canonical: %+v", session)
				}
			}
			if data.Prompts[0].Project != canonical {
				t.Fatalf("prompt not canonical: %+v", data.Prompts[0])
			}
		})
	}
}
