package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
)

func TestLiteralDetectedProjectPreservesStoredHistoryWithoutBinding(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://host/team/my%20project.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no binding, got %v", err)
	}
	s, err := store.New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.CreateSession("legacy", "my%20project", dir); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(store.AddObservationParams{SessionID: "legacy", Type: "decision", Title: "history", Content: "preserved history", Project: "my%20project", Scope: "project"})
	if err != nil {
		t.Fatal(err)
	}
	detected := project.DetectProjectFull(dir)
	session, err := s.GetSession("legacy")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	if detected.Error != nil || detected.Project != "my%20project" || session.Project != detected.Project || observation.Project == nil || *observation.Project != detected.Project {
		t.Fatalf("history split: detection=%+v session=%+v observation=%+v", detected, session, observation)
	}
}

func TestLiteralCloudProjectTargetIsolation(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.EnrollProject("my%20project"); err != nil {
		t.Fatal(err)
	}
	for project, chunk := range map[string]string{"my%20project": "literal-chunk", "my project": "decoded-chunk"} {
		if err := s.RecordSyncedChunkForTarget(cloudTargetKeyForProject(project), chunk); err != nil {
			t.Fatal(err)
		}
	}
	queries := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/sync/pull" {
			http.Error(w, "unexpected request", 400)
			return
		}
		queries <- r.URL.Query().Get("project")
		_, _ = fmt.Fprint(w, `{"version":1,"chunks":[{"id":"literal-chunk"}]}`)
	}))
	defer server.Close()
	t.Setenv("ENGRAM_CLOUD_SERVER", server.URL)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_SYNC", "false")
	withArgs(t, "engram", "sync", "--cloud", "--status", "--literal-project", "--project", "my%20project")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("status: %v %s", recovered, stderr)
	}
	for _, want := range []string{"Local chunks:    1", "Remote chunks:   1", "Pending import:  0"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("missing %q: %s", want, stdout)
		}
	}
	select {
	case got := <-queries:
		if got != "my%20project" {
			t.Fatalf("transport project: %q", got)
		}
	default:
		t.Fatal("transport was not executed")
	}
}

func TestLiteralCloudProjectDelimitedNames(t *testing.T) {
	for _, name := range []string{"-project", "-h", "help"} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			for _, command := range []string{"enroll", "unenroll"} {
				withArgs(t, "engram", "cloud", command, "--literal-project", "--", name)
				stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
				if recovered != nil || stderr != "" || !strings.Contains(stdout, fmt.Sprintf("Project %q", name)) {
					t.Fatalf("%s: %v %s %s", command, recovered, stderr, stdout)
				}
			}
			withArgs(t, "engram", "cloud", "enroll", "--", name)
			captureOutputAndRecover(t, func() { cmdCloud(cfg) })
			t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
			old := syncStatus
			syncStatus = func(_ *engramsync.Syncer) (int, int, int, error) { return 0, 0, 0, nil }
			t.Cleanup(func() { syncStatus = old })
			withArgs(t, "engram", "sync", "--cloud", "--status", "--literal-project", "--project="+name)
			stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
			if recovered != nil || stderr != "" || !strings.Contains(stdout, fmt.Sprintf("project=%q", name)) {
				t.Fatalf("sync: %v %s %s", recovered, stderr, stdout)
			}
		})
	}
}

func TestLiteralCloudProjectNormalization(t *testing.T) {
	for _, input := range []string{"my%20project", "my%2520project", "my+project", "my%2project", "  My Project  "} {
		want, _ := store.NormalizeProject(strings.TrimSpace(input))
		got, _, err := normalizeCloudCLIProjectInput(input, true)
		if err != nil || got != want {
			t.Fatalf("literal %q: got %q, want %q, err %v", input, got, want, err)
		}
	}
}

func TestLiteralCloudProjectCommands(t *testing.T) {
	for _, input := range []string{"my%20project", "my%2520project", "my+project", "my%2project"} {
		t.Run(input, func(t *testing.T) {
			cfg := testConfig(t)
			withArgs(t, "engram", "cloud", "enroll", "--literal-project", input)
			_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudEnroll(cfg) })
			if recovered != nil || stderr != "" {
				t.Fatalf("enroll: %v %s", recovered, stderr)
			}
			s, err := store.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if err := s.EnrollProject("my project"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
			old := syncStatus
			syncStatus = func(_ *engramsync.Syncer) (int, int, int, error) { return 0, 0, 0, nil }
			t.Cleanup(func() { syncStatus = old })
			for _, env := range []bool{false, true} {
				t.Setenv("ENGRAM_CLOUD_SYNC", fmt.Sprint(env))
				args := []string{"engram", "sync", "--status", "--literal-project", "--project", input}
				if !env {
					args = append(args, "--cloud")
				}
				withArgs(t, args...)
				stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
				if recovered != nil || stderr != "" || !strings.Contains(stdout, fmt.Sprintf("project=%q", input)) {
					t.Fatalf("status: %v %s %s", recovered, stderr, stdout)
				}
			}
			withArgs(t, "engram", "cloud", "unenroll", input, "--literal-project")
			_, stderr, recovered = captureOutputAndRecover(t, func() { cmdCloudUnenroll(cfg) })
			if recovered != nil || stderr != "" {
				t.Fatalf("unenroll: %v %s", recovered, stderr)
			}
			for project, want := range map[string]bool{input: false, "my project": true} {
				got, err := s.IsProjectEnrolled(project)
				if err != nil || got != want {
					t.Fatalf("enrollment %q: %v %v", project, got, err)
				}
			}
		})
	}
}

