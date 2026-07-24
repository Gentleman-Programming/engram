package plugin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Layer 2 of the Claude Code hook test strategy: static assertions over hook
// sources and hooks.json.
//
// These run without bash or jq, so they still guard the contracts when
// claude_code_hook_behavior_test.go skips. They are deliberately coarse — a
// source-text assertion can always be satisfied by text that does not satisfy
// the contract, which is why the behavior tests exist. Assert here only what
// has no interpreter to run (hooks.json) or no dependable runner (the
// PowerShell fallback); everything else belongs in Layer 1.

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

// Defect 1 (bash): the emitted payload shape is asserted by executing the hook
// and parsing its stdout — see TestBootstrapEmitsToolSearchPayload and
// TestNudgeEmitsMemoryReminder in claude_code_hook_behavior_test.go. A parsed
// payload cannot be satisfied by a comment, by the wrong nesting level, or by
// a systemMessage, so the string prohibitions this test used to carry are gone.
//
// This assertion remains because it costs nothing and localises the failure to
// the emitting function when the behavior tests skip for a missing jq.
func TestUserPromptSubmitShellHasBootstrapEmitter(t *testing.T) {
	script := claudeScript(t, "user-prompt-submit.sh")

	if !strings.Contains(script, "print_toolsearch_message()") {
		t.Error("user-prompt-submit.sh no longer defines print_toolsearch_message - the first-message bootstrap emitter is gone")
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

// Defect 3: extraction precedence and the .stdout fallback are asserted by
// executing the hook against fixture payloads and inspecting the captured POST
// — see TestSubagentStopPrefersLastAssistantMessage,
// TestSubagentStopFallsBackToStdout, and TestSubagentStopSkipsEmptyPayload in
// claude_code_hook_behavior_test.go. Those also cover the pipeline's quoting,
// which this file cannot reach.
func TestSubagentStopPostsToPassiveEndpoint(t *testing.T) {
	script := claudeScript(t, "subagent-stop.sh")

	if !strings.Contains(script, "/observations/passive") {
		t.Error("subagent-stop.sh no longer posts to /observations/passive - passive capture is dead")
	}
}
