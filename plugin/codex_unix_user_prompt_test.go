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
				case "/project/current":
					_, _ = io.WriteString(w, `{"project":"test-project","project_source":"config"}`)
				case "/sessions/" + sessionID:
					_, _ = fmt.Fprintf(w, `{"started_at":%q}`, now.Add(-tt.sessionAge).Format(time.DateTime))
				case "/observations":
					_, _ = io.WriteString(w, tt.observations)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
			env := append(codexPromptTestEnv(port), "TZ="+tt.timezone)
			runHook := func() string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, bashPath, adapterPath)
				cmd.Env = env
				cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"cwd":"/tmp/test","session_id":%q}`, sessionID))
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

	postStarted := make(chan struct{})
	releasePost := make(chan struct{})
	var postOnce sync.Once
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/project/current":
			_, _ = io.WriteString(w, `{"project":"test-project","project_source":"config"}`)
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
	run.Env = codexPromptTestEnv(port)
	run.Stdin = inputReader
	run.Stdout = stdoutWriter
	run.Stderr = stderrWriter
	if err := run.Start(); err != nil {
		t.Fatalf("start adapter: %v", err)
	}
	_ = inputReader.Close()
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	if _, err := io.WriteString(inputWriter, `{"cwd":"/tmp/test","session_id":"pipe-test-`+time.Now().Format("150405.000000000")+`","prompt":"capture this"}`); err != nil {
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
	bashPath := codexTestBash(t)
	requireCodexUnixTools(t, bashPath)
	adapterPath := filepath.Join(repoRoot(t), "plugin", "codex", "scripts", "user-prompt-submit.sh")
	binDir := t.TempDir()
	markerPath := filepath.Join(binDir, "stdin-result")
	writeCodexPromptProbeCommand(t, filepath.Join(binDir, "cat"), "#!/bin/bash\nprintf '%s' '{\"cwd\":\"/tmp/test\",\"session_id\":\"stdin-pipe-test\",\"prompt\":\"capture this\"}'\n")
	writeCodexPromptProbeCommand(t, filepath.Join(binDir, "curl"), "#!/bin/bash\ncase \"$*\" in\n  *'/project/current'*) printf '%s' '{\"project\":\"test-project\",\"project_source\":\"config\"}' ;;\n  *'/prompts'*) if IFS= read -r _; then printf data > \"$PROMPT_STDIN_MARKER\"; else printf eof > \"$PROMPT_STDIN_MARKER\"; fi ;;\n  *) exit 0 ;;\nesac\n")

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	run := exec.Command(bashPath, "-c", `PATH="$1:$PATH"; export PATH; "$2"`, "codex-test", binDir, adapterPath)
	run.Env = append(codexPromptTestEnv("7437"), "PROMPT_STDIN_MARKER="+markerPath)
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

	deadline := time.Now().Add(time.Second)
	for {
		marker, err := os.ReadFile(markerPath)
		if err == nil {
			if string(marker) != "eof" {
				t.Fatalf("detached curl stdin = %q, want EOF from /dev/null", marker)
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
			return
		}
		if !os.IsNotExist(err) {
			t.Fatalf("read stdin probe result: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("prompt persistence did not finish the stdin probe")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func writeCodexPromptProbeCommand(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write probe command %s: %v", path, err)
	}
}

func codexPromptTestEnv(port string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(item), "ENGRAM_PORT=") {
			continue
		}
		env = append(env, item)
	}
	return append(env, "ENGRAM_PORT="+port)
}
