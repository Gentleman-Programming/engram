package plugin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCodexPromptSubmitRejectsEndedHostSession(t *testing.T) {
	codexPromptSubmitSession(t, true, false)
}

func TestCodexPromptSubmitRejectsUnconfirmedRegistration(t *testing.T) {
	codexPromptSubmitSession(t, false, true)
}

func TestCodexPromptSubmitPersistsActiveHostSession(t *testing.T) {
	codexPromptSubmitSession(t, false, false)
}

func codexPromptSubmitSession(t *testing.T, ended, refuseRegistration bool) {
	t.Helper()
	if testing.Short() {
		t.Skip("executes Codex shell hook")
	}
	bash := codexTestBash(t)
	for _, tool := range []string{"curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("requires %s: %v", tool, err)
		}
	}
	root := t.TempDir()
	db, err := store.New(store.FallbackConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close prompt fixture store: %v", err)
		}
	})
	id := "prompt-" + filepath.Base(root)
	const project = "codex-prompt-probe"
	if err := db.StartSession(id, project, root); err != nil {
		t.Fatal(err)
	}
	if ended {
		if err := db.EndSession(id, "finished"); err != nil {
			t.Fatal(err)
		}
	}
	production := server.New(db, 0).Handler()
	var posts, registrations atomic.Int32
	requests := make(chan struct{}, 1)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/runtime-sessions/resolve" && r.Method == http.MethodPost {
			if refuseRegistration {
				http.Error(w, "resolution unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		if r.URL.Path == "/sessions" && r.Method == http.MethodPost {
			registrations.Add(1)
			if refuseRegistration {
				http.Error(w, "registration unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		if r.URL.Path == "/project/current" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"project":"codex-prompt-probe","project_source":"config"}`)
			return
		}
		if r.URL.Path == "/prompts" && r.Method == http.MethodPost {
			posts.Add(1)
			production.ServeHTTP(w, r)
			select {
			case requests <- struct{}{}:
			default:
			}
			return
		}
		production.ServeHTTP(w, r)
	}))
	defer fixture.Close()
	bin := filepath.Join(root, "bin")
	env := codexHandoffEnv(t, root, fixture.URL, buildCodexFixtureCLI(t))
	if err := os.WriteFile(filepath.Join(bin, "touch"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	const prompt = "capture only for an active host session"
	payload, err := json.Marshal(map[string]string{"session_id": id, "cwd": root, "prompt": prompt})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, filepath.Join(repoRoot(t), "plugin", "codex", "scripts", "user-prompt-submit.sh"))
	cmd.Dir = root
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(payload)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook failed: %v: %s", err, output)
	}
	// The hook returns before its detached write. Wait for the active request;
	// rejected requests get a bounded quiet window after registration completes.
	if ended || refuseRegistration {
		select {
		case <-requests:
			t.Fatal("prompt POST followed rejected registration")
		case <-time.After(2 * time.Second):
		}
	} else {
		select {
		case <-requests:
		case <-time.After(5 * time.Second):
			t.Fatal("confirmed prompt POST did not arrive")
		}
	}
	prompts, err := db.RecentPrompts(project, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := 1
	if ended || refuseRegistration {
		want = 0
	}
	if registrations.Load() != 0 || len(prompts) != want || posts.Load() != int32(want) {
		t.Fatalf("ended=%t refused=%t: registrations=%d, persisted prompts=%d, POST /prompts=%d; want no registrations and %d prompts", ended, refuseRegistration, registrations.Load(), len(prompts), posts.Load(), want)
	}
	if want == 1 && (prompts[0].SessionID != id || prompts[0].Content != prompt) {
		t.Fatalf("active prompt = %+v, want session %q and content %q", prompts[0], id, prompt)
	}
}

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
	binary := buildCodexFixtureCLI(t)
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
					requests := make(chan map[string]any, 4)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/external/sessions" || r.URL.Path == "/external/runtime-sessions/resolve" {
							wantPath := "/external/sessions"
							if event == "compact" {
								wantPath = "/external/runtime-sessions/resolve"
							}
							if r.Method != http.MethodPost || r.URL.Path != wantPath {
								t.Errorf("wrong lifecycle operation: %s %s", r.Method, r.URL.Path)
							}
						}
						switch r.URL.Path {
						case "/external/health":
							_, _ = io.WriteString(w, `{"status":"ok","service":"engram","capabilities":{"runtime_session_resolution":true}}`)
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
						case "/external/sessions", "/external/runtime-sessions/resolve":
							body, err := io.ReadAll(r.Body)
							if err != nil {
								t.Errorf("read registration: %v", err)
							}
							if tc.name == "opaque text" {
								if !bytes.Contains(body, []byte("é")) {
									t.Errorf("registration body lost UTF-8 bytes for opaque ID: %q", body)
								}
							}
							var payload map[string]any
							if err := json.Unmarshal(body, &payload); err != nil {
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
								if event == "compact" {
									status = http.StatusOK
								}
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
								if err := json.NewEncoder(w).Encode(map[string]any{"id": tc.id, "status": map[bool]string{true: "resolved", false: "created"}[event == "compact"]}); err != nil {
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
					cmd.Env = codexHandoffEnv(t, cwd, "  "+server.URL+"/external  ", binary)
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
					for _, want := range []string{"Never invent", "mem_session_start"} {
						if !strings.Contains(output, want) {
							t.Errorf("missing instruction %q", want)
						}
					}
					if tc.registered {
						if !strings.Contains(output, "ACTIVE PROTOCOL") {
							t.Error("confirmed registration missing active protocol")
						}
					} else if event != "compact" {
						for _, forbidden := range []string{"ACTIVE PROTOCOL", "Call `mem_save`", "Call `mem_session_summary`", "call mem_search"} {
							if strings.Contains(output, forbidden) {
								t.Errorf("unconfirmed startup instructs agent memory use: %q", forbidden)
							}
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
					} else {
						for _, want := range []string{"Agent-attributed memory writes must stop", "host hook re-registers the same runtime ID", "independent CLI/manual save", "not a substitute for session attribution"} {
							if !strings.Contains(output, want) {
								t.Errorf("missing failed registration guidance %q", want)
							}
						}
						if strings.Contains(output, "omit session_id") {
							t.Error("failure advises omitted-ID agent write")
						}
					}
					if event == "compact" {
						first := strings.Index(output, "1. FIRST: Call mem_session_summary")
						then := strings.Index(output, "2. THEN: Call mem_context")
						if tc.registered {
							if first < 0 || then <= first || strings.Index(output, marker) > first {
								t.Error("confirmed compaction must receive identity before summary, then recover context")
							}
						} else {
							for _, forbidden := range []string{"ACTIVE PROTOCOL", "1. FIRST: Call mem_session_summary", "2. THEN: Call mem_context", "call mem_search", "Call `mem_save`", "Call `mem_session_summary`"} {
								if strings.Contains(output, forbidden) {
									t.Errorf("unconfirmed compaction instructs tool use: %q", forbidden)
								}
							}
						}
					}
					id, validID := tc.id.(string)
					wantRequest := validID && id != "" && !tc.noProject
					if got := len(requests); (got == 1) != wantRequest || got > 1 {
						t.Fatalf("registration requests=%d, want request=%t", got, wantRequest)
					}
					if wantRequest {
						registered := <-requests
						if registered["id"] != id || registered["project"] != "test-project" || registered["directory"] != cwd || (event != "compact" && registered["resume"] != true) {
							t.Errorf("incorrect registration payload: %#v", registered)
						}
					}
				})
			}
		})
	}
}

