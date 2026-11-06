package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Exercise the public setup path without invoking a host CLI or using real profiles.
func TestMemoryProtocolInstalledHostInstructions(t *testing.T) {
	for _, agent := range []string{"gemini-cli", "codex"} {
		t.Run(agent, func(t *testing.T) {
			resetSetupSeams(t)
			useIsolatedProfile(t)
			lookPathFn = func(string) (string, error) { return "mock-host", nil }
			runCommand = func(string, ...string) ([]byte, error) { return []byte("ok"), nil }
			if _, err := Install(agent); err != nil {
				t.Fatal(err)
			}
			path := codexInstructionsPath()
			if agent == "gemini-cli" {
				path = geminiSystemPromptPath()
			}
			text := readMemoryContract(t, path)
			assertMemoryContract(t, text)
			for _, forbidden := range []string{"ToolSearch", "Core tools are loaded", "Deferred tools", "mcp__engram__", "permissions allowlist"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("generic instructions impose Claude bootstrap assumption %q", forbidden)
				}
			}
			if !strings.Contains(text, "Summary persistence does not end the session") {
				t.Error("missing summary/lifecycle distinction")
			}
			if agent == "gemini-cli" {
				if !strings.Contains(text, "After mem_session_summary succeeds, call mem_session_end") {
					t.Error("Gemini-specific closure was lost")
				}
				geminiClose := memoryProseBlock(t, text, "### GEMINI CLI SESSION CLOSE (mandatory)", "")
				if !regexp.MustCompile(`(?is)(actual|explicit).{0,50}(clos|end)`).MatchString(geminiClose) {
					t.Error("Gemini summary-then-end must be limited to actual session closure")
				}
				if !strings.Contains(geminiClose, "Do not call mem_session_end before mem_session_summary") {
					t.Error("Gemini summary-before-end ordering was lost")
				}
			} else if strings.Contains(text, "call mem_session_end") {
				t.Error("generic protocol must not require every host to end via MCP")
			}
		})
	}
}

// These manually maintained skills and docs are prose contracts, not generated copies.
func TestMemoryProtocolHostAssets(t *testing.T) {
	for _, path := range []string{
		"plugin/claude-code/skills/memory/SKILL.md",
		"plugin/codex/skills/memory/SKILL.md",
		"DOCS.md",
	} {
		t.Run(path, func(t *testing.T) {
			text := readMemoryContract(t, filepath.Join("../..", path))
			if path == "DOCS.md" {
				text = memoryProseBlock(t, text, "## Memory Protocol\n", "\n## Project Name Normalization")
			}
			assertMemoryContract(t, text)
			// Claude SKILL.md still references its own adapter names and discovery.
			if strings.Contains(path, "claude-code") {
				if !strings.Contains(text, "mcp__plugin_engram_engram__") {
					t.Error("Claude-specific registration guidance was lost")
				}
			}
			if strings.Contains(path, "codex") || path == "DOCS.md" {
				assertCodexBindingContract(t, text)
			}
			if strings.Contains(path, "codex") {
				for _, forbidden := range []string{"ToolSearch", "Core tools are loaded", "permissions allowlist"} {
					if strings.Contains(text, forbidden) {
						t.Errorf("Codex skill contains unsupported assumption %q", forbidden)
					}
				}
			}
		})
	}
}

// Session-start hooks inject protocol copies into the agent's context at runtime.
// Every injected copy MUST contain the shared phrase and MUST NOT leak Claude-specific
// assumptions (except where the host is Claude Code itself).
func TestMemoryProtocolSessionStartSh(t *testing.T) {
	for _, path := range []string{
		"plugin/codex/scripts/session-start.sh",
		"plugin/claude-code/scripts/session-start.sh",
	} {
		t.Run(path, func(t *testing.T) {
			text := readMemoryContract(t, filepath.Join("../..", path))
			// All session-start protocol copies must have the shared phrase.
			sharedPhrase := "The agent MUST follow this protocol without waiting for activation"
			if !strings.Contains(text, sharedPhrase) {
				t.Errorf("session-start.sh missing shared phrase %q", sharedPhrase)
			}
			// Host-specific hosts must not leak forbidden phrases in their protocol block.
			if strings.Contains(path, "codex") {
				for _, forbidden := range []string{"Core tools are loaded", "Deferred tools"} {
					if strings.Contains(text, forbidden) {
						t.Errorf("Codex session-start.sh contains forbidden assumption %q", forbidden)
					}
				}
			}
			if strings.Contains(path, "claude-code") {
				// Claude's session-start.sh legitimately references ToolSearch for
				// its own adapter; we only assert the shared phrase is present.
				if !strings.Contains(text, "## Engram Persistent Memory — ACTIVE PROTOCOL") {
					t.Error("Claude session-start.sh missing protocol header")
				}
			}
		})
	}
}

