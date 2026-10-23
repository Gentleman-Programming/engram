package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestObsidianExportLimitPublicCLI(t *testing.T) {
	binary := buildCompatibilityBinary(t)
	root := t.TempDir()
	data, vault := filepath.Join(root, "data"), filepath.Join(root, "vault")
	run := func(args ...string) compatibilityRun {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "ENGRAM_DATA_DIR="+data, "ENGRAM_PROJECT=", "HOME="+root, "USERPROFILE="+root, "ENGRAM_NO_UPDATE_CHECK=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		result := compatibilityRun{stdout: stdout.String(), stderr: stderr.String()}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.exit = exitErr.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return result
	}
	for i := 1; i <= 5; i++ {
		result := run("save", fmt.Sprintf("note %d", i), fmt.Sprintf("content %d", i), "--project", "alpha")
		if result.exit != 0 || result.stderr != "" {
			t.Fatalf("seed: %+v", result)
		}
	}
	args := []string{"obsidian-export", "--vault", vault, "--graph-config", "skip", "--all"}
	expectedReport := "Obsidian export complete\n  Created: 5\n  Updated: 0\n  Deleted: 0\n  Skipped: 0\n  Hubs:    1\n"
	result := run(args...)
	if result.exit != 0 || result.stdout != expectedReport || result.stderr != "" {
		t.Fatalf("unlimited export: %+v", result)
	}
	// Hash the complete public output and isolated data tree, including counters,
	// hub notes and incremental state, not just the exported observation count.
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"positive", []string{"--limit", "2"}},
		{"zero", []string{"--limit", "0"}},
		{"negative", []string{"--limit", "-3"}},
		{"nonnumeric", []string{"--limit", "abc"}},
		{"missing value", []string{"--limit"}},
		{"equals", []string{"--limit=2"}},
		{"watch", []string{"--limit", "2", "--watch", "--interval", "30s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := compatibilityTreeHash(t, root)
			result := run(append(append([]string{}, args...), tc.flags...)...)
			if result.exit != 1 || result.stdout != "" || result.stderr != "error: --limit is not supported by obsidian-export; omit it to export all matching observations\n" {
				t.Errorf("rejection: %+v", result)
			}
			if compatibilityTreeHash(t, root) != before {
				t.Error("rejected invocation changed the store or vault")
			}
		})
	}
	t.Run("search limit is unchanged", func(t *testing.T) {
		result := run("search", "content", "--project", "alpha", "--limit", "2")
		if result.exit != 0 || result.stderr != "" || !strings.Contains(result.stdout, "2 memories") {
			t.Fatalf("search limit: %+v", result)
		}
	})
	t.Run("help describes rejection", func(t *testing.T) {
		result := run("help")
		if result.exit != 0 || result.stderr != "" || !strings.Contains(result.stdout, "--limit         Unsupported; rejected to protect complete reconciliation") {
			t.Fatalf("help: %+v", result)
		}
	})
}