var codexFixtureBinaries sync.Map
var codexFixtureOnce sync.Once
var codexFixturePath, codexFixtureDir string
var codexFixtureErr error

func TestMain(m *testing.M) {
	// Pi exports PI_CODING_AGENT to every process it starts, and the Claude Code
	// hooks are no-ops when it is set. Clear it so the hook tests behave the same
	// from a Pi shell as from CI; the Pi guard tests set it explicitly.
	if err := os.Unsetenv("PI_CODING_AGENT"); err != nil {
		fmt.Fprintf(os.Stderr, "clear PI_CODING_AGENT: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	if codexFixtureDir != "" {
		if err := os.RemoveAll(codexFixtureDir); err != nil {
			code = 1
		}
	}
	os.Exit(code)
}

// Resolve tools without starting a login shell or inheriting subprocess settings.
func buildCodexFixtureCLI(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	codexFixtureOnce.Do(func() {
		codexFixtureDir, codexFixtureErr = os.MkdirTemp("", "engram-codex-fixture-")
		if codexFixtureErr != nil {
			return
		}
		codexFixturePath = filepath.ToSlash(filepath.Join(codexFixtureDir, "engram.exe"))
		cmd := exec.Command("go", "build", "-o", codexFixturePath, "./cmd/engram")
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			codexFixtureErr = fmt.Errorf("build fixture CLI: %w: %s", err, output)
			return
		}
		codexFixtureBinaries.Store(codexFixturePath, true)
	})
	if codexFixtureErr != nil {
		t.Fatal(codexFixtureErr)
	}
	return codexFixturePath
}