func assertCodexBindingContract(t *testing.T, text string) {
	t.Helper()
	for _, required := range []string{
		"PreToolUse", "engram hook codex-pre-tool-use",
		"mcp__engram__mem_*", "mcp__plugin_engram_engram__mem_*",
		"authoritative Codex host", "server-confirmed", "Read-only tools",
		"direct/nonintercepted clients", "reuse this exact",
		"`mem_session_end` uses `id`, not `session_id`",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Codex binding contract missing %q", required)
		}
	}
	if strings.Contains(text, "does not automatically intercept") {
		t.Error("Codex contract falsely denies native PreToolUse interception")
	}
}

func TestMemoryProtocolClaudeVersions(t *testing.T) {
	if err := memoryVersionsMatch(
		[]byte(readMemoryContract(t, "../../plugin/claude-code/.claude-plugin/plugin.json")),
		[]byte(readMemoryContract(t, "../../.claude-plugin/marketplace.json")),
	); err != nil {
		t.Fatal(err)
	}
}

// Explicit inventory: check delivered prose, not surrounding adapter code/comments.
func TestMemoryProtocolSourceInventory(t *testing.T) {
	for _, source := range []struct {
		path, start, end string
		claude           bool
	}{
		{"internal/setup/setup.go", "const memoryProtocolMarkdown = `", "`", false},
		{"plugin/opencode/engram.ts", "const MEMORY_INSTRUCTIONS = `", "\n`", false},
		{"internal/setup/plugins/opencode/engram.ts", "const MEMORY_INSTRUCTIONS = `", "\n`", false},
		{"plugin/pi/index.ts", "const MEMORY_INSTRUCTIONS = `", "\n`", false},
		{"plugin/pi/README.md", "### Injected memory contract", "### Automatic user-prompt capture", false},
		{"skills/memory-protocol/SKILL.md", "## When to Use", "", false},
		{"plugin/claude-code/skills/memory/SKILL.md", "", "", true},
		{"plugin/codex/skills/memory/SKILL.md", "", "", false},
		{"plugin/claude-code/scripts/session-start.sh", "cat <<'PROTOCOL'\n", "\nPROTOCOL", true},
		{"plugin/codex/scripts/session-start.sh", "cat <<'PROTOCOL'\n", "\nPROTOCOL", false},
		{"plugin/claude-code/scripts/post-compaction.sh", "cat <<'PROTOCOL'\n", "\nPROTOCOL", true},
		{"plugin/codex/scripts/post-compaction.sh", "cat <<'PROTOCOL'\n", "\nPROTOCOL", false},
		{"DOCS.md", "## Memory Protocol\n", "\n## Project Name Normalization", false},
	} {
		t.Run(source.path, func(t *testing.T) {
			text := readMemoryContract(t, filepath.Join("../..", source.path))
			text = memoryProseBlock(t, text, source.start, source.end)
			assertSelectiveMemoryContract(t, text)
			if !strings.Contains(text, "The agent MUST follow this protocol without waiting for activation") {
				t.Error("protocol prose missing shared activation phrase")
			}
			for _, forbidden := range []string{"Core tools are loaded", "Deferred tools", "Engram will automatically extract and save these", "does not automatically intercept"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("protocol prose contains obsolete assumption %q", forbidden)
				}
			}
			if !source.claude {
				for _, forbidden := range []string{"Use ToolSearch", "call ToolSearch", "permissions allowlist", "### CORE TOOLS — always available"} {
					if strings.Contains(text, forbidden) {
						t.Errorf("non-Claude prose imposes %q", forbidden)
					}
				}
			}
		})
	}
	source, err := os.ReadFile("../../plugin/opencode/engram.ts")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := os.ReadFile("plugins/opencode/engram.ts")
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != string(embedded) {
		t.Error("embedded OpenCode adapter differs from source")
	}
}

