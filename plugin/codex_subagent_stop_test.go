package plugin_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodexSubagentStopAlwaysEmitsHookEnvelope(t *testing.T) {
	bash := codexTestBash(t)
	requireCodexUnixTools(t, bash)
	script := repoRoot(t) + "/plugin/codex/scripts/subagent-stop.sh"
	missingCWD := filepath.Join(t.TempDir(), "missing")
	inputWithMissingCWD, err := json.Marshal(struct {
		CWD                  string `json:"cwd"`
		LastAssistantMessage string `json:"last_assistant_message"`
	}{
		CWD:                  missingCWD,
		LastAssistantMessage: "captured",
	})
	if err != nil {
		t.Fatalf("marshal test input: %v", err)
	}
	for _, input := range []string{"", `{}`, string(inputWithMissingCWD)} {
		run := exec.Command(bash, script)
		run.Stdin = strings.NewReader(input)
		var stdout, stderr bytes.Buffer
		run.Stdout = &stdout
		run.Stderr = &stderr
		err := run.Run()
		if err != nil {
			t.Fatalf("input %q: %v", input, err)
		}
		if stdout.String() != "{}\n" {
			t.Fatalf("input %q stdout=%q, want exactly a JSON envelope", input, stdout.String())
		}
		var envelope map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatalf("input %q emitted invalid JSON %q: %v", input, stdout.String(), err)
		}
	}

	requests := make(chan map[string]any, 1)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for controlled capture server: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/project/current":
			if r.URL.Query().Get("cwd") != repoRoot(t) {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"project":"codex-test-project","project_source":"git_root"}`))
		case "/observations/passive":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			requests <- payload
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	port := "ENGRAM_PORT=" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)

	missingRun := exec.Command(bash, script)
	missingRun.Env = appendFilteredCodexPort(port)
	missingRun.Stdin = bytes.NewReader(inputWithMissingCWD)
	var missingStdout, missingStderr bytes.Buffer
	missingRun.Stdout = &missingStdout
	missingRun.Stderr = &missingStderr
	if err := missingRun.Run(); err != nil {
		t.Fatalf("missing cwd: %v", err)
	}
	if missingStdout.String() != "{}\n" {
		t.Fatalf("missing cwd stdout=%q, want exactly a JSON envelope", missingStdout.String())
	}
	if !strings.Contains(missingStderr.String(), "unable to resolve project") {
		t.Fatalf("missing-cwd diagnostic stderr=%q", missingStderr.String())
	}

	normalInput, err := json.Marshal(struct {
		SessionID            string `json:"session_id"`
		CWD                  string `json:"cwd"`
		LastAssistantMessage string `json:"last_assistant_message"`
	}{"session-1", repoRoot(t), "completed normally"})
	if err != nil {
		t.Fatalf("marshal normal input: %v", err)
	}
	run := exec.Command(bash, script)
	run.Env = appendFilteredCodexPort(port)
	run.Stdin = bytes.NewReader(normalInput)
	var stdout, stderr bytes.Buffer
	run.Stdout = &stdout
	run.Stderr = &stderr
	if err := run.Run(); err != nil {
		t.Fatalf("normal completion: %v; stderr=%q", err, stderr.String())
	}
	if stdout.String() != "{}\n" {
		t.Fatalf("normal completion stdout=%q, want exactly a JSON envelope", stdout.String())
	}
	select {
	case payload := <-requests:
		if payload["content"] != "completed normally" || payload["project"] != "codex-test-project" {
			t.Fatalf("unexpected passive capture payload: %#v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("normal completion did not post passive capture")
	}
}

func TestCodexSubagentStopExplicitEmptyMessageSelection(t *testing.T) {
	bash := codexTestBash(t)
	requireCodexUnixTools(t, bash)
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skipf("jq unavailable: %v", err)
	}
	// Reject jq 1.7+ binary-mode flags, even when the installed jq accepts them.
	shimDir := t.TempDir()
	shim := "#!/usr/bin/env bash\nfor arg in \"$@\"; do\n  case \"$arg\" in -b|-bj|--binary) exit 97;; esac\ndone\nexec " + fmt.Sprintf("'%s'", strings.ReplaceAll(jq, "'", "'\\''")) + " \"$@\"\n"
	shimPath := filepath.Join(shimDir, "jq")
	if err := os.WriteFile(shimPath, []byte(shim), 0755); err != nil {
		t.Fatal(err)
	}
	rejected := exec.Command(bash, shimPath, "-bj", ".")
	if err := rejected.Run(); err == nil || rejected.ProcessState.ExitCode() != 97 {
		t.Fatalf("jq compatibility shim did not reject -bj: %v", err)
	}
	for _, tt := range []struct {
		name, primary, stdout, want string
	}{
		{"primary wins", "primary", "fallback", "primary"},
		{"primary beginning with dash b", "-bug", "fallback", "-bug"},
		{"empty primary falls back", "", "fallback", "fallback"},
		{"empty primary preserves trailing newline", "", "first\nsecond\n", "first\nsecond\n"},
		{"empty primary preserves CRLF", "", "first\r\nsecond\r\n", "first\r\nsecond\r\n"},
		{"both empty skip capture", "", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var captures []passiveCapture
			var capturesMu sync.Mutex
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/project/current":
					_, _ = w.Write([]byte(`{"project":"codex-test-project","project_source":"git_root"}`))
				case "/observations/passive":
					if r.Method != http.MethodPost {
						t.Errorf("unexpected method %s", r.Method)
					}
					var capture passiveCapture
					if err := json.NewDecoder(r.Body).Decode(&capture); err != nil {
						t.Errorf("decode POST: %v", err)
					}
					capturesMu.Lock()
					captures = append(captures, capture)
					capturesMu.Unlock()
				default:
					http.NotFound(w, r)
				}
			})}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close() })
			input, err := json.Marshal(struct {
				SessionID            string `json:"session_id"`
				CWD                  string `json:"cwd"`
				LastAssistantMessage string `json:"last_assistant_message"`
				Stdout               string `json:"stdout"`
			}{"explicit-fields", repoRoot(t), tt.primary, tt.stdout})
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bash, repoRoot(t)+"/plugin/codex/scripts/subagent-stop.sh")
			cmd.Env = append(appendFilteredCodexPort("ENGRAM_PORT="+strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)), "PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd.Stdin = bytes.NewReader(input)
			output, err := cmd.CombinedOutput()
			if err != nil || string(output) != "{}\n" {
				t.Fatalf("hook output = %q, err = %v", output, err)
			}
			capturesMu.Lock()
			defer capturesMu.Unlock()
			if tt.want == "" {
				if len(captures) != 0 {
					t.Fatalf("unexpected passive POSTs: %+v", captures)
				}
				return
			}
			if len(captures) != 1 || captures[0].Content != tt.want || captures[0].SessionID != "explicit-fields" || captures[0].Project != "codex-test-project" || captures[0].Source != "subagent-stop" {
				t.Fatalf("passive POSTs = %+v, want one capture of %q", captures, tt.want)
			}
		})
	}
}

func appendFilteredCodexPort(port string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(value), "ENGRAM_PORT=") {
			continue
		}
		env = append(env, value)
	}
	return append(env, port)
}