func TestLiteralCloudProjectInvalidFlagsBeforeStore(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_SYNC", "false")
	stubExitWithPanic(t)
	old := storeNew
	storeNew = func(store.Config) (*store.Store, error) { t.Fatal("store opened for invalid flags"); return nil, nil }
	t.Cleanup(func() { storeNew = old })
	for _, args := range [][]string{
		{"sync", "--cloud", "--project", "--literal-project", "--status"},
		{"sync", "--cloud", "--project", "-h"},
		{"sync", "--cloud", "--project", "--help"},
		{"sync", "--literal-project"},
		{"sync", "--cloud", "--literal-project", "--project", ""},
		{"sync", "--cloud", "--literal-project", "--all"},
		{"sync", "--literal-project", "--project", "name"},
		{"sync", "--cloud", "--import", "--literal-project"},
		{"cloud", "enroll", "--literal-project"},
		{"cloud", "enroll", "name", "extra"},
		{"cloud", "unenroll", "name", "--unknown"},
		{"cloud", "unenroll", "name", "extra"},
		{"cloud", "enroll", "name", "--unknown"},
		{"cloud", "unenroll", "--literal-project"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			withArgs(t, append([]string{"engram"}, args...)...)
			_, stderr, recovered := captureOutputAndRecover(t, func() {
				if args[0] == "sync" {
					cmdSync(testConfig(t))
				} else {
					cmdCloud(testConfig(t))
				}
			})
			if recovered == nil || stderr == "" {
				t.Fatalf("expected flag rejection: %v %q", recovered, stderr)
			}
		})
	}
}

func TestCloudCLIProjectInputUsesSinglePathUnescape(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "literal space", input: "my project", want: "my project"},
		{name: "encoded space", input: "my%20project", want: "my project"},
		{name: "encoded percent sequence", input: "my%2520project", want: "my%20project"},
		{name: "literal plus", input: "my+project", want: "my+project"},
	}

	for _, tt := range tests {
		t.Run("enroll "+tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			withArgs(t, "engram", "cloud", "enroll", tt.input)

			stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudEnroll(cfg) })
			if recovered != nil || stderr != "" {
				t.Fatalf("enrollment should succeed, panic=%v stderr=%q", recovered, stderr)
			}
			if !strings.Contains(stdout, `Project "`+tt.want+`" enrolled`) {
				t.Fatalf("expected canonical enrollment output for %q, got %q", tt.want, stdout)
			}

			s, err := store.New(cfg)
			if err != nil {
				t.Fatalf("store.New: %v", err)
			}
			defer func() { _ = s.Close() }()
			enrolled, err := s.IsProjectEnrolled(tt.want)
			if err != nil {
				t.Fatalf("IsProjectEnrolled(%q): %v", tt.want, err)
			}
			if !enrolled {
				t.Fatalf("expected %q to be enrolled", tt.want)
			}
		})

		t.Run("sync "+tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			s, err := store.New(cfg)
			if err != nil {
				t.Fatalf("store.New: %v", err)
			}
			if err := s.EnrollProject(tt.want); err != nil {
				_ = s.Close()
				t.Fatalf("EnrollProject(%q): %v", tt.want, err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("close store: %v", err)
			}

			t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
			oldSyncStatus := syncStatus
			syncStatus = func(_ *engramsync.Syncer) (int, int, int, error) {
				return 0, 0, 0, nil
			}
			t.Cleanup(func() { syncStatus = oldSyncStatus })
			withArgs(t, "engram", "sync", "--cloud", "--status", "--project", tt.input)

			stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
			if recovered != nil || stderr != "" {
				t.Fatalf("cloud sync status should succeed, panic=%v stderr=%q", recovered, stderr)
			}
			if !strings.Contains(stdout, `Cloud sync status (project="`+tt.want+`")`) {
				t.Fatalf("expected canonical cloud sync project %q, got %q", tt.want, stdout)
			}
		})
	}
}

func TestNormalizeCloudCLIProjectInputRejectsMalformedEscapes(t *testing.T) {
	_, _, err := normalizeCloudCLIProjectInput("my%2project", false)
	if err == nil || !strings.Contains(err.Error(), "invalid URL escape") {
		t.Fatalf("expected malformed URL escape error, got %v", err)
	}
}

// TestCloudCLIEnrollRejectsReservedInboxProject proves the CLI surfaces the
// store-level rejection of the reserved cloud inbox project name and leaves
// enrollment empty.
func TestCloudCLIEnrollRejectsReservedInboxProject(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	withArgs(t, "engram", "cloud", "enroll", "inbox")

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudEnroll(cfg) })
	if code, ok := recovered.(exitCode); !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1, got %v", recovered)
	}
	if !strings.Contains(stderr, "reserved for the cloud inbox") {
		t.Fatalf("expected reserved-name rejection on stderr, got %q", stderr)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	enrolled, err := s.IsProjectEnrolled("inbox")
	if err != nil {
		t.Fatalf("IsProjectEnrolled(inbox): %v", err)
	}
	if enrolled {
		t.Fatal("the reserved inbox project name must never enroll")
	}
}

func TestCloudCLICommandsRejectMalformedProjectEscapes(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)

	tests := []struct {
		name   string
		invoke func()
	}{
		{
			name: "enroll",
			invoke: func() {
				withArgs(t, "engram", "cloud", "enroll", "my%2project")
				cmdCloudEnroll(cfg)
			},
		},
		{
			name: "sync",
			invoke: func() {
				withArgs(t, "engram", "sync", "--cloud", "--project", "my%2project")
				cmdSync(cfg)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, recovered := captureOutputAndRecover(t, tt.invoke)
			if code, ok := recovered.(exitCode); !ok || int(code) != 1 {
				t.Fatalf("expected exit code 1, got %v", recovered)
			}
			if !strings.Contains(stderr, "invalid cloud project URL encoding") {
				t.Fatalf("expected malformed encoding error, got %q", stderr)
			}
		})
	}
}