func memoryProseBlock(t *testing.T, text, start, end string) string {
	t.Helper()
	if start != "" {
		index := strings.Index(text, start)
		if index < 0 {
			t.Fatalf("missing prose start %q", start)
		}
		text = text[index+len(start):]
	}
	if end != "" {
		index := strings.Index(text, end)
		if index < 0 {
			t.Fatalf("missing prose end %q", end)
		}
		text = text[:index]
	}
	return text
}

func TestMemoryProtocolClaudeVersionFixtures(t *testing.T) {
	for _, fixture := range []struct {
		name, plugin, marketplace string
		wantError                 bool
	}{
		{"future synchronized bump", "9.4.2", "9.4.2", false},
		{"mismatch", "9.4.2", "9.4.1", true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			plugin := []byte(fmt.Sprintf(`{"version":%q}`, fixture.plugin))
			marketplace := []byte(fmt.Sprintf(`{"plugins":[{"name":"engram","version":%q}]}`, fixture.marketplace))
			if err := memoryVersionsMatch(plugin, marketplace); (err != nil) != fixture.wantError {
				t.Fatalf("synchronization error = %v, want error %v", err, fixture.wantError)
			}
		})
	}
}

func memoryVersionsMatch(pluginData, marketplaceData []byte) error {
	var plugin struct{ Version string }
	var marketplace struct {
		Plugins []struct{ Name, Version string }
	}
	if err := json.Unmarshal(pluginData, &plugin); err != nil {
		return err
	}
	if err := json.Unmarshal(marketplaceData, &marketplace); err != nil {
		return err
	}
	if plugin.Version == "" {
		return fmt.Errorf("Claude plugin version is empty")
	}
	for _, entry := range marketplace.Plugins {
		if entry.Name == "engram" && entry.Version == plugin.Version {
			return nil
		}
	}
	return fmt.Errorf("marketplace lacks matching Engram version")
}

func readMemoryContract(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func assertMemoryContract(t *testing.T, text string) {
	t.Helper()
	assertSelectiveMemoryContract(t, text)
	for _, required := range []string{
		"The agent MUST follow this protocol without waiting for activation",
		"adapter-specific", "ordinary responses", "primary",
		"If memory work fails or needs follow-up, still send the answer",
	} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(required)) {
			t.Errorf("memory contract missing %q", required)
		}
	}
	if strings.Contains(text, "Engram will automatically extract and save these") {
		t.Error("passive capture promises universal response extraction")
	}
}

