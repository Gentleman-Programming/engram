package plugin_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCodexUnixSessionEndAdapter(t *testing.T) {
	bashPath := codexTestBash(t)
	requireCodexUnixTools(t, bashPath)
	adapterPath := filepath.Join(repoRoot(t), "plugin", "codex", "scripts", "session-end.sh")

	requests := make(chan string, 8)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/project/current" {
			_, _ = io.WriteString(w, `{"project":"end-project","project_source":"config"}`)
			return
		}
		var payload struct{ ID, Project, Directory string }
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Directory == "" || payload.Project != "end-project" {
			t.Error("end lost canonical project or cwd")
		}
		requests <- r.Method + " " + r.URL.EscapedPath() + " " + payload.ID
		if payload.ID == "slow" {
			<-r.Context().Done()
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":%q,"status":"ended"}`, payload.ID)
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	port := strings.TrimPrefix(listener.Addr().String(), "127.0.0.1:")

	t.Run("posts to a valid custom port", func(t *testing.T) {
		assertSilentCodexUnixSessionEnd(t, bashPath, adapterPath, `{"session_id":"custom-port"}`, &port, "")
		assertCodexUnixRequest(t, requests, "POST /runtime-sessions/end custom-port")
	})

	t.Run("preserves reserved session ID in JSON rather than URL path", func(t *testing.T) {
		assertSilentCodexUnixSessionEnd(t, bashPath, adapterPath, `{"session_id":"session id/with?characters#%"}`, &port, "")
		assertCodexUnixRequest(t, requests, "POST /runtime-sessions/end session id/with?characters#%")
	})

	t.Run("bounds an unresponsive HTTP request", func(t *testing.T) {
		assertSilentCodexUnixSessionEnd(t, bashPath, adapterPath, `{"session_id":"slow"}`, &port, "")
		assertCodexUnixRequest(t, requests, "POST /runtime-sessions/end slow")
	})

	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "empty input", input: ""},
		{name: "malformed input", input: "{"},
		{name: "missing session ID", input: `{}`},
		{name: "empty session ID", input: `{"session_id":""}`},
		{name: "empty cwd", input: `{"session_id":"id","cwd":""}`},
		{name: "numeric session ID", input: `{"session_id":42}`},
		{name: "boolean session ID", input: `{"session_id":true}`},
		{name: "array session ID", input: `{"session_id":[]}`},
		{name: "object session ID", input: `{"session_id":{}}`},
	} {
		t.Run(tc.name+" makes no request", func(t *testing.T) {
			assertSilentCodexUnixSessionEnd(t, bashPath, adapterPath, tc.input, &port, "")
			assertNoCodexUnixRequest(t, requests)
		})
	}

	for _, tc := range []struct {
		name string
		port string
	}{
		{name: "nonnumeric", port: "invalid"},
		{name: "URL authority syntax", port: "80@host.example"},
		{name: "out of range", port: "65536"},
		{name: "zero", port: "0"},
	} {
		t.Run("rejects "+tc.name+" port before curl", func(t *testing.T) {
			if got := codexFixtureEndpoint(t, &tc.port); got != "" {
				t.Fatalf("invalid port selected endpoint %q", got)
			}
		})
	}

	for _, tc := range []struct {
		name string
		port *string
	}{
		{name: "missing", port: nil},
		{name: "blank", port: stringPointer("")},
	} {
		t.Run(tc.name+" port defaults to 7437 without binding it", func(t *testing.T) {
			if got := codexFixtureEndpoint(t, tc.port); got != "http://127.0.0.1:7437" {
				t.Fatalf("default endpoint = %q", got)
			}
		})
	}

	t.Run("fails open when the API closes without a response", func(t *testing.T) {
		failureListener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen for failed response: %v", err)
		}
		defer failureListener.Close()
		failurePort := strings.TrimPrefix(failureListener.Addr().String(), "127.0.0.1:")
		accepted := make(chan struct{})
		go func() {
			connection, acceptErr := failureListener.Accept()
			if acceptErr == nil {
				close(accepted)
				_ = connection.Close()
			}
		}()

		assertSilentCodexUnixSessionEnd(t, bashPath, adapterPath, `{"session_id":"id"}`, &failurePort, "")
		select {
		case <-accepted:
		case <-time.After(3 * time.Second):
			t.Fatal("adapter did not connect to the failure listener")
		}
	})
}

func codexTestBash(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		path, err := exec.LookPath("bash")
		if err != nil {
			t.Fatalf("find bash: %v", err)
		}
		return path
	}

	gitPath, err := exec.LookPath("git.exe")
	if err != nil {
		t.Fatalf("find Git for Windows: %v", err)
	}
	path, err := codexTestBashCandidate(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func codexTestBashCandidate(gitPath string) (string, error) {
	gitDir := filepath.Dir(gitPath)
	// git.exe can live in cmd, bin, or mingw64/bin depending on installation.
	for _, candidate := range []string{
		filepath.Join(gitDir, "bash.exe"),
		filepath.Join(gitDir, "..", "bin", "bash.exe"),
		filepath.Join(gitDir, "..", "..", "bin", "bash.exe"),
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return filepath.Clean(candidate), nil
		}
	}
	return "", fmt.Errorf("find Git Bash near git.exe at %s", gitPath)
}