func codexHandoffEnv(t *testing.T, cwd, serverURL string, binary ...string) []string {
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
	for _, name := range []string{"cat", "dirname", "jq", "date", "touch", "bash"} {
		writeTool(name, "exec "+resolve(name)+` "$@"`)
	}
	reject := "printf '%s\\n' rejected >> " + quote(filepath.Join(cwd, "guard-rejected")) + "; exit 97"
	writeTool("engram", reject)
	if len(binary) == 1 {
		if _, owned := codexFixtureBinaries.Load(binary[0]); !owned || !filepath.IsAbs(binary[0]) {
			t.Fatal("fixture CLI must be an absolute candidate-built executable")
		}
		writeTool("engram", `[ "$#" -eq 2 ] && [ "$1" = hook ] && [ "$ENGRAM_URL" = `+quote(strings.TrimSpace(serverURL))+` ] || { `+reject+`; }
case "$2" in
  codex-register|codex-resolve) [ "$ENGRAM_HOOK_OUTPUT" = json ] || { `+reject+`; } ;;
  codex-session-end) ;;
  *) `+reject+` ;;
esac
exec `+quote(binary[0])+` "$@"`)
	}
	writeTool("curl", "origin="+quote(target.Scheme+"://"+target.Host)+"\n"+`validate_request() {
  urls=0
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -sf) shift ;;
      --max-time)
        [ "$#" -ge 2 ] || return 1
        case "$2" in 0.2|1|2|3) ;; *) return 1 ;; esac
        shift 2 ;;
      -d)
        [ "$#" -ge 2 ] || return 1
        printf '%s' "$2" | jq -e 'type == "object" and (.session_id | type) == "string"' >/dev/null || return 1
        shift 2 ;;
      -X|-H|--data-binary|-w)
        [ "$#" -ge 2 ] || return 1
        case "$1:$2" in
          '-X:POST'|'-H:Content-Type: application/json'|'--data-binary:@-'|'-w:\n%{http_code}') ;;
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
	guard := exec.Command(codexTestBash(t), filepath.Join(cwd, "bin", "engram"), "hook", "codex-resolve")
	guard.Env = env
	if err := guard.Run(); err == nil {
		t.Fatal("fixture selected an ambient CLI rather than its rejecting guard")
	}
	if len(requests) != 0 {
		t.Fatal("rejected transport invoked the fixture server")
	}
	if _, err := os.Stat(filepath.Join(cwd, "guard-rejected")); err != nil {
		t.Fatalf("missing rejection evidence: %v", err)
	}
}
