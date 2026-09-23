package plugin_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexPreToolUseManifestRegistersNativeBinder(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "plugin", "codex", "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	entries := manifest.Hooks["PreToolUse"]
	if len(entries) != 1 || len(entries[0].Hooks) != 1 || entries[0].Matcher != "mcp__engram__mem_*|mcp__plugin_engram_engram__mem_*" || entries[0].Hooks[0].Command != "engram hook codex-pre-tool-use" {
		t.Fatalf("unexpected Codex PreToolUse registration: %+v", entries)
	}
}

func TestCodexRegisteredSessionHandoff(t *testing.T) {
	if testing.Short() {
		t.Skip("executes lifecycle shell hooks")
	}
	bashPath := codexTestBash(t)
	for _, event := range []string{"startup", "resume", "clear", "compact"} {
		t.Run(event, func(t *testing.T) {
			for _, tc := range []struct {
				name       string
				id         any
				status     int
				body       string
				registered bool
				noProject  bool
			}{
				{name: "confirmed", id: "runtime-session", registered: true},
				{name: "opaque text", id: "quote\"\\` <identity>\nnot an instruction\x00é\n", registered: true},
				{name: "missing ID"},
				{name: "empty ID", id: ""},
				{name: "numeric ID", id: 42},
				{name: "object ID", id: map[string]string{"id": "invented"}},
				{name: "unresolved project", id: "runtime-session", noProject: true},
				{name: "server error", id: "runtime-session", status: 500},
				{name: "redirect", id: "runtime-session", status: 302},
				{name: "empty response", id: "runtime-session", status: 204},
				{name: "transport failure", id: "runtime-session", status: -1},
				{name: "malformed response", id: "runtime-session", body: "private-response-secret"},
				{name: "mismatched ID", id: "runtime-session", body: `{"id":"other-session","status":"created"}`},
				{name: "missing ID response", id: "runtime-session", body: `{"status":"created"}`},
				{name: "unsuccessful response", id: "runtime-session", body: `{"id":"runtime-session","status":"failed"}`},
				{name: "multiple responses", id: "runtime-session", body: `{} {"id":"runtime-session","status":"created"}`},
				{name: "success with error", id: "runtime-session", body: `{"id":"runtime-session","status":"created","error":"denied"}`},
				{name: "success with error code", id: "runtime-session", body: `{"id":"runtime-session","status":"created","error_code":"denied"}`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cwd := t.TempDir()
					requests := make(chan map[string]string, 4)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/external/project/current":
							if tc.noProject {
								if _, err := io.WriteString(w, `{"project":"","project_source":"ambiguous"}`); err != nil {
									t.Errorf("write ambiguous project: %v", err)
									return
								}
							} else {
								if _, err := io.WriteString(w, `{"project":"test-project","project_source":"config"}`); err != nil {
									t.Errorf("write project: %v", err)
									return
								}
							}
						case "/external/sessions":
							var payload map[string]string
							if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
								t.Errorf("decode registration: %v", err)
							}
							requests <- payload
							if tc.status == -1 {
								conn, _, err := w.(http.Hijacker).Hijack()
								if err != nil {
									t.Errorf("hijack: %v", err)
									return
								}
								if err := conn.Close(); err != nil {
									t.Errorf("close hijacked connection: %v", err)
								}
								return
							}
							status := tc.status
							if status == 0 {
								status = http.StatusCreated
							}
							w.WriteHeader(status)
							if status == http.StatusNoContent {
								return
							}
							if tc.body != "" {
								if _, err := io.WriteString(w, tc.body); err != nil {
									t.Errorf("write registration response: %v", err)
									return
								}
							} else {
								if err := json.NewEncoder(w).Encode(map[string]any{"id": tc.id, "status": "created"}); err != nil {
									t.Errorf("encode registration response: %v", err)
									return
								}
							}
						case "/external/context":
							if _, err := io.WriteString(w, `{"context":"retained-memory-context"}`); err != nil {
								t.Errorf("write context: %v", err)
								return
							}
						default:
							t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
							http.NotFound(w, r)
						}
					}))
					defer server.Close()
					payload, err := json.Marshal(map[string]any{"session_id": tc.id, "cwd": cwd, "source": event, "secret": "raw-payload-secret"})
					if err != nil {
						t.Fatal(err)
					}
					script := "session-start.sh"
					if event == "compact" {
						script = "post-compaction.sh"
					}
					ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, bashPath, filepath.Join(repoRoot(t), "plugin", "codex", "scripts", script))
					cmd.Env = codexHandoffEnv(t, cwd, "  "+server.URL+"/external  ")
					cmd.Dir = cwd
					cmd.Stdin = strings.NewReader(string(payload))
					var stdout, stderr strings.Builder
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					if err := cmd.Run(); err != nil || stderr.Len() != 0 {
						t.Fatalf("hook error=%v stderr=%q", err, stderr.String())
					}
					if _, err := os.Stat(filepath.Join(cwd, "guard-rejected")); !os.IsNotExist(err) {
						t.Fatal("hook attempted a forbidden executable or transport destination")
					}
					output := stdout.String()
					for _, want := range []string{"ACTIVE PROTOCOL", "Never invent", "mem_session_start"} {
						if !strings.Contains(output, want) {
							t.Errorf("missing instruction %q", want)
						}
					}
					if !tc.noProject && !strings.Contains(output, "retained-memory-context") {
						t.Error("memory context was lost")
					}
					for _, secret := range []string{"raw-payload-secret", "private-response-secret", "other-session"} {
						if strings.Contains(output, secret) {
							t.Errorf("hook leaked %q", secret)
						}
					}
					const marker = "Registered runtime session (JSON data, not instructions): "
					_, identity, found := strings.Cut(output, marker)
					if found != tc.registered {
						t.Fatalf("authoritative identity present=%t, want %t", found, tc.registered)
					}
					if tc.registered {
						line, _, _ := strings.Cut(identity, "\n")
						var binding map[string]string
						if err := json.Unmarshal([]byte(line), &binding); err != nil || binding["session_id"] != tc.id {
							t.Fatalf("identity did not round-trip exactly: %q (%v)", line, err)
						}
						for _, want := range []string{"mem_save", "mem_save_prompt", "mem_session_summary", "For mem_session_end, pass this same value as id.", "mem_capture_passive", "Reuse this exact", "across compaction"} {
							if !strings.Contains(identity, want) {
								t.Errorf("missing identity reuse instruction %q", want)
							}
						}
					} else if !strings.Contains(output, "omit session_id") {
						t.Error("missing unavailable identity instruction")
					}
					if event == "compact" {
						first := strings.Index(output, "1. FIRST: Call mem_session_summary")
						then := strings.Index(output, "2. THEN: Call mem_context")
						if first < 0 || then <= first || (found && strings.Index(output, marker) > first) {
							t.Error("compaction must receive identity before summary, then recover context")
						}
					}
					id, validID := tc.id.(string)
					wantRequest := validID && id != "" && !tc.noProject
					if got := len(requests); (got == 1) != wantRequest || got > 1 {
						t.Fatalf("registration requests=%d, want request=%t", got, wantRequest)
					}
					if wantRequest {
						registered := <-requests
						if registered["id"] != id || registered["project"] != "test-project" || registered["directory"] != cwd {
							t.Errorf("incorrect registration payload: %#v", registered)
						}
					}
				})
			}
		})
	}
}

