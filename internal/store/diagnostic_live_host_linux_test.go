package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeSessionDiagnosticRechecksChangedRoot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recreate bool
		want     RuntimeSessionDiagnosticState
	}{
		{"removed", false, RuntimeSessionDiagnosticMissing},
		{"recreated active", true, RuntimeSessionDiagnosticActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, fixture, worktree := t.TempDir(), t.TempDir(), t.TempDir()
			marker := filepath.Join(fixture, "checking")
			release := filepath.Join(fixture, "release")
			git := "#!/bin/sh\n: > \"$FIXTURE_GIT_MARKER\"\n" +
				"while [ ! -f \"$FIXTURE_GIT_RELEASE\" ]; do sleep 0.005; done\n" +
				"printf '%s\\n' \"$FIXTURE_GIT_WORKTREE\"\n"
			if err := os.WriteFile(filepath.Join(fixture, "git"), []byte(git), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fixture+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FIXTURE_GIT_MARKER", marker)
			t.Setenv("FIXTURE_GIT_RELEASE", release)
			t.Setenv("FIXTURE_GIT_WORKTREE", worktree)

			cfg := FallbackConfig(data)
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			const root, project = "doctor-root", "doctor-project"
			if err := s.CreateSession(root, project, worktree); err != nil {
				t.Fatal(err)
			}
			if err := s.EndSession(root, ""); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			type outcome struct {
				state RuntimeSessionDiagnosticState
				err   error
			}
			result := make(chan outcome, 1)
			go func() {
				state, err := ReadRuntimeSessionDiagnostic(ctx, data, root)
				result <- outcome{state, err}
			}()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				_, statErr := os.Stat(marker)
				if statErr == nil {
					break
				}
				if !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal(statErr)
				}
				select {
				case <-ticker.C:
				case early := <-result:
					t.Fatalf("diagnostic returned before its scope check: %+v", early)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if err = s.DeleteSession(root); err != nil {
				t.Fatal(err)
			}
			if tc.recreate {
				if err = s.CreateSession(root, project, worktree); err != nil {
					t.Fatal(err)
				}
			}
			if err = os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			got := <-result
			if got.err != nil || got.state != tc.want {
				t.Fatalf("diagnostic returned %q, %v; want %q", got.state, got.err, tc.want)
			}
		})
	}
}
