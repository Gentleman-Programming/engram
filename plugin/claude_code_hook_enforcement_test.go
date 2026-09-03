package plugin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Layer 2 of the Claude Code hook test strategy: the small interpreter-free
// backstops for hooks.json and the PowerShell fallback. Bash contracts belong
// to the behavior tests, which execute the hooks and parse their output.

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

var powerShellBootstrapTools = []string{
	"mem_save", "mem_search", "mem_context", "mem_session_summary",
	"mem_session_start", "mem_session_end", "mem_get_observation",
	"mem_suggest_topic_key", "mem_capture_passive", "mem_save_prompt",
	"mem_update", "mem_current_project", "mem_judge",
}

// powerShellToolSearchSet reads the ToolSearch message assignment inside its
// intended function body and returns its exact comma-delimited tokens.
func powerShellToolSearchSet(t *testing.T, script string) map[string]bool {
	t.Helper()
	const functionStart = "function Write-ToolSearchMessage {"
	start := strings.Index(script, functionStart)
	if start < 0 {
		t.Fatal("PowerShell hook has no Write-ToolSearchMessage function")
	}
	body := script[start+len(functionStart):]
	endBody := strings.Index(body, "\n}")
	if endBody < 0 {
		t.Fatal("PowerShell hook has an unterminated Write-ToolSearchMessage function")
	}
	for _, line := range strings.Split(body[:endBody], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `$message = "`) {
			continue
		}
		list := line[len(`$message = "`):]
		start := strings.Index(list, "select:")
		if start < 0 {
			continue
		}
		list = list[start+len("select:"):]
		end := strings.Index(list, "`n")
		if end < 0 {
			t.Fatalf("PowerShell ToolSearch message has no terminating `n: %q", line)
		}
		set := make(map[string]bool)
		for _, token := range strings.Split(list[:end], ",") {
			if token != "" {
				set[token] = true
			}
		}
		return set
	}
	t.Fatal("PowerShell hook has no $message assignment with a ToolSearch select: list")
	return nil
}

func powerShellToolSearchFixture(lines ...string) string {
	return "function Write-ToolSearchMessage {\n" + strings.Join(lines, "\n") + "\n}"
}

func assertExactPowerShellToolSearchNames(t *testing.T, listed map[string]bool) {
	t.Helper()
	want := make(map[string]bool, len(powerShellBootstrapTools)*2)
	for _, prefix := range []string{"mcp__engram__", "mcp__plugin_engram_engram__"} {
		for _, tool := range powerShellBootstrapTools {
			want[prefix+tool] = true
		}
	}
	for name := range want {
		if !listed[name] {
			t.Errorf("PowerShell ToolSearch list is missing %q", name)
		}
	}
	for name := range listed {
		if !want[name] {
			t.Errorf("PowerShell ToolSearch list contains unexpected name %q", name)
		}
	}
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
	want := map[string]bool{"startup": true, "resume": true, "clear": true, "fork": true}
	for token := range want {
		if !tokens[token] {
			t.Errorf("SessionStart session-start.sh matcher %q is missing exact token %q - resumed/forked sessions get no context injection", matcher, token)
		}
	}
	for token := range tokens {
		if !want[token] {
			t.Errorf("SessionStart session-start.sh matcher %q includes unexpected token %q", matcher, token)
		}
	}
}

// Defect 1 (PowerShell parity): the Windows-native fallback must use the same
// additionalContext shape and preserve its documented empty subsequent reply.
func TestUserPromptSubmitPowerShellUsesAdditionalContext(t *testing.T) {
	script := claudeScript(t, "user-prompt-submit.ps1")
	normalized := strings.ReplaceAll(script, "\r\n", "\n")

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
	if !strings.Contains(normalized, "function Write-EmptyHookResponse {\n  Write-Output '{}'\n}") ||
		!strings.Contains(normalized, "Write-EmptyHookResponse\n  exit 0") {
		t.Error("user-prompt-submit.ps1 must retain its documented empty subsequent response")
	}
	assertExactPowerShellToolSearchNames(t, powerShellToolSearchSet(t, script))
}

// mem_save is a prefix of mem_save_prompt; compare parsed tokens, not source
// substrings. A comment that looks like an assignment must not become the list.
func TestPowerShellToolSearchSetRejectsFalsePositives(t *testing.T) {
	t.Run("substring", func(t *testing.T) {
		set := powerShellToolSearchSet(t, powerShellToolSearchFixture("$message = \"select:mcp__engram__mem_save_prompt`n\""))
		if set["mcp__engram__mem_save"] || !set["mcp__engram__mem_save_prompt"] {
			t.Errorf("set = %v, want only mem_save_prompt", set)
		}
	})
	t.Run("comment marker", func(t *testing.T) {
		script := powerShellToolSearchFixture(
			"# $message = \"select:mcp__engram__mem_save`n\"",
			"$message = \"select:mcp__engram__mem_search`n\"")
		set := powerShellToolSearchSet(t, script)
		if set["mcp__engram__mem_save"] || !set["mcp__engram__mem_search"] {
			t.Errorf("set = %v, want only the real mem_search assignment", set)
		}
	})
	t.Run("misplaced assignment", func(t *testing.T) {
		script := "$message = \"select:mcp__engram__mem_save`n\"\n" +
			powerShellToolSearchFixture("$message = \"select:mcp__engram__mem_search`n\"")
		set := powerShellToolSearchSet(t, script)
		if set["mcp__engram__mem_save"] || !set["mcp__engram__mem_search"] {
			t.Errorf("set = %v, want only the Write-ToolSearchMessage assignment", set)
		}
	})
}
