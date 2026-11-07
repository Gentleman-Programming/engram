package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestProjectsAliasExistingListAndHelp(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "engram", "projects")
	out, stderr := captureOutput(t, func() { cmdProjects(cfg) })
	if out != "No projects found.\n" || stderr != "" {
		t.Fatalf("default list %q %q", out, stderr)
	}
	withArgs(t, "engram", "projects", "list")
	out, stderr = captureOutput(t, func() { cmdProjects(cfg) })
	if out != "No projects found.\n" || stderr != "" {
		t.Fatalf("explicit list %q %q", out, stderr)
	}
	out, stderr = captureOutput(t, printProjectsUsage)
	legacy := "usage: engram projects list\n       engram projects merge --from <source> --to <canonical> (--dry-run|--apply)\n       engram projects consolidate [--all] [--dry-run]\n       engram projects prune [--dry-run] [--paths-only]\n       engram projects rescue-ownership --project <name> [--session <id>]... [--observation <id>]... [--prompt <id>]...\n"
	if out != "" || stderr != projectAliasUsage+legacy {
		t.Fatalf("projects help %q %q", out, stderr)
	}
	withArgs(t, "engram", "projects", "alias", "--help")
	out, stderr = captureOutput(t, func() { cmdProjects(cfg) })
	if out != projectAliasUsage || stderr != "" {
		t.Fatalf("alias help %q %q", out, stderr)
	}
}

func TestProjectsAliasCommandReachable(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "engram", "projects", "alias", "list")
	stubExitWithPanic(t)
	out, stderr, recovered := captureOutputAndRecover(t, func() { cmdProjects(cfg) })
	if recovered != nil || stderr != "" || out != "No project aliases.\n" {
		t.Fatalf("stdout=%q stderr=%q exit=%v", out, stderr, recovered)
	}
}

func TestProjectsAliasRejectedFlags(t *testing.T) {
	cfg := testConfig(t)
	stubExitWithPanic(t)
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"set", "--key", "a", "--cwd", "b", "--project", "owner"}, "exactly one of --key or --cwd is required"},
		{[]string{"set", "--key", "a"}, "--project is required"},
		{[]string{"remove"}, "exactly one of --key or --cwd is required"},
		{[]string{"list", "--project", "a"}, "unexpected argument \"--project\""},
		{[]string{"resolve", "--git-root", "forged"}, "unexpected argument \"--git-root\""},
		{[]string{"set", "--key", "a", "--key", "b", "--project", "owner"}, "duplicate flag --key"},
		{[]string{"set", "--key"}, "--key requires a value"},
		{[]string{"remove", "--cwd", ""}, "--cwd requires a value"},
		{[]string{"list", "extra"}, "unexpected argument \"extra\""},
		{[]string{"wat"}, "unknown command \"wat\""},
		{nil, "expected set, list, remove, or resolve"},
	} {
		args := tc.args
		withArgs(t, append([]string{"engram", "projects", "alias"}, args...)...)
		out, stderr, recovered := captureOutputAndRecover(t, func() { cmdProjects(cfg) })
		if recovered != exitCode(1) || out != "" || stderr != "project alias: "+tc.message+"\n" {
			t.Fatalf("args=%v stdout=%q stderr=%q exit=%v", args, out, stderr, recovered)
		}
	}
	withArgs(t, "engram", "projects", "alias", "list")
	out, stderr, recovered := captureOutputAndRecover(t, func() { cmdProjects(cfg) })
	if recovered != nil || stderr != "" || out != "No project aliases.\n" {
		t.Fatalf("rejected input drift: %q %q %v", out, stderr, recovered)
	}
}

