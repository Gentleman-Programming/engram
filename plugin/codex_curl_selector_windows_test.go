package plugin_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Placeholder executables exercise selection only; no tool is executed.
func TestCodexWindowsCurlSelector(t *testing.T) {
	const childKey = "ENGRAM_TEST_CURL_SELECTOR_CHILD"
	if tool := os.Getenv(childKey); tool != "" {
		fmt.Printf("selected=%s\n", codexIntentTool(t, tool, os.Getenv("ENGRAM_TEST_CURL_SELECTOR_DIR")))
		return
	}
	root := t.TempDir()
	gitDir := filepath.Join(root, "git", "usr", "bin")
	legacy := filepath.Join(root, "git", "mingw64", "bin", "curl.exe")
	reviewed := filepath.Join(root, "reviewed", "curl.exe")
	nonExecutable := filepath.Join(root, "reviewed", "curl.cmd")
	for _, path := range []string{legacy, reviewed, nonExecutable} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	directory := filepath.Join(root, "directory.exe")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(reviewed)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tool, selector, wantError string
	}{
		{name: "explicit curl wins over Git layout", tool: "curl", selector: reviewed},
		{name: "missing curl rejects legacy fallback", tool: "curl", wantError: "explicit reviewed CODEX_TEST_TRUSTED_CURL executable required"},
		{name: "relative curl rejected", tool: "curl", selector: "curl.exe", wantError: "fixture tool must be absolute"},
		{name: "directory curl rejected", tool: "curl", selector: directory, wantError: "fixture tool must be regular"},
		{name: "script curl rejected", tool: "curl", selector: nonExecutable, wantError: "fixture requires real .exe tools, not script launchers"},
		{name: "missing selected curl does not fall back", tool: "curl", selector: filepath.Join(root, "missing.exe"), wantError: "cannot canonicalize fixture tool path="},
		{name: "existing bash selector unchanged", tool: "bash", selector: reviewed},
		{name: "existing jq selector unchanged", tool: "jq", selector: reviewed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(self, "-test.run=^TestCodexWindowsCurlSelector$", "-test.count=1")
			for _, item := range os.Environ() {
				key, _, _ := strings.Cut(item, "=")
				if !strings.HasPrefix(strings.ToUpper(key), "CODEX_TEST_TRUSTED_") && key != childKey {
					cmd.Env = append(cmd.Env, item)
				}
			}
			cmd.Env = append(cmd.Env, childKey+"="+tc.tool, "ENGRAM_TEST_CURL_SELECTOR_DIR="+gitDir,
				"CODEX_TEST_TRUSTED_"+strings.ToUpper(tc.tool)+"="+tc.selector)
			if tc.tool != "curl" {
				cmd.Env = append(cmd.Env, "CODEX_TEST_TRUSTED_CURL=ignored-relative-curl.exe")
			}
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %q", stderr.String())
			}
			if tc.wantError == "" {
				want := "selected=" + filepath.ToSlash(canonical) + "\nPASS\n"
				if err != nil || stdout.String() != want {
					t.Fatalf("selection: exit=%v stdout=%q; want exit=0 stdout=%q", err, stdout.String(), want)
				}
			} else {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 || strings.Count(stdout.String(), tc.wantError) != 1 || strings.Contains(stdout.String(), "selected=") {
					t.Fatalf("rejection: exit=%v stdout=%q; want exit=1, one %q and no selected result", err, stdout.String(), tc.wantError)
				}
			}
			if info, err := os.Stat(legacy); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("selector changed legacy fixture: %v", err)
			}
		})
	}
}
