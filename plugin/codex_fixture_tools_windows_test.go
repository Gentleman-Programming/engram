//go:build windows

package plugin_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCodexWindowsCurlFixtureResolution(t *testing.T) {
	const probeKey = "ENGRAM_TEST_CODEX_CURL_FIXTURE_CASE"
	if scenario := os.Getenv(probeKey); scenario != "" {
		codexWindowsCurlFixtureProbe(t, scenario)
		return
	}

	cases := []struct {
		name     string
		exit     int
		message  string
		pathPart string
	}{
		{name: "existing_git_layout"},
		{name: "native_curl_without_mingw64"},
		{name: "invalid_git_candidate", exit: 1, message: "fixture tool must be regular"},
		{name: "invalid_native_candidate", exit: 1, message: "fixture tool must be regular"},
		{name: "relative_system_root", exit: 1, message: "fixture tool must be absolute"},
		{name: "no_trusted_candidate", exit: 1, message: "cannot canonicalize fixture tool path=", pathPart: "System32"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			// Exercise t.Fatal through the Go test executable, never execute the
			// marker .exe files used solely for path-selection assertions.
			cmd := exec.Command(os.Args[0], "-test.run=^TestCodexWindowsCurlFixtureResolution$", "-test.v")
			cmd.Env = append(os.Environ(), probeKey+"="+tt.name)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			exit := 0
			if err != nil {
				if failed, ok := err.(*exec.ExitError); ok {
					exit = failed.ExitCode()
				} else {
					t.Fatalf("start fixture probe: %v", err)
				}
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %q", stderr.String())
			}
			if exit != tt.exit {
				t.Fatalf("probe exit=%d, want %d; stdout:\n%s", exit, tt.exit, stdout.String())
			}
			if tt.message != "" && !strings.Contains(stdout.String(), tt.message) {
				t.Fatalf("missing diagnostic %q; stdout:\n%s", tt.message, stdout.String())
			}
			if tt.pathPart != "" && !strings.Contains(stdout.String(), tt.pathPart) {
				t.Fatalf("missing attempted native candidate %q; stdout:\n%s", tt.pathPart, stdout.String())
			}
		})
	}
}

func codexWindowsCurlFixtureProbe(t *testing.T, scenario string) {
	t.Helper()
	root := t.TempDir()
	toolDir := filepath.Join(root, "git", "usr", "bin")
	systemRoot := filepath.Join(root, "windows")
	gitCurl := filepath.Join(root, "git", "mingw64", "bin", "curl.exe")
	nativeCurl := filepath.Join(systemRoot, "System32", "curl.exe")
	decoyDir := filepath.Join(root, "ambient")
	for _, dir := range []string{toolDir, filepath.Dir(nativeCurl), decoyDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	marker := func(name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("path-selection marker, never executed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	marker(filepath.Join(decoyDir, "curl.exe"))
	t.Setenv("PATH", decoyDir)
	t.Setenv("SystemRoot", systemRoot)
	want := nativeCurl
	switch scenario {
	case "existing_git_layout":
		marker(gitCurl)
		marker(nativeCurl)
		want = gitCurl
	case "native_curl_without_mingw64":
		marker(nativeCurl)
	case "invalid_git_candidate":
		if err := os.MkdirAll(gitCurl, 0o700); err != nil {
			t.Fatal(err)
		}
		marker(nativeCurl)
	case "invalid_native_candidate":
		if err := os.MkdirAll(nativeCurl, 0o700); err != nil {
			t.Fatal(err)
		}
	case "relative_system_root":
		t.Setenv("SystemRoot", "relative")
	case "no_trusted_candidate":
	default:
		t.Fatalf("unknown fixture probe scenario %q", scenario)
	}
	got := codexIntentTool(t, "curl", toolDir)
	if scenario != "existing_git_layout" && scenario != "native_curl_without_mingw64" {
		// Returning successfully is a failure for the parent's negative probe.
		return
	}
	canonical, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.ToSlash(canonical) {
		t.Fatalf("selected %q, want %q", got, filepath.ToSlash(canonical))
	}
}

func TestCodexWindowsNativeCurlFallbackRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("requires the real native Windows curl process")
	}
	// Resolve a real OS executable against a Git layout without mingw64.
	// Unlike the selection markers above, this delegate is actually executed.
	curl := codexIntentTool(t, "curl", filepath.Join(t.TempDir(), "git", "usr", "bin"))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/probe" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("fixture-native-curl\n"))
	}))
	defer server.Close()
	cmd := exec.Command(curl, "--disable", "--noproxy", "*", "--proxy", "", "--proto", "=http", "--globoff", "--max-time", "3", "-sf", server.URL+"/probe")
	cmd.Dir = t.TempDir()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("native curl: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.String() != "fixture-native-curl\n" || stderr.Len() != 0 || requests.Load() != 1 {
		t.Fatalf("native curl stdout=%q stderr=%q requests=%d", stdout.String(), stderr.String(), requests.Load())
	}
}

func TestCodexWindowsOtherFixtureToolsPreserveSelection(t *testing.T) {
	for _, name := range []string{"bash", "jq", "cat", "dirname", "chmod", "date", "touch"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			candidate := filepath.Join(dir, name+".exe")
			// Only the resolver is called; these marker files are not launched.
			if err := os.WriteFile(candidate, []byte("selection marker\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SystemRoot", "relative")
			if name == "bash" || name == "jq" {
				t.Setenv("CODEX_TEST_TRUSTED_"+strings.ToUpper(name), candidate)
			}
			got := codexIntentTool(t, name, dir)
			canonical, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if got != filepath.ToSlash(canonical) {
				t.Fatalf("selected %q, want %q", got, filepath.ToSlash(canonical))
			}
		})
	}
}
