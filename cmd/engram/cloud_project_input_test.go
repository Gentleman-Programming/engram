package main

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v2/internal/sync"
)

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
			defer s.Close()
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
	_, _, err := normalizeCloudCLIProjectInput("my%2project")
	if err == nil || !strings.Contains(err.Error(), "invalid URL escape") {
		t.Fatalf("expected malformed URL escape error, got %v", err)
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