func TestCodexTestBashCandidates(t *testing.T) {
	for _, tc := range []struct {
		name, gitDir, bashPath string
		directory, missing     bool
	}{
		{name: "cmd sibling bin", gitDir: "cmd", bashPath: "bin/bash.exe"},
		{name: "bin colocated", gitDir: "bin", bashPath: "bin/bash.exe"},
		{name: "mingw64 sibling bin", gitDir: "mingw64/bin", bashPath: "bin/bash.exe"},
		{name: "reject directory", gitDir: "cmd", bashPath: "bin/bash.exe", directory: true, missing: true},
		{name: "no candidate", gitDir: "cmd", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			gitPath := filepath.Join(root, filepath.FromSlash(tc.gitDir), "git.exe")
			if tc.bashPath != "" {
				candidate := filepath.Join(root, filepath.FromSlash(tc.bashPath))
				if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
					t.Fatal(err)
				}
				if tc.directory {
					if err := os.Mkdir(candidate, 0o755); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(candidate, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := codexTestBashCandidate(gitPath)
			if tc.missing {
				if got != "" || err == nil || err.Error() != "find Git Bash near git.exe at "+gitPath {
					t.Fatalf("expected no-candidate contract, got %q, %v", got, err)
				}
				return
			}
			want := filepath.Join(root, filepath.FromSlash(tc.bashPath))
			if err != nil || got != want {
				t.Fatalf("candidate = %q, %v; want %q", got, err, want)
			}
		})
	}
}

func requireCodexUnixTools(t *testing.T, bashPath string) {
	t.Helper()
	check := exec.Command(bashPath, "--noprofile", "--norc", "-c", "command -v jq >/dev/null && command -v curl >/dev/null")
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("Unix SessionEnd runtime tests require jq and curl: %v: %s", err, output)
	}
}

func assertSilentCodexUnixSessionEnd(t *testing.T, bashPath, adapterPath, input string, port *string, pathPrefix string) {
	t.Helper()
	cwd := t.TempDir()
	var payload map[string]any
	if json.Unmarshal([]byte(input), &payload) == nil && payload != nil {
		if _, present := payload["cwd"]; !present {
			payload["cwd"] = cwd
		}
		data, _ := json.Marshal(payload)
		input = string(data)
	}
	run := exec.Command(bashPath, adapterPath)
	run.Dir = cwd
	run.Env = codexHandoffEnv(t, cwd, "http://127.0.0.1:"+*port, buildCodexFixtureCLI(t))
	run.Stdin = strings.NewReader(input)
	var stdout, stderr strings.Builder
	run.Stdout = &stdout
	run.Stderr = &stderr
	started := time.Now()
	err := run.Run()
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("closure exceeded two seconds: %s", elapsed)
	}
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("run Unix adapter: %v", err)
		}
	}
	if code != 0 || stdout.String() != "" || stderr.String() != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want silent exit 0", code, stdout.String(), stderr.String())
	}
}

func assertCodexUnixRequest(t *testing.T, requests <-chan string, want string) {
	t.Helper()
	select {
	case got := <-requests:
		if got != want {
			t.Errorf("request = %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("adapter did not post to the loopback server")
	}
}

func assertNoCodexUnixRequest(t *testing.T, requests <-chan string) {
	t.Helper()
	select {
	case got := <-requests:
		t.Fatalf("unexpected request %q", got)
	default:
	}
}

func codexFixtureEndpoint(t *testing.T, port *string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "cmd", "engram", "hook_codex.go"))
	if err != nil {
		t.Fatal(err)
	}
	_, body, found := strings.Cut(strings.ReplaceAll(string(data), "\r\n", "\n"), "func codexHookURL() string {")
	body, _, ended := strings.Cut(body, "\n}\n")
	if !found || !ended {
		t.Fatal("endpoint selector boundary changed")
	}
	dir := t.TempDir()
	source := "package main\nimport (\"fmt\";\"os\";\"strconv\";\"strings\")\nfunc codexHookURL() string {" + body + "\n}\nfunc main(){fmt.Print(codexHookURL())}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "endpoint.exe")
	build := exec.Command("go", "build", "-o", binary, filepath.Join(dir, "main.go"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build endpoint probe: %v: %s", err, output)
	}
	run := exec.Command(binary)
	run.Env = []string{"ENGRAM_URL=", "ENGRAM_SOCKET="}
	if port != nil {
		run.Env = append(run.Env, "ENGRAM_PORT="+*port)
	}
	output, err := run.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}
