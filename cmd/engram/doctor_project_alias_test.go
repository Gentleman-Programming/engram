package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/diagnostic"
	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestDoctorProjectAliasPublic(t *testing.T) { testDoctorProjectAliasPublic(t, "root-key") }

func TestDoctorProjectAliasInvalidRoot(t *testing.T) {
	for _, root := range []string{"root\x7fkey", `root\key`} {
		t.Run(root, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.Contains(root, `\`) {
				t.Skip("literal backslash basename requires POSIX filesystem")
			}
			testDoctorProjectAliasPublic(t, root)
		})
	}
}

func testDoctorProjectAliasPublic(t *testing.T, root string) {
	for _, check := range []string{diagnostic.CheckSessionProjectDirectoryMismatch, diagnostic.CheckManualSessionNameProjectMismatch} {
		conflicts := []bool{false}
		if root == "root-key" {
			conflicts = append(conflicts, true)
		}
		for _, conflict := range conflicts {
			t.Run(check+map[bool]string{false: "/cwd", true: "/conflict"}[conflict], func(t *testing.T) {
				cfg := testConfig(t)
				old := exitFunc
				code := 0
				exitFunc = func(c int) { code = c }
				t.Cleanup(func() { exitFunc = old })
				dir := newDoctorGitRepo(t, root)
				cmd := exec.Command("git", "remote", "set-url", "origin", "git@github.com:user/upstream-name.git")
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("set synthetic remote: %v %s", err, out)
				}
				if check == diagnostic.CheckSessionProjectDirectoryMismatch {
					detected := projectpkg.DetectProjectFull(dir)
					if detected.Error != nil || detected.Project != "upstream-name" {
						t.Fatalf("bind synthetic repo: %+v", detected)
					}
				} // An unbound Git root supplies an alias key, not directory authority.
				seedDoctorRepairRows(t, cfg, "manual-save-upstream-name", "my-name", dir)
				seedDoctorSession(t, cfg, "known-target", "upstream-name", "")
				// A root key differs from the trusted target but is not itself an alias.
				withArgs(t, "engram", "doctor", "repair", "--project", "my-name", "--check", check, "--plan")
				initial, initialErr := captureOutput(t, func() { cmdDoctor(cfg) })
				var noAlias diagnostic.RepairPlan
				if err := json.Unmarshal([]byte(initial), &noAlias); err != nil {
					t.Fatalf("decode no-alias plan: %v; exit=%d stdout=%q stderr=%q", err, code, initial, initialErr)
				}
				if code != 0 || initialErr != "" || len(noAlias.Actions) != 1 {
					t.Fatalf("no-alias regression: %s %s", initial, initialErr)
				}
				s, err := store.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.SetProjectAlias(store.ProjectAliasKindCWD, dir, "my-name"); err != nil {
					t.Fatal(err)
				}
				if conflict {
					if _, err = s.SetProjectAlias(store.ProjectAliasKindKey, filepath.Base(dir), "upstream-name"); err != nil {
						t.Fatal(err)
					}
				}
				before, err := s.Export()
				if err != nil {
					t.Fatal(err)
				}
				counts, err := s.Stats()
				if err != nil {
					t.Fatal(err)
				}
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
				withArgs(t, "engram", "doctor", "--project", "my-name", "--check", check, "--json")
				out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
				if code != 0 || stderr != "" || !strings.Contains(out, "cwd_alias") || (conflict && !strings.Contains(out, `"conflict": true`)) {
					t.Fatalf("code=%d stderr=%s out=%s", code, stderr, out)
				}
				for _, mode := range []string{"--plan", "--dry-run", "--apply"} {
					withArgs(t, "engram", "doctor", "repair", "--project", "my-name", "--check", check, mode)
					out, stderr = captureOutput(t, func() { cmdDoctor(cfg) })
					var plan diagnostic.RepairPlan
					if err = json.Unmarshal([]byte(out), &plan); err != nil {
						t.Fatalf("%v: %s", err, out)
					}
					if code != 0 || stderr != "" || plan.Status != "noop" || len(plan.Actions) != 0 || plan.Counts != (diagnostic.RepairCounts{}) {
						t.Fatalf("code=%d stderr=%s plan=%+v", code, stderr, plan)
					}
					if !strings.Contains(out, "project_alias_contradicts_reclassification") {
						t.Fatal(out)
					}
				}
				s, err = store.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := s.Close(); err != nil {
						t.Error(err)
					}
				}()
				after, err := s.Export()
				if err != nil {
					t.Fatal(err)
				}
				afterCounts, err := s.Stats()
				if err != nil {
					t.Fatal(err)
				}
				after.ExportedAt = before.ExportedAt // export wall clock is not persisted state
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(counts, afterCounts) {
					t.Fatal("noop changed stored rows or counters")
				}
				if check == diagnostic.CheckManualSessionNameProjectMismatch {
					if err := s.CreateSession("ordinary", "ordinary-project", dir); err != nil {
						t.Fatal(err)
					}
					if _, err := s.SetProjectAlias(store.ProjectAliasKindCWD, dir, "upstream-name"); err != nil {
						t.Fatal(err)
					}
					withArgs(t, "engram", "doctor", "repair", "--project", "ordinary-project", "--check", check, "--apply")
					out, stderr = captureOutput(t, func() { cmdDoctor(cfg) })
					var plan diagnostic.RepairPlan
					if err := json.Unmarshal([]byte(out), &plan); err != nil {
						t.Fatal(err)
					}
					if code != 0 || stderr != "" || plan.Status != "noop" || len(plan.Actions) != 0 {
						t.Fatalf("alias granted move: exit=%d stderr=%q stdout=%s", code, stderr, out)
					}
				}
			})
		}
	}
}
