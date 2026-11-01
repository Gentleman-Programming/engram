package plugin_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestClaudeSubagentHandbackHookRegistration(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "plugin", "claude-code", "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Async   bool   `json:"async"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, group := range manifest.Hooks["PostToolUse"] {
		if group.Matcher != "^SubagentHandback$" {
			continue
		}
		for _, hook := range group.Hooks {
			if hook.Type == "command" && hook.Command == `"${CLAUDE_PLUGIN_ROOT}/scripts/subagent-stop.sh"` && hook.Async && hook.Timeout == 10 {
				return
			}
		}
	}
	t.Fatal("missing asynchronous exact-match SubagentHandback capture hook")
}

func TestSubagentHandbackPersistsAndDeduplicatesThroughCore(t *testing.T) {
	requireHookBinaries(t)
	db, err := store.New(store.FallbackConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".engram"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".engram", "config.json"), []byte(`{"project_name":"handback-project"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession("handback-session", "handback-project", root); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(db, 0).Handler())
	t.Cleanup(srv.Close)
	const report = "## Key Learnings\n1. Una decisión válida mantiene su sesión y proyecto sin duplicarse.\n"
	for _, event := range []string{"PostToolUse", "SubagentStop"} {
		input, err := json.Marshal(map[string]any{"hook_event_name": event, "tool_name": "SubagentHandback", "tool_input": map[string]string{"message": report}, "session_id": "handback-session", "cwd": root, "last_assistant_message": report})
		if err != nil {
			t.Fatal(err)
		}
		runHook(t, "subagent-stop.sh", string(input), map[string]string{"ENGRAM_PORT": serverPort(t, srv), "PI_CODING_AGENT": ""})
		rows, err := db.AllObservations("handback-project", "", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].SessionID != "handback-session" || rows[0].Content != "Una decisión válida mantiene su sesión y proyecto sin duplicarse." {
			t.Fatalf("capture after %s: %+v", event, rows)
		}
	}
}

func TestSubagentHandbackPayloadHandling(t *testing.T) {
	requireHookBinaries(t)
	const report = "## Key Learnings\r\n1. La sesión conserva eñes → 中文 🧠\r\n\"quoted\" $HOME `id` \\path\n"
	message := func(value any) map[string]any { return map[string]any{"message": value} }
	for _, tc := range []struct {
		name, event, tool string
		toolInput         any
		want              string
	}{
		{"nonobject tool input", "PostToolUse", "SubagentHandback", report, ""},
		{"missing tool input", "PostToolUse", "SubagentHandback", nil, ""},
		{"report", "PostToolUse", "SubagentHandback", message(report), report},
		{"empty", "PostToolUse", "SubagentHandback", message(""), ""},
		{"missing", "PostToolUse", "SubagentHandback", message(nil), ""},
		{"object", "PostToolUse", "SubagentHandback", message(map[string]string{"message": report}), ""},
		{"array", "PostToolUse", "SubagentHandback", message([]string{report}), ""},
		{"boolean", "PostToolUse", "SubagentHandback", message(false), ""},
		{"unrelated tool", "PostToolUse", "Write", message(report), ""},
		{"failed handback", "PostToolUseFailure", "SubagentHandback", message(report), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, captured := captureServer(t)
			input, err := json.Marshal(map[string]any{
				"hook_event_name": tc.event, "tool_name": tc.tool, "tool_input": tc.toolInput,
				"session_id": "handback-session", "cwd": t.TempDir(), "last_assistant_message": "Report handed back.", "stdout": "not the report",
			})
			if err != nil {
				t.Fatal(err)
			}
			runHook(t, "subagent-stop.sh", string(input), map[string]string{"ENGRAM_PORT": serverPort(t, srv), "PI_CODING_AGENT": ""})
			got := captured()
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected captures: %+v", got)
				}
				return
			}
			want := passiveCapture{SessionID: "handback-session", Project: "engram", Source: "subagent-stop", Content: tc.want}
			if len(got) != 1 || got[0] != want {
				t.Fatalf("capture=%+v want=%+v", got, want)
			}
		})
	}
}
