package plugin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests lock in the four fixes that restore Claude Code hook enforcement
// after SessionStart. Each defect no-op'd silently, so the regression risk is
// that a future edit reverts one without any runtime error. The tests are
// deterministic file/JSON assertions (no script execution) to match the
// existing plugin_test convention (see hooks_quoting_test.go, assets_test.go)
// and the engram testing-coverage rule: prefer deterministic tests over flaky
// integration paths.

func claudeScript(t *testing.T, name string) string {
	t.Helper()
	root := repoRoot(t)
	path := filepath.Join(root, "plugin", "claude-code", "scripts", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", name, err)
	}
	return string(data)
}

// Defect 4: the SessionStart matcher must cover resumed and forked sessions.
// A resumed/forked session receives no engram context injection when the
// matcher is only "startup|clear".
func TestSessionStartMatcherCoversResumeAndFork(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "plugin", "claude-code", "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("cannot read hooks.json: %v", err)
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
		t.Fatalf("cannot parse hooks.json: %v", err)
	}

	var matcher string
	for _, group := range manifest.Hooks["SessionStart"] {
		for _, h := range group.Hooks {
			if strings.Contains(h.Command, "session-start.sh") {
				matcher = group.Matcher
			}
		}
	}
	if matcher == "" {
		t.Fatal("no SessionStart group invokes session-start.sh — hooks.json changed")
	}

	for _, want := range []string{"startup", "resume", "clear", "fork"} {
		if !strings.Contains(matcher, want) {
			t.Errorf("SessionStart session-start.sh matcher %q is missing %q — resumed/forked sessions get no context injection", matcher, want)
		}
	}
}

// Defect 1: on a UserPromptSubmit hook, only stdout / additionalContext enters
// the model's context. A systemMessage payload renders to the terminal as
// "UserPromptSubmit says: ..." (issue #145) and never reaches the model, so the
// bootstrap and the save nudge silently no-op. The shell hook must emit
// hookSpecificOutput.additionalContext and must not emit a systemMessage
// payload.
func TestUserPromptSubmitShellUsesAdditionalContext(t *testing.T) {
	script := claudeScript(t, "user-prompt-submit.sh")

	if !strings.Contains(script, "additionalContext") {
		t.Error("user-prompt-submit.sh does not emit additionalContext — hook output never reaches the model")
	}
	if !strings.Contains(script, `"hookEventName":"UserPromptSubmit"`) &&
		!strings.Contains(script, `hookEventName: "UserPromptSubmit"`) {
		t.Error("user-prompt-submit.sh additionalContext payload is missing hookEventName: UserPromptSubmit")
	}
	// Assert on the emitted JSON key, not the bare word: the explanatory code
	// comments legitimately reference systemMessage.
	if strings.Contains(script, `"systemMessage":`) {
		t.Error("user-prompt-submit.sh still emits a systemMessage payload — that renders to the terminal and never reaches the model (issue #145)")
	}
}

// Defect 1 (PowerShell parity): the Windows-native fallback must use the same
// additionalContext shape. Note: the .ps1 keeps a code comment mentioning
// systemMessage for historical context, so this test asserts on the emitted
// object shape, not on the mere absence of the word.
func TestUserPromptSubmitPowerShellUsesAdditionalContext(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "plugin", "claude-code", "scripts", "user-prompt-submit.ps1"))
	if err != nil {
		t.Fatalf("cannot read user-prompt-submit.ps1: %v", err)
	}
	script := string(data)

	if !strings.Contains(script, "additionalContext") || !strings.Contains(script, "hookEventName") {
		t.Error("user-prompt-submit.ps1 does not emit hookSpecificOutput.additionalContext")
	}
	// The emitted object must not set systemMessage as an output field.
	if strings.Contains(script, "systemMessage =") {
		t.Error("user-prompt-submit.ps1 still emits a systemMessage output field — it never reaches the model (issue #145)")
	}
}

// Defect 2 (wrong tool-name prefix / dual-prefix bootstrap) is covered by
// TestClaudeCodeUserPromptHookCovers{Direct,Plugin}ServerID in
// internal/setup/setup_test.go, alongside the other Claude Code setup tests.

// Defect 3: Claude Code's SubagentStop payload carries the subagent's final
// text in last_assistant_message; there is no .stdout field, so reading .stdout
// captured nothing and every subagent run no-op'd. The hook must read
// last_assistant_message (keeping .stdout as a fallback for other harnesses).
func TestSubagentStopReadsLastAssistantMessage(t *testing.T) {
	script := claudeScript(t, "subagent-stop.sh")

	if !strings.Contains(script, "last_assistant_message") {
		t.Error("subagent-stop.sh does not read last_assistant_message — SubagentStop passive capture no-ops on every run")
	}
	if strings.Contains(script, `'.stdout // empty'`) {
		t.Error("subagent-stop.sh reads only .stdout — that field is absent from the SubagentStop payload")
	}
}