func TestProjectsAliasCRUDAndReadOnlyPreview(t *testing.T) {
	cfg := testConfig(t)
	cwd := filepath.Clean(t.TempDir())
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(cwd))
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("history", "legacy", cwd); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("enrolled-history", "owner", cwd); err != nil {
		t.Fatal(err)
	}
	beforeStats, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	beforeSession, err := s.GetSession("history")
	if err != nil {
		t.Fatal(err)
	}
	beforeQueue, err := s.ListPendingSyncMutations("cloud", 1000)
	if err != nil {
		t.Fatal(err)
	}
	beforeHighwater, err := s.MaxPendingSyncMutationSeq("cloud")
	if err != nil {
		t.Fatal(err)
	}
	beforeEnrollment, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	stubExitWithPanic(t)
	call := func(want string, args ...string) {
		t.Helper()
		withArgs(t, append([]string{"engram", "projects", "alias"}, args...)...)
		out, stderr, recovered := captureOutputAndRecover(t, func() { cmdProjects(cfg) })
		if out != want || stderr != "" || recovered != nil {
			t.Fatalf("%v stdout=%q want=%q stderr=%q exit=%v", args, out, want, stderr, recovered)
		}
	}
	call("Alias set: key legacy -> owner\n", "set", "--key", "Legacy", "--project", "Owner")
	call(fmt.Sprintf("Alias set: cwd %s -> owner\n", cwd), "set", "--cwd", filepath.Join(cwd, "."), "--project", "owner")
	call(fmt.Sprintf("cwd\t%s\towner\nkey\tlegacy\towner\n", cwd), "list")
	for _, args := range [][]string{
		{"set", "--key", "legacy", "--project", "bad/name"},
		{"set", "--key", "bad/name", "--project", "owner"},
		{"remove", "--key", "bad/name"},
		{"resolve", "--designation", "bad/name", "--cwd", cwd},
	} {
		withArgs(t, append([]string{"engram", "projects", "alias"}, args...)...)
		out, stderr, exit := captureOutputAndRecover(t, func() { cmdProjects(cfg) })
		if out != "" || stderr != "project alias: invalid project name: bad/name (project must be a name, not a path)\n" || exit != exitCode(1) {
			t.Fatalf("invalid metadata: %v %q %q %v", args, out, stderr, exit)
		}
		call(fmt.Sprintf("cwd\t%s\towner\nkey\tlegacy\towner\n", cwd), "list")
	}
	preview := func(designation, source, key, mapped string, conflict, enrolled bool, keyOwner, cwdOwner string) {
		t.Helper()
		candidate := map[string]any{"key": key, "source": source, "directory": cwd, "raw_project": designation}
		alias := func(kind, key, owner string) any {
			if owner == "" {
				return nil
			}
			return map[string]any{"kind": kind, "key": key, "project": owner}
		}
		target := key
		if target == "" {
			target = "unsorted"
		}
		disposition := "inbox"
		if enrolled {
			target = mapped
			disposition = "enrolled"
		}
		want := map[string]any{"candidate": candidate, "aliases": map[string]any{"project": mapped, "key_alias": alias("key", key, keyOwner), "cwd_alias": alias("cwd", cwd, cwdOwner), "conflict": conflict}, "conflict": conflict, "enrolled": enrolled, "disposition": disposition, "project": target}
		// Struct order is the public JSON contract, asserted as complete bytes below.
		wireAlias := func(kind store.ProjectAliasKind, value, owner string) []byte {
			if owner == "" {
				return []byte("null")
			}
			b, _ := json.Marshal(store.ProjectAlias{Kind: kind, Key: value, Project: owner})
			return b
		}
		ka := wireAlias(store.ProjectAliasKindKey, key, keyOwner)
		ca := wireAlias(store.ProjectAliasKindCWD, cwd, cwdOwner)
		quoted := func(v string) string { b, _ := json.Marshal(v); return string(b) }
		exact := fmt.Sprintf("{\"candidate\":{\"key\":%s,\"source\":%s,\"directory\":%s,\"raw_project\":%s},\"aliases\":{\"project\":%s,\"key_alias\":%s,\"cwd_alias\":%s,\"conflict\":%t},\"conflict\":%t,\"enrolled\":%t,\"disposition\":%s,\"project\":%s}\n", quoted(key), quoted(source), quoted(cwd), quoted(designation), quoted(mapped), ka, ca, conflict, conflict, enrolled, quoted(disposition), quoted(target))
		var decoded map[string]any
		if err := json.Unmarshal([]byte(exact), &decoded); err != nil || !reflect.DeepEqual(decoded, want) {
			t.Fatalf("test expectation: %v", err)
		}
		args := []string{"resolve", "--cwd", cwd}
		if designation != "" {
			args = append(args, "--designation", designation)
		}
		call(exact, args...)
	}
	preview("Legacy", "explicit_override", "legacy", "owner", false, true, "owner", "owner")
	call(fmt.Sprintf("Alias set: cwd %s -> other\n", cwd), "set", "--cwd", cwd, "--project", "other")
	preview("Legacy", "explicit_override", "legacy", "", true, false, "owner", "other")
	preview("unknown", "explicit_override", "unknown", "other", false, false, "", "other")
	preview("", "unknown", "", "other", false, false, "", "other")
	call("Alias removed.\n", "remove", "--cwd", cwd)
	preview("unknown", "explicit_override", "unknown", "unknown", false, false, "", "")
	preview("owner", "explicit_override", "owner", "owner", false, true, "", "")
	preview("", "unknown", "", "", false, false, "", "")
	// A remote label is not root evidence, and inspection may not initialize binding.
	if out, err := exec.Command("git", "-C", cwd, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	if out, err := exec.Command("git", "-C", cwd, "remote", "add", "origin", "https://example.test/not-the-root.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote: %s %v", out, err)
	}
	rootKey := strings.ToLower(filepath.Base(cwd))
	preview("", "git_root", rootKey, rootKey, false, false, "", "")
	preview("unknown", "explicit_override", "unknown", "unknown", false, false, "", "")
	if _, err := os.Stat(filepath.Join(cwd, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
		t.Fatalf("resolve wrote Git binding: %v", err)
	}
	call("Alias removed.\n", "remove", "--key", "legacy")
	call("Alias not found.\n", "remove", "--key", "legacy")
	call("No project aliases.\n", "list")
	s, err = store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	afterStats, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	afterSession, err := s.GetSession("history")
	if err != nil {
		t.Fatal(err)
	}
	afterQueue, err := s.ListPendingSyncMutations("cloud", 1000)
	if err != nil {
		t.Fatal(err)
	}
	afterEnrollment, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatal(err)
	}
	afterHighwater, err := s.MaxPendingSyncMutationSeq("cloud")
	if err != nil || beforeHighwater != afterHighwater {
		t.Fatalf("highwater changed: %d -> %d %v", beforeHighwater, afterHighwater, err)
	}
	if !reflect.DeepEqual(beforeStats, afterStats) || !reflect.DeepEqual(beforeSession, afterSession) || !reflect.DeepEqual(beforeQueue, afterQueue) || !reflect.DeepEqual(beforeEnrollment, afterEnrollment) {
		t.Fatal("alias administration changed history, counters, queue, or enrollment")
	}
}

