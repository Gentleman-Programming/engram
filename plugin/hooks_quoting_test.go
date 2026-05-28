package plugin_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// hooksJSON is the parsed structure of plugin/claude-code/hooks/hooks.json.
type hooksJSON struct {
	Hooks map[string][]hookGroup `json:"hooks"`
}

type hookGroup struct {
	Matcher string     `json:"matcher,omitempty"`
	Hooks   []hookEntry `json:"hooks"`
}

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	Path    string `json:"path,omitempty"`
}

// TestHooksJSONPluginRootIsQuoted loads plugin/claude-code/hooks/hooks.json and
// asserts that every command/path field referencing ${CLAUDE_PLUGIN_ROOT} wraps
// the variable expansion in double quotes so that Windows usernames or any path
// component containing spaces does not split the argument at the space.
//
// Correct form (POSIX shell):  "${CLAUDE_PLUGIN_ROOT}/scripts/foo.sh"
// Broken form:                   ${CLAUDE_PLUGIN_ROOT}/scripts/foo.sh
//
// The test fails (red) when the variable is present but unquoted, and passes
// (green) once every occurrence is properly quoted.
func TestHooksJSONPluginRootIsQuoted(t *testing.T) {
	root := repoRoot(t)
	hooksPath := root + "/plugin/claude-code/hooks/hooks.json"

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("cannot read hooks.json: %v", err)
	}

	var manifest hooksJSON
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("cannot parse hooks.json: %v", err)
	}

	const varName = "${CLAUDE_PLUGIN_ROOT}"
	// The quoted form that must surround the variable when it appears in a
	// command string passed to a POSIX shell.
	const quotedForm = `"${CLAUDE_PLUGIN_ROOT}`

	checked := 0
	for eventName, groups := range manifest.Hooks {
		for gi, group := range groups {
			for hi, entry := range group.Hooks {
				for _, field := range []struct {
					name  string
					value string
				}{
					{"command", entry.Command},
					{"path", entry.Path},
				} {
					if !strings.Contains(field.value, varName) {
						continue
					}
					checked++
					if !strings.Contains(field.value, quotedForm) {
						t.Errorf(
							"hooks[%q][%d].hooks[%d].%s references %s without surrounding double-quotes:\n  got:  %s\n  want: %s...",
							eventName, gi, hi, field.name,
							varName,
							field.value,
							quotedForm,
						)
					}
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("no hook entries reference ${CLAUDE_PLUGIN_ROOT} — test is broken or hooks.json changed")
	}
	t.Logf("checked %d hook command/path field(s) for proper quoting", checked)
}