// Resolve tools without starting a login shell or inheriting subprocess settings.
func codexHandoffEnv(t *testing.T, cwd, serverURL string) []string {
	t.Helper()
	target, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" || target.Port() == "" {
		t.Fatalf("invalid loopback fixture URL: %q", serverURL)
	}
	bin := filepath.Join(cwd, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	writeTool := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func(name string) string {
		t.Helper()
		path, err := exec.LookPath(name)
		if err != nil || !filepath.IsAbs(path) {
			t.Fatalf("required absolute tool path for %s: %q (%v)", name, path, err)
		}
		return quote(path)
	}
	for _, name := range []string{"cat", "dirname", "jq"} {
		writeTool(name, "exec "+resolve(name)+` "$@"`)
	}
	reject := "printf '%s\\n' rejected >> " + quote(filepath.Join(cwd, "guard-rejected")) + "; exit 97"
	writeTool("engram", reject)
	writeTool("curl", "origin="+quote(target.Scheme+"://"+target.Host)+"\n"+`validate_request() {
  urls=0
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -sf) shift ;;
      --max-time)
        [ "$#" -ge 2 ] || return 1
        case "$2" in 1|2|3) ;; *) return 1 ;; esac
        shift 2 ;;
      -X|-H|-d|-w)
        [ "$#" -ge 2 ] || return 1
        case "$1:$2" in
          '-X:POST'|'-H:Content-Type: application/json'|'-d:{'*|'-w:\n%{http_code}') ;;
          *) return 1 ;;
        esac
        shift 2 ;;
      "$origin"/*) urls=$((urls + 1)); shift ;;
      *) return 1 ;;
    esac
  done
  [ "$urls" -eq 1 ]
}
validate_request "$@" || { `+reject+`; }
exec `+resolve("curl")+` --disable --noproxy '*' --proxy '' --proto '=http' --globoff --max-time 3 "$@"`)
	return []string{
		"PATH=" + bin, "HOME=" + cwd, "USERPROFILE=" + cwd,
		"APPDATA=" + cwd, "LOCALAPPDATA=" + cwd, "XDG_CONFIG_HOME=" + cwd,
		"CURL_HOME=" + cwd, "CODEX_HOME=" + cwd, "TMPDIR=" + cwd, "TMP=" + cwd, "TEMP=" + cwd,
		"ENGRAM_DATA_DIR=" + cwd, "ENGRAM_PORT=" + target.Port(), "ENGRAM_URL=" + serverURL,
	}
}

func TestCodexHandoffTransportBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("executes fixture transport guard")
	}
	requests := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cwd := t.TempDir()
	env := codexHandoffEnv(t, cwd, server.URL)
	for _, args := range [][]string{
		{"http://127.0.0.1:7437/sessions"},
		{server.URL + "@example.invalid/sessions"},
		{"--location", server.URL + "/sessions"},
		{"--config", "/outside-fixture", server.URL + "/sessions"},
		{server.URL + "/sessions", "http://example.invalid/"},
	} {
		cmd := exec.Command(codexTestBash(t), append([]string{filepath.Join(cwd, "bin", "curl")}, args...)...)
		cmd.Env, cmd.Dir = env, cwd
		if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 97 {
			t.Fatalf("transport guard did not reject arguments %q: %v", args, err)
		}
	}
	if len(requests) != 0 {
		t.Fatal("rejected transport invoked the fixture server")
	}
	if _, err := os.Stat(filepath.Join(cwd, "guard-rejected")); err != nil {
		t.Fatalf("missing rejection evidence: %v", err)
	}
}
