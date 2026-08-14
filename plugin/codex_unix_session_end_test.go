package plugin_test

import (
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
		requests <- r.Method + " " + r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	port := strings.TrimPrefix(listener.Addr().String(), "127.0.0.1:")

	t.Run("posts to a valid custom port", func(t *testing.T) {
		assertSilentCodexUnixSessionEnd(t, bashPath, adapterPath, `{"session_id":"custom-port"}`, &port, "")
		assertCodexUnixRequest(t, requests, "POST /sessions/custom-port/end")
	})

	t.Run("percent-encodes a reserved session ID", func(t *testing.T) {
		assertSilentCodexUnixSessionEnd(t, bashPath, adapterPath, `{"session_id":"session id/with?characters#%"}`, &port, "")
		assertCodexUnixRequest(t, requests, "POST /sessions/session%20id%2Fwith%3Fcharacters%23%25/end")
	})

	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "empty input", input: ""},
		{name: "malformed input", input: "{"},
		{name: "missing session ID", input: `{}`},
		{name: "empty session ID", input: `{"session_id":""}`},
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
			fakeCurlDir := writeRecordingCurl(t)
			assertSilentCodexUnixSessionEnd(t, bashPath, adapterPath, `{"session_id":"id"}`, &tc.port, fakeCurlDir)
			if _, err := os.Stat(filepath.Join(fakeCurlDir, "args")); !os.IsNotExist(err) {
				t.Fatalf("curl must not run for port %q", tc.port)
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
			fakeCurlDir := writeRecordingCurl(t)
			assertSilentCodexUnixSessionEnd(t, bashPath, adapterPath, `{"session_id":"id"}`, tc.port, fakeCurlDir)
			args, err := os.ReadFile(filepath.Join(fakeCurlDir, "args"))
			if err != nil {
				t.Fatalf("read recorded curl arguments: %v", err)
			}
			if !strings.Contains(string(args), "http://127.0.0.1:7437/sessions/id/end") {
				t.Fatalf("curl arguments do not use default port 7437: %q", args)
			}
			if !strings.Contains(string(args), "--max-time\n2\n") {
				t.Fatalf("curl arguments do not set a two-second timeout: %q", args)
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
	bashPath := filepath.Clean(filepath.Join(filepath.Dir(gitPath), "..", "bin", "bash.exe"))
	if _, err := os.Stat(bashPath); err != nil {
		t.Fatalf("find Git Bash at %s: %v", bashPath, err)
	}
	return bashPath
}

func requireCodexUnixTools(t *testing.T, bashPath string) {
	t.Helper()
	check := exec.Command(bashPath, "-lc", "command -v jq >/dev/null && command -v curl >/dev/null")
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("Unix SessionEnd runtime tests require jq and curl: %v: %s", err, output)
	}
}

func assertSilentCodexUnixSessionEnd(t *testing.T, bashPath, adapterPath, input string, port *string, pathPrefix string) {
	t.Helper()
	var run *exec.Cmd
	if pathPrefix == "" {
		run = exec.Command(bashPath, adapterPath)
	} else {
		run = exec.Command(bashPath, "-c", `PATH="$1:$PATH"; export PATH; "$2"`, "codex-test", pathPrefix, adapterPath)
	}
	run.Env = make([]string, 0, len(os.Environ())+1)
	for _, env := range os.Environ() {
		upper := strings.ToUpper(env)
		if strings.HasPrefix(upper, "ENGRAM_PORT=") {
			continue
		}
		run.Env = append(run.Env, env)
	}
	if port != nil {
		run.Env = append(run.Env, "ENGRAM_PORT="+*port)
	}
	run.Stdin = strings.NewReader(input)
	var stdout, stderr strings.Builder
	run.Stdout = &stdout
	run.Stderr = &stderr
	err := run.Run()
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

func writeRecordingCurl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "curl")
	content := "#!/bin/bash\nprintf '%s\\n' \"$@\" > \"$(dirname \"$0\")/args\"\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write recording curl: %v", err)
	}
	return dir
}