// Static prose checks cover delivered consumers, including the public Install path
// above. They establish instruction alignment, not actual model obedience.
func assertSelectiveMemoryContract(t *testing.T, text string) {
	t.Helper()
	text = strings.Join(strings.Fields(strings.ReplaceAll(text, "\\`", "`")), " ")
	for _, rule := range []struct{ name, pattern string }{
		{"durable verified selection", `(?i)verified.{0,100}durable.{0,100}costly to reconstruct`},
		{"decisions with reasons", `(?i)decisions?.{0,40}(rationale|reasons)`},
		{"root cause and real constraints", `(?i)non-obvious root causes?.{0,100}real constraints`},
		{"resumable pending work", `(?i)resumption-relevant pending work`},
		{"deliberate grouping", `(?i)group related conclusions`},
		{"privacy and evidence exclusions", `(?i)(exclude|never (save|persist)).{0,180}secrets.{0,180}private.{0,180}speculation.{0,180}raw.{0,180}routine progress`},
		{"useful recall, not mention trigger", `(?i)materially useful.{0,100}(recall|related work)`},
		{"progressive recall", `(?i)mem_context.{0,300}mem_search.{0,1000}mem_get_observation`},
		{"no project-mention ceremony", `(?i)(mere|ordinary) project mentions?.{0,100}(not|no|never)`},
		{"no per-task ceremony", `(?i)(not every (task|response|event)|no per-(task|event))`},
		{"search before selected write", `(?i)mem_search.{0,160}(before writing|before (a |each )?selected write)`},
		{"read full candidates", `(?i)mem_get_observation.{0,180}(full|snippet)|read.{0,30}full.{0,160}mem_get_observation`},
		{"update same topic", `(?i)same.{0,60}topic.{0,100}mem_update`},
		{"preserve facts and history", `(?i)preserve.{0,80}(still-valid|valid) fact.{0,180}history`},
		{"new topics only", `(?i)(genuinely new|new bounded) topic.{0,100}mem_save`},
		{"covered knowledge needs no write", `(?i)(already covered.{0,70}(nothing|no write)|write nothing.{0,70}covered)`},
		{"no duplicate fallback on lookup error", `(?i)(ambiguous|failed lookup|failed read).{0,180}(no duplicate|duplicate save|duplicate fallback)`},
		{"failed read is not absence", `(?i)failed (read|lookup).{0,60}not.{0,40}(absence|new topic)`},
		{"suggested key is not identity", `(?i)(suggested|suggestion).{0,80}(key|topic_key).{0,80}(hint|not proof)`},
		{"project and scope separation", `(?i)(never|do not).{0,40}(merge|overwrite).{0,60}projects?.{0,30}scopes?`},
		{"significant closure or handoff", `(?i)significant.{0,80}(explicit.{0,40}(clos|handoff)|clos.{0,40}handoff)`},
		{"ordinary completion is not closure", `(?i)ordinary task completion.{0,100}(does not|requires no)`},
		{"honest unavailable evidence", `(?i)((unavailable|failed).{0,100}(evidence|proof).{0,100}(honest|report|claim)|honest.{0,100}(unavailable|failed).{0,100}(evidence|proof))`},
		{"no blind repeated writes", `(?i)(do not|never|no).{0,40}blind.{0,50}(repeat|retr|writ)`},
		{"delivery despite bookkeeping failure", `(?i)If memory work fails or needs follow-up, still send the answer`},
	} {
		if !regexp.MustCompile(rule.pattern).MatchString(text) {
			t.Errorf("selective cadence missing %s", rule.name)
		}
	}
	for _, obsolete := range []string{
		"after EVERY task", "after any of these", "call mem_save NOW",
		"FIRST message references the project", "Before saying done/listo",
		"When completing a task or subtask, include", "Before ending a session or saying",
	} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(obsolete)) {
			t.Errorf("universal cadence survives: %q", obsolete)
		}
	}
}

func TestMemoryProtocolHookRecoveryAndLifecyclePreserved(t *testing.T) {
	for _, host := range []string{"claude-code", "codex"} {
		for _, hook := range []string{"session-start.sh", "post-compaction.sh"} {
			t.Run(host+"/"+hook, func(t *testing.T) {
				text := readMemoryContract(t, filepath.Join("../../plugin", host, "scripts", hook))
				for _, required := range []string{"Summary persistence does not end the session", "SessionEnd hook owns lifecycle closure", "SubagentStop hook", "not automatically captured", "alone does not guarantee storage"} {
					if !strings.Contains(text, required) {
						t.Errorf("host-owned safety contract missing %q", required)
					}
				}
				if hook == "post-compaction.sh" {
					if !regexp.MustCompile(`(?s)FIRST: Call mem_session_summary.*THEN: Call mem_context.*call mem_search.*Only THEN continue`).MatchString(text) {
						t.Error("compaction summary-first progressive recovery was lost")
					}
				}
			})
		}
	}
}

func TestMemoryProtocolCanonicalOwnership(t *testing.T) {
	text := readMemoryContract(t, "../../DOCS.md")
	for _, path := range []string{"internal/setup/setup.go", "plugin/opencode/engram.ts", "internal/setup/plugins/opencode/engram.ts", "skills/memory-protocol/SKILL.md"} {
		if !strings.Contains(text, path) {
			t.Errorf("canonical ownership omits source %q", path)
		}
	}
	for _, pattern := range []string{
		`(?is)canonical.{0,100}(source|contract)`,
		`(?is)(repository-owned|repo-owned).{0,180}(consumer|guidance|copies)`,
		`(?is)external.{0,100}harness.{0,180}(not|outside|independent)`,
	} {
		if !regexp.MustCompile(pattern).MatchString(text) {
			t.Errorf("canonical ownership missing %q", pattern)
		}
	}
}
