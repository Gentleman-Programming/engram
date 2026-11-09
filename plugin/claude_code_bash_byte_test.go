package plugin_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Apple Bash 3.2 printf.def's asciicode returns the quoted char without an
// unsigned cast. Exercise that signed-byte result through the public hook;
// this compatibility fixture is not execution of a native Bash 3.2 binary.
// https://github.com/apple-oss-distributions/bash/blob/main/bash-3.2/builtins/printf.def
const legacySignedBytePrintf = `
printf() {
  local _legacy_dest="" _legacy_format _legacy_number
  local LC_ALL=C
  if [[ "$1" == -v ]]; then _legacy_dest="$2"; shift 2; fi
  _legacy_format="$1"; shift
  case "$_legacy_format" in
    %d|%02X|%02x)
      if [[ "${1:0:1}" == "'" ]]; then
        builtin printf -v _legacy_number '%d' "$1"
        if (( _legacy_number >= 128 )); then _legacy_number=$(( _legacy_number - 256 )); fi
        set -- "$_legacy_number" "${@:2}"
      fi
      ;;
  esac
  if [[ -n "$_legacy_dest" ]]; then
    builtin printf -v "$_legacy_dest" "$_legacy_format" "$@"
  else
    builtin printf "$_legacy_format" "$@"
  fi
}
`

func TestClaudeCodeNoJQDecisionBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("executes the owned Bash hook and HTTP fixture")
	}
	requireHookBinaries(t)
	for _, tc := range []struct {
		name, project string
		legacy        bool
		rawResponse   string
		wantPrompts   int
		turns         int
	}{
		{name: "current Unicode", project: "hook test?mañana", wantPrompts: 1},
		{name: "legacy ASCII", project: "canonical-project", legacy: true, wantPrompts: 1},
		{name: "legacy Unicode", project: "hook test?mañana", legacy: true, wantPrompts: 2, turns: 2},
		{name: "legacy raw control rejected", legacy: true, rawResponse: "{\"decision\":\"capture\",\"project\":\"bad\x1fproject\",\"project_source\":\"config\"}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const cwd = "/workspace with space/mañana"
			const prompt = "quote \" slash \\ newline\nbmp Ω pair 😃 esc \x1b"
			session := newSessionID(t)
			var mu sync.Mutex
			decisions, projectReads, observationReads := 0, 0, 0
			var stored []string
			port := claudeCodeWindowsPromptServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/prompts/capture-decision":
					var request struct{ Source, Cwd, Content string }
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decision JSON: %v", err)
					}
					if r.Method != http.MethodPost || request.Source != "claude-code" || request.Cwd != cwd || request.Content != prompt {
						t.Errorf("decision input changed: %s %+v", r.Method, request)
					}
					mu.Lock()
					decisions++
					mu.Unlock()
					if tc.rawResponse != "" {
						_, _ = w.Write([]byte(tc.rawResponse))
					} else {
						_ = json.NewEncoder(w).Encode(map[string]string{"decision": "capture", "project": tc.project, "project_source": "config"})
					}
				case "/prompts":
					var payload struct {
						SessionID string `json:"session_id"`
						Project   string `json:"project"`
						Content   string `json:"content"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Errorf("prompt JSON: %v", err)
					}
					if r.Method != http.MethodPost || payload.SessionID != session || payload.Project != tc.project || payload.Content != prompt {
						t.Errorf("prompt changed: %s %+v", r.Method, payload)
					}
					mu.Lock()
					stored = append(stored, payload.Content)
					mu.Unlock()
					w.WriteHeader(http.StatusNoContent)
				case "/project/current":
					if r.URL.Query().Get("cwd") != cwd {
						t.Errorf("reminder cwd changed: %q", r.URL.Query().Get("cwd"))
					}
					mu.Lock()
					projectReads++
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(map[string]string{"project": tc.project, "project_source": "config"})
				case "/sessions/" + session:
					_, _ = w.Write([]byte(`{"started_at":"2000-01-01T00:00:00Z"}`))
				case "/observations":
					if r.URL.Query().Get("project") != tc.project {
						t.Errorf("reminder project changed: %q", r.URL.Query().Get("project"))
					}
					mu.Lock()
					observationReads++
					mu.Unlock()
					_, _ = w.Write([]byte(`[{"created_at":"2000-01-01T00:00:00Z"}]`))
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			}))
			dir := t.TempDir()
			preload := `command() { if [[ "$1" == "-v" && "$2" == "jq" ]]; then return 1; fi; builtin command "$@"; }`
			if tc.legacy {
				preload += legacySignedBytePrintf
			}
			bashEnv := filepath.Join(dir, "no-jq.sh")
			if err := os.WriteFile(bashEnv, []byte(preload), 0600); err != nil {
				t.Fatal(err)
			}
			input, err := json.Marshal(map[string]string{"session_id": session, "cwd": cwd, "prompt": prompt})
			if err != nil {
				t.Fatal(err)
			}
			mustNotExist := filepath.Join(dir, "must-not-create-store")
			env := map[string]string{
				"ENGRAM_PORT": port, "TMPDIR": dir, "BASH_ENV": bashScriptPath(t, bashEnv), "ENGRAM_DATA_DIR": mustNotExist,
			}
			stdout, stderr := runHookWithStderr(t, "user-prompt-submit.sh", string(input), env)
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
			response := decodeHookPayload(t, stdout)
			if response.SystemMessage != "" || response.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
				t.Errorf("unexpected hook response: %+v", response)
			}
			assertToolSearchNames(t, selectNames(t, response.HookSpecificOutput.AdditionalContext))
			if !strings.Contains(response.HookSpecificOutput.AdditionalContext, "CRITICAL FIRST ACTION") {
				t.Errorf("bootstrap missing: %q", stdout)
			}
			turns := tc.turns
			if turns == 0 {
				turns = 1
			}
			if turns == 2 {
				stdout, stderr = runHookWithStderr(t, "user-prompt-submit.sh", string(input), env)
				if stderr != "" || !strings.Contains(stdout, "MEMORY REMINDER") {
					t.Errorf("reminder response=%q stderr=%q", stdout, stderr)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if decisions != turns || len(stored) != tc.wantPrompts {
				t.Errorf("decision requests=%d, prompt writes=%d, want %d/%d; stored=%q", decisions, len(stored), turns, tc.wantPrompts, stored)
			}
			if projectReads != turns-1 || observationReads != turns-1 {
				t.Errorf("reminder reads project=%d observations=%d, want %d each", projectReads, observationReads, turns-1)
			}
			if _, err := os.Stat(mustNotExist); !os.IsNotExist(err) {
				t.Fatalf("hook touched the local store: %v", err)
			}
		})
	}
}
