package plugin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodexUnixUserPromptSubmitMarkerDirectory(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "plugin", "codex", "scripts", "user-prompt-submit.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var assignment string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "STATE_FILE=") {
			assignment = line
		}
	}
	if assignment == "" {
		t.Fatal("missing marker assignment")
	}
	for _, dir := range []string{filepath.ToSlash(t.TempDir()), ""} {
		cmd := exec.Command(codexTestBash(t), "--noprofile", "--norc", "-c", assignment+`; [ "$STATE_FILE" = "${TMPDIR:-/tmp}/$SESSION_KEY" ] || exit 42; printf '%s' "$STATE_FILE"`)
		cmd.Dir = t.TempDir()
		cmd.Env = []string{"TMPDIR=" + dir, "SESSION_KEY=quoted ' marker"}
		output, err := cmd.Output()
		want := dir
		if want == "" {
			want = "/tmp"
		}
		if err != nil || !strings.HasSuffix(string(output), "/quoted ' marker") {
			t.Fatalf("marker path=%q error=%v, want owned/default directory %q", output, err, want)
		}
	}
}

func TestCodexUnixUserPromptSubmitValidatesObservationsAndFirstSaveThreshold(t *testing.T) {
	bashPath := codexTestBash(t)
	requireCodexUnixTools(t, bashPath)
	adapterPath := filepath.Join(repoRoot(t), "plugin", "codex", "scripts", "user-prompt-submit.sh")
	now := time.Now().UTC()

	tests := []struct {
		name         string
		observations string
		sessionAge   time.Duration
		timezone     string
		wantNudge    bool
	}{
		{name: "first save before 15 minutes", observations: "[]", sessionAge: 10 * time.Minute, wantNudge: false},
		{name: "first save after 15 minutes", observations: "[]", sessionAge: 20 * time.Minute, wantNudge: true},
		{name: "naive UTC stale timestamp under EST5", observations: fmt.Sprintf(`[{"created_at":%q}]`, now.Add(-20*time.Minute).Format(time.DateTime)), sessionAge: 20 * time.Minute, timezone: "EST5", wantNudge: true},
		{name: "recent UTC timestamp under JST-9", observations: fmt.Sprintf(`[{"created_at":%q}]`, now.Add(-5*time.Minute).Format(time.RFC3339)), sessionAge: 20 * time.Minute, timezone: "JST-9", wantNudge: false},
		{name: "malformed JSON", observations: "[{", sessionAge: 20 * time.Minute, wantNudge: false},
		{name: "non-array JSON", observations: `{}`, sessionAge: 20 * time.Minute, wantNudge: false},
		{name: "non-empty array without timestamp", observations: `[{}]`, sessionAge: 20 * time.Minute, wantNudge: false},
		{name: "non-empty array with null timestamp", observations: `[{"created_at":null}]`, sessionAge: 20 * time.Minute, wantNudge: false},
		{name: "non-empty array with non-string timestamp", observations: `[{"created_at":42}]`, sessionAge: 20 * time.Minute, wantNudge: false},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := fmt.Sprintf("nudge-%d-%d", time.Now().UnixNano(), index)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					_, _ = io.WriteString(w, `{"status":"ok","service":"engram","capabilities":{"runtime_session_resolution":true}}`)
				case "/project/current":
					_, _ = io.WriteString(w, `{"project":"test-project","project_source":"config"}`)
				case "/runtime-sessions/resolve":
					var request struct {
						ID string `json:"id"`
					}
					if json.NewDecoder(r.Body).Decode(&request) != nil || request.ID != sessionID || r.Method != http.MethodPost {
						t.Error("invalid no-create resolution")
						http.Error(w, "invalid", http.StatusBadRequest)
						return
					}
					_, _ = fmt.Fprintf(w, `{"id":%q,"status":"resolved"}`, sessionID)
				case "/sessions":
					t.Error("prompt must not register a session")
					http.Error(w, "no registration", http.StatusConflict)
				case "/sessions/" + sessionID:
					_, _ = fmt.Fprintf(w, `{"id":%q,"project":"test-project","ownership_mode":"project_owned","started_at":%q}`, sessionID, now.Add(-tt.sessionAge).Format(time.DateTime))
				case "/observations":
					_, _ = io.WriteString(w, tt.observations)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
			env := append(codexPromptTestEnv(t, port), "TZ="+tt.timezone)
			runHook := func() string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, bashPath, adapterPath)
				cmd.Dir = t.TempDir()
				cmd.Env = env
				cwd := t.TempDir()
				codexFixtureProject(t, cwd, "test-project")
				cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"cwd":%q,"session_id":%q}`, cwd, sessionID))
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("run Codex user prompt hook: %v; output: %s", err, output)
				}
				if !json.Valid(output) {
					t.Fatalf("Codex user prompt hook emitted invalid JSON %q", output)
				}
				return string(output)
			}

			runHook()
			output := runHook()
			if gotNudge := strings.Contains(output, "MEMORY REMINDER"); gotNudge != tt.wantNudge {
				t.Fatalf("second hook invocation nudge = %t, want %t; output: %q", gotNudge, tt.wantNudge, output)
			}
		})
	}
}

func TestCodexUnixUserPromptSubmitDetachesPromptPersistencePipes(t *testing.T) {
	bashPath := codexTestBash(t)
	requireCodexUnixTools(t, bashPath)
	adapterPath := filepath.Join(repoRoot(t), "plugin", "codex", "scripts", "user-prompt-submit.sh")

	sessionID := "pipe-test-" + time.Now().Format("150405.000000000")
	postStarted := make(chan struct{})
	releasePost := make(chan struct{})
	var postOnce sync.Once
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = io.WriteString(w, `{"status":"ok","service":"engram","capabilities":{"runtime_session_resolution":true}}`)
		case "/project/current":
			_, _ = io.WriteString(w, `{"project":"test-project","project_source":"config"}`)
		case "/sessions":
			t.Error("prompt must not register a session")
			http.Error(w, "no registration", http.StatusConflict)
		case "/runtime-sessions/resolve":
			var request struct {
				ID string `json:"id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.ID != sessionID || r.Method != http.MethodPost {
				t.Errorf("invalid session registration: method=%s id=%q err=%v", r.Method, request.ID, err)
				http.Error(w, "invalid registration", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"id":%q,"status":"resolved"}`, sessionID)
		case "/sessions/" + sessionID:
			http.NotFound(w, r)
		case "/prompts":
			postOnce.Do(func() { close(postStarted) })
			<-releasePost
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})}
	defer func() {
		close(releasePost)
		_ = server.Close()
	}()
	go func() { _ = server.Serve(listener) }()
	port := strings.TrimPrefix(listener.Addr().String(), "127.0.0.1:")

	inputReader, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	defer inputWriter.Close()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	defer stdoutReader.Close()
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	defer stderrReader.Close()

	run := exec.Command(bashPath, adapterPath)
	run.Dir = t.TempDir()
	run.Env = codexPromptTestEnv(t, port)
	run.Stdin = inputReader
	run.Stdout = stdoutWriter
	run.Stderr = stderrWriter
	if err := run.Start(); err != nil {
		t.Fatalf("start adapter: %v", err)
	}
	_ = inputReader.Close()
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	cwd := t.TempDir()
	codexFixtureProject(t, cwd, "test-project")
	if _, err := io.WriteString(inputWriter, fmt.Sprintf(`{"cwd":%q,"session_id":%q,"prompt":"capture this"}`, cwd, sessionID)); err != nil {
		t.Fatalf("write hook input: %v", err)
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatalf("close hook input: %v", err)
	}

	type hookOutput struct {
		stdout []byte
		stderr []byte
	}
	outputDone := make(chan hookOutput, 1)
	go func() {
		stdout, _ := io.ReadAll(stdoutReader)
		stderr, _ := io.ReadAll(stderrReader)
		outputDone <- hookOutput{stdout: stdout, stderr: stderr}
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- run.Wait() }()

	select {
	case <-postStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("prompt persistence did not reach the delayed loopback server")
	}

	select {
	case output := <-outputDone:
		if !json.Valid(output.stdout) {
			t.Fatalf("stdout is not valid hook JSON: %q", output.stdout)
		}
		if string(output.stderr) != "" {
			t.Fatalf("stderr = %q, want immediate EOF without output", output.stderr)
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("stdout or stderr remained open while the delayed prompt POST was in flight")
	}

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("adapter exit: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground hook did not complete before the delayed POST response")
	}
}

func TestCodexUnixUserPromptSubmitDetachesPromptPersistenceStdin(t *testing.T) {
	testCodexPromptPersistenceStdin(t, false)
}

func TestCodexUnixUserPromptSubmitStdinProbeWaitsForStartup(t *testing.T) {
	testCodexPromptPersistenceStdin(t, true)
}

func testCodexPromptPersistenceStdin(t *testing.T, delayStartup bool) {
	t.Helper()
	bashPath := codexTestBash(t)
	requireCodexUnixTools(t, bashPath)
	adapterPath := filepath.Join(repoRoot(t), "plugin", "codex", "scripts", "user-prompt-submit.sh")
	binDir := t.TempDir()
	codexFixtureProject(t, binDir, "test-project")
	markerPath := filepath.Join(binDir, "stdin-result")
	readyPath := filepath.Join(binDir, "stdin-ready")
	releasePath := filepath.Join(binDir, "stdin-release")
	// Preserve the deterministic publication regression from #2072.
	defer func() { _ = os.WriteFile(releasePath, nil, 0o600) }()
	writeCodexPromptProbeCommand(t, filepath.Join(binDir, "cat"), "#!/bin/bash\nprintf '%s' "+"'"+fmt.Sprintf(`{"cwd":%q,"session_id":"stdin-pipe-test","prompt":"capture this"}`, filepath.ToSlash(binDir))+"'\n")
	writeCodexPromptProbeCommand(t, filepath.Join(binDir, "curl"), "#!/bin/bash\ncase \"$*\" in\n  *'/project/current'*) printf '%s' '{\"project\":\"test-project\",\"project_source\":\"config\"}' ;;\n  *'/sessions'*) printf '%s\\n201' '{\"id\":\"stdin-pipe-test\",\"status\":\"created\"}' ;;\n  *'/prompts'*) "+codexPromptStdinProbe+" ;;\n  *) exit 0 ;;\nesac\n")

	if delayStartup {
		// Builtins only: the fixture deliberately excludes ambient executables.
		path := filepath.Join(binDir, "curl")
		probe, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeCodexPromptProbeCommand(t, path, strings.Replace(string(probe), "printf '%s' '{\"project\"", "start=$SECONDS; while (( SECONDS - start < 3 )); do :; done; printf '%s' '{\"project\"", 1))
	}

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	run := exec.Command(bashPath, "-c", `PATH="$(convert "$1"):$PATH"; export PATH; "$2"`, "codex-test", binDir, adapterPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = io.WriteString(w, `{"status":"ok","service":"engram","capabilities":{"runtime_session_resolution":true}}`)
		case "/project/current":
			_, _ = io.WriteString(w, `{"project":"test-project","project_source":"config"}`)
		case "/sessions/stdin-pipe-test":
			http.NotFound(w, r)
		case "/runtime-sessions/resolve":
			_, _ = io.WriteString(w, `{"id":"stdin-pipe-test","status":"resolved"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	run.Dir = binDir
	run.Env = append(codexPromptTestEnv(t, strings.TrimPrefix(server.URL, "http://127.0.0.1:")), "PROMPT_STDIN_MARKER="+markerPath, "PROMPT_STDIN_READY="+readyPath, "PROMPT_STDIN_RELEASE="+filepath.ToSlash(releasePath))
	run.Stdin = stdinReader
	if err := run.Start(); err != nil {
		_ = stdinWriter.Close()
		t.Fatalf("start adapter: %v", err)
	}
	_ = stdinReader.Close()

	waitDone := make(chan error, 1)
	go func() { waitDone <- run.Wait() }()
	waited := false
	defer func() {
		_ = stdinWriter.Close()
		if !waited {
			_ = run.Process.Kill()
			<-waitDone
		}
	}()

	if err := observeCodexPromptStdin(readyPath, markerPath, releasePath, 10*time.Second, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waitDone:
		waited = true
		if err != nil {
			t.Fatalf("run adapter: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("adapter did not exit after the stdin probe")
	}
}

// Announce readiness immediately before the actual read, not at adapter launch.
// A read timeout is never used as a substitute for EOF.
const codexPromptStdinProbe = `printf ready > "$PROMPT_STDIN_READY"
if IFS= read -r input || [ -n "$input" ]; then result=data; else result=eof; fi
exec 3> "$PROMPT_STDIN_MARKER"
if [[ -n "${PROMPT_STDIN_RELEASE:-}" ]]; then
  # Hold publication until the observer has sampled the empty file.
  while [[ -d "${PROMPT_STDIN_RELEASE%/*}" && ! -f "$PROMPT_STDIN_RELEASE" ]]; do :; done
  [[ -f "$PROMPT_STDIN_RELEASE" ]] || exit 1
fi
printf '%s' "$result" >&3
exec 3>&-`

func observeCodexPromptStdin(readyPath, resultPath, releasePath string, startupBudget, readBudget time.Duration) error {
	if _, err := waitCodexProbeMarker(readyPath, startupBudget, false); err != nil {
		return fmt.Errorf("stdin probe did not become ready: %w", err)
	}
	deadline := time.Now().Add(readBudget)
	if releasePath != "" {
		marker, err := waitCodexProbeMarker(resultPath, time.Until(deadline), true)
		if err != nil {
			return fmt.Errorf("stdin probe did not open publication marker: %w", err)
		}
		if len(marker) != 0 {
			return fmt.Errorf("probe published before release: %q", marker)
		}
		if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
			return fmt.Errorf("release stdin probe publication: %w", err)
		}
	}
	result, err := waitCodexProbeMarker(resultPath, time.Until(deadline), false)
	if err != nil {
		return fmt.Errorf("stdin probe did not finish after readiness: %w", err)
	}
	_, err = codexPromptStdinProbeResult(result)
	return err
}

func waitCodexProbeMarker(path string, budget time.Duration, allowEmpty bool) ([]byte, error) {
	deadline := time.Now().Add(budget)
	for {
		value, err := os.ReadFile(path)
		// Opening a marker precedes writing it; an empty file is not a result.
		if err == nil && (allowEmpty || len(value) > 0) {
			return value, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for %s", filepath.Base(path))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCodexPromptStdinProbeRejectsInheritedInput(t *testing.T) {
	for _, tc := range []struct {
		name, input, wantError string
		closeInput             bool
	}{
		{name: "EOF", closeInput: true},
		{name: "data", input: "unexpected\n", wantError: `detached curl stdin = "data", want EOF from /dev/null`},
		{name: "unterminated data", input: "unexpected", closeInput: true, wantError: `detached curl stdin = "data", want EOF from /dev/null`},
		{name: "open pipe", wantError: "stdin probe did not finish after readiness:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			ready, result := filepath.Join(root, "ready"), filepath.Join(root, "result")
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			if tc.input != "" {
				if _, err := io.WriteString(writer, tc.input); err != nil {
					t.Fatal(err)
				}
			}
			if tc.closeInput {
				_ = writer.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			run := exec.CommandContext(ctx, codexTestBash(t), "-c", codexPromptStdinProbe)
			run.Env = append(codexHandoffEnv(t, root, "http://127.0.0.1:1"), "PROMPT_STDIN_READY="+ready, "PROMPT_STDIN_MARKER="+result)
			run.Stdin = reader
			var stdout, stderr strings.Builder
			run.Stdout, run.Stderr = &stdout, &stderr
			if err := run.Start(); err != nil {
				t.Fatal(err)
			}
			probeErr := observeCodexPromptStdin(ready, result, "", 10*time.Second, time.Second)
			if tc.name == "open pipe" {
				if _, err := os.Stat(result); !os.IsNotExist(err) {
					t.Errorf("open pipe published a result before EOF: %v", err)
				}
			}
			// Release a deliberately blocked read only after observing the failure.
			_ = writer.Close()
			if err := run.Wait(); err != nil {
				t.Fatalf("probe exit: %v", err)
			}
			if stdout.String() != "" || stderr.String() != "" {
				t.Fatalf("probe output stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if tc.wantError == "" {
				if probeErr != nil {
					t.Fatal(probeErr)
				}
			} else if probeErr == nil || !strings.HasPrefix(probeErr.Error(), tc.wantError) {
				t.Fatalf("probe error = %v, want %q", probeErr, tc.wantError)
			}
		})
	}
}

func TestCodexPromptStdinProbeResult(t *testing.T) {
	for _, tc := range []struct {
		name      string
		marker    string
		complete  bool
		wantError bool
	}{
		{name: "created but unpublished", marker: ""},
		{name: "detached EOF", marker: "eof", complete: true},
		{name: "inherited data", marker: "data", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			complete, err := codexPromptStdinProbeResult([]byte(tc.marker))
			if complete != tc.complete || (err != nil) != tc.wantError {
				t.Fatalf("probe result = (%t, %v), want (%t, error=%t)", complete, err, tc.complete, tc.wantError)
			}
			if tc.wantError && err.Error() != `detached curl stdin = "data", want EOF from /dev/null` {
				t.Fatalf("probe error = %q", err)
			}
		})
	}
}

func codexPromptStdinProbeResult(marker []byte) (bool, error) {
	// Redirection creates the file before printf publishes the probe result.
	if len(marker) == 0 {
		return false, nil
	}
	if string(marker) != "eof" {
		return false, fmt.Errorf("detached curl stdin = %q, want EOF from /dev/null", marker)
	}
	return true, nil
}

func writeCodexPromptProbeCommand(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write probe command %s: %v", path, err)
	}
}

func codexPromptTestEnv(t *testing.T, port string) []string {
	t.Helper()
	return codexHandoffEnv(t, t.TempDir(), "http://127.0.0.1:"+port, buildCodexFixtureCLI(t))
}