func TestProjectsAliasRootOnly(t *testing.T) {
	for _, name := range []string{"configured", "multiplechildren", "corruptbinding"} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			cwd := filepath.Clean(t.TempDir())
			t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(cwd))
			initRepo := func(dir string) {
				t.Helper()
				if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
					t.Fatalf("git init: %s %v", out, err)
				}
			}
			binding := filepath.Join(cwd, ".git", "engram-project-identity.json")
			var bindingBytes []byte
			key, source := "", "unknown"
			switch name {
			case "configured":
				initRepo(cwd)
				if err := os.Mkdir(filepath.Join(cwd, ".engram"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cwd, ".engram", "config.json"), []byte(`{"project_name":"not-the-root"}`), 0600); err != nil {
					t.Fatal(err)
				}
				key, source = filepath.Base(cwd), "git_root"
			case "multiplechildren":
				for _, child := range []string{"one", "two"} {
					dir := filepath.Join(cwd, child)
					if err := os.Mkdir(dir, 0755); err != nil {
						t.Fatal(err)
					}
					initRepo(dir)
				}
			case "corruptbinding":
				initRepo(cwd)
				bindingBytes = []byte("not valid binding JSON\n")
				if err := os.WriteFile(binding, bindingBytes, 0600); err != nil {
					t.Fatal(err)
				}
				key, source = filepath.Base(cwd), "git_root"
			}
			s, err := store.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			evidence := &store.ProjectProvenance{Source: "explicit_override", Directory: "raw/../capture", RawProject: " History "}
			if err := s.CreateSessionWithOwnershipMode("root-history", "history", cwd, store.SessionOwnershipShared, evidence); err != nil {
				t.Fatal(err)
			}
			if err := s.EnrollProject("history"); err != nil {
				t.Fatal(err)
			}
			observationID, err := s.AddObservation(store.AddObservationParams{SessionID: "root-history", Project: "history", Type: "note", Title: "history", Content: "preserve record", OperationID: "root-operation", ProjectProvenance: evidence})
			if err != nil {
				t.Fatal(err)
			}
			promptID, err := s.AddPrompt(store.AddPromptParams{SessionID: "root-history", Project: "history", Content: "preserve prompt", ProjectProvenance: evidence})
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range []struct{ kind, id string }{
				{"session", "root-history"}, {"observation", strconv.FormatInt(observationID, 10)}, {"prompt", strconv.FormatInt(promptID, 10)},
			} {
				got, err := s.CaptureProjectProvenance(record.kind, record.id)
				if err != nil || !reflect.DeepEqual(got, evidence) {
					t.Fatalf("seeded %s provenance: %#v %v", record.kind, got, err)
				}
			}
			if _, err := s.SetProjectAlias(store.ProjectAliasKindKey, "unrelated", "history"); err != nil {
				t.Fatal(err)
			}
			queue, err := s.ListPendingSyncMutations("cloud", 1000)
			if err != nil || len(queue) == 0 {
				t.Fatalf("meaningful queue missing: %v %v", queue, err)
			}
			highwater, err := s.MaxPendingSyncMutationSeq("cloud")
			if err != nil || highwater == 0 {
				t.Fatalf("meaningful highwater missing: %d %v", highwater, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				t.Helper()
				s, err := store.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
				}()
				values := []any{}
				capture := func(value any, err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
					values = append(values, value)
				}
				capture(s.ListProjectAliases())
				capture(s.GetSession("root-history"))
				capture(s.GetObservation(observationID))
				capture(s.GetObservationVersions(observationID))
				capture(s.GetObservationSaveResult("root-operation"))
				capture(s.RecentPrompts("history", 100))
				capture(s.CaptureProjectProvenance("session", "root-history"))
				capture(s.CaptureProjectProvenance("observation", strconv.FormatInt(observationID, 10)))
				capture(s.CaptureProjectProvenance("prompt", strconv.FormatInt(promptID, 10)))
				capture(s.Stats())
				capture(s.ListProjectsWithStats(""))
				capture(s.ListEnrolledProjects())
				states, err := s.ListSyncStates()
				capture(states, err)
				for _, state := range states {
					capture(s.GetSyncState(state.TargetKey))
				}
				capture(s.ListPendingSyncMutations("cloud", 1000))
				capture(s.MaxPendingSyncMutationSeq("cloud"))
				b, err := json.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			before := snapshot()
			stubExitWithPanic(t)
			preview := func(designation, key, source string) {
				t.Helper()
				target := key
				if target == "" {
					target = "unsorted"
				}
				quote := func(v string) string {
					b, err := json.Marshal(v)
					if err != nil {
						t.Fatal(err)
					}
					return string(b)
				}
				want := fmt.Sprintf("{\"candidate\":{\"key\":%s,\"source\":%s,\"directory\":%s,\"raw_project\":%s},\"aliases\":{\"project\":%s,\"key_alias\":null,\"cwd_alias\":null,\"conflict\":false},\"conflict\":false,\"enrolled\":false,\"disposition\":\"inbox\",\"project\":%s}\n", quote(key), quote(source), quote(cwd), quote(designation), quote(key), quote(target))
				args := []string{"engram", "projects", "alias", "resolve", "--cwd", cwd}
				if designation != "" {
					args = append(args, "--designation", designation)
				}
				withArgs(t, args...)
				out, stderr, exit := captureOutputAndRecover(t, func() { cmdProjects(cfg) })
				// Check protected effects even on RED, before reporting the output failure.
				if after := snapshot(); after != before {
					t.Fatalf("resolve changed protected public state\nbefore %s\nafter %s", before, after)
				}
				if bindingBytes != nil {
					got, err := os.ReadFile(binding)
					if err != nil || string(got) != string(bindingBytes) {
						t.Fatalf("binding changed: %q %v", got, err)
					}
				} else if name == "configured" {
					if _, err := os.Stat(binding); !os.IsNotExist(err) {
						t.Fatalf("binding initialized: %v", err)
					}
				} else {
					for _, child := range []string{"one", "two"} {
						if _, err := os.Stat(filepath.Join(cwd, child, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
							t.Fatalf("child binding initialized: %v", err)
						}
					}
				}
				if out != want || stderr != "" || exit != nil {
					t.Fatalf("stdout=%q want=%q stderr=%q exit=%v", out, want, stderr, exit)
				}
			}
			preview("Unknown--Name", "unknown-name", "explicit_override")
			preview("", key, source)
		})
	}
}
