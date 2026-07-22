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

	// Compare exact alternatives split on "|": strings.Contains would accept an
	// invalid superset like "resumed" as satisfying "resume".
	tokens := make(map[string]bool)
	for _, tok := range strings.Split(matcher, "|") {
		tokens[tok] = true
	}
	for _, want := range []string{"startup", "resume", "clear", "fork"} {
		if !tokens[want] {
			t.Errorf("SessionStart session-start.sh matcher %q is missing exact token %q - resumed/forked sessions get no context injection", matcher, want)
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

	// Assert the contiguous nested structure, not separate key checks: Claude
	// Code only delivers additionalContext when it sits inside hookSpecificOutput
	// with the UserPromptSubmit event, so a top-level additionalContext must fail.
	if !strings.Contains(script, `"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":`) {
		t.Error("user-prompt-submit.sh bootstrap must nest additionalContext inside hookSpecificOutput with hookEventName UserPromptSubmit")
	}
	// The nudge path builds the same structure via jq (unquoted keys).
	if !strings.Contains(script, `hookSpecificOutput: {hookEventName: "UserPromptSubmit", additionalContext:`) {
		t.Error("user-prompt-submit.sh nudge must nest additionalContext inside hookSpecificOutput with hookEventName UserPromptSubmit")
	}
	// Assert on the emitted JSON key, not the bare word: the explanatory code
	// comments legitimately reference systemMessage.
	if strings.Contains(script, `"systemMessage":`) {
		t.Error("user-prompt-submit.sh still emits a systemMessage payload — that renders to the terminal and never reaches the model (issue #145)")
	}
}

// Defect 1 (PowerShell parity): the Windows-native fallback must use the same
// additionalContext shape. The assertions target emit-only tokens (property
// assignments and the quoted event value), not bare words: the .ps1 comments
// mention "additionalContext" and "systemMessage", so a word search would pass
// even if the emitted object wrapper or event value were removed.
func TestUserPromptSubmitPowerShellUsesAdditionalContext(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "plugin", "claude-code", "scripts", "user-prompt-submit.ps1"))
	if err != nil {
		t.Fatalf("cannot read user-prompt-submit.ps1: %v", err)
	}
	script := string(data)

	for _, want := range []string{
		"hookSpecificOutput = [PSCustomObject]", // the wrapper object
		"'UserPromptSubmit'",                    // the exact event value
		"additionalContext = $message",          // the context field carrying the payload
	} {
		if !strings.Contains(script, want) {
			t.Errorf("user-prompt-submit.ps1 emitted payload is missing %q - additionalContext must be wrapped in hookSpecificOutput with the UserPromptSubmit event", want)
		}
	}
	// The emitted object must not set systemMessage as an output field.
	if strings.Contains(script, "systemMessage =") {
		t.Error("user-prompt-submit.ps1 still emits a systemMessage output field - it never reaches the model (issue #145)")
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

	// Positively require the combined extraction: last_assistant_message for
	// Claude Code, with .stdout retained as the fallback for other harnesses.
	// Asserting only the absence of the stdout-only form would let a
	// last_assistant_message-only extraction pass while breaking those harnesses.
	if !strings.Contains(script, ".last_assistant_message // .stdout") {
		t.Error("subagent-stop.sh must extract '.last_assistant_message // .stdout' - last_assistant_message for Claude Code, .stdout fallback for other harnesses")
	}
	if strings.Contains(script, `'.stdout // empty'`) {
		t.Error("subagent-stop.sh reads only .stdout - that field is absent from the SubagentStop payload")
	}
}
