package plugin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot returns the absolute path to the repository root by walking up from
// this test file's location.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// plugin/assets_test.go -> up one directory
	return filepath.Dir(filepath.Dir(file))
}

// TestPluginAssetsDoNotLeakSpanishTriggers walks the injected assets of all
// three plugins (claude-code, opencode, pi) and asserts that none of them
// contain Spanish trigger tokens. Those tokens act as register cues in the
// model's context and cause English sessions to drift into Spanish even when
// language-lock rules are in place elsewhere.
func TestPluginAssetsDoNotLeakSpanishTriggers(t *testing.T) {
	root := repoRoot(t)

	bannedTokens := []string{
		`"dale"`,
		`"listo"`,
		`"acordate"`,
		`"qué hicimos"`,
		`"sí, esa"`,
		`"siempre hacé`,
		`"recordar"`,
		`"vamos con eso"`,
		`"me gusta más así"`,
		`"descartemos eso"`,
		`"quiero algo diferente"`,
	}

	targets := []struct {
		pattern string
	}{
		// claude-code: shell scripts and skill markdown files
		{filepath.Join(root, "plugin", "claude-code", "scripts", "*.sh")},
		{filepath.Join(root, "plugin", "claude-code", "skills", "*", "SKILL.md")},
		// opencode: TypeScript plugin adapter
		{filepath.Join(root, "plugin", "opencode", "*.ts")},
		// pi: TypeScript plugin adapter
		{filepath.Join(root, "plugin", "pi", "*.ts")},
	}

	for _, target := range targets {
		matches, err := filepath.Glob(target.pattern)
		if err != nil {
			t.Fatalf("glob %q: %v", target.pattern, err)
		}
		if len(matches) == 0 {
			t.Fatalf("glob %q matched no files — check the path", target.pattern)
		}
		for _, path := range matches {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			rel, _ := filepath.Rel(root, path)
			text := string(content)
			for _, token := range bannedTokens {
				if strings.Contains(text, token) {
					t.Errorf("%s contains banned Spanish trigger token %s", rel, token)
				}
			}
		}
	}
}

// marketplaceJSON is the minimal structure of .claude-plugin/marketplace.json
// needed to extract the version declared for the engram plugin entry.
type marketplaceJSON struct {
	Plugins []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"plugins"`
}

// pluginJSON is the structure of plugin/claude-code/.claude-plugin/plugin.json.
type pluginJSON struct {
	Version string `json:"version"`
}

// TestPluginVersionsMatch asserts that the version declared in
// .claude-plugin/marketplace.json matches the version in
// plugin/claude-code/.claude-plugin/plugin.json.
//
// A mismatch between these two files causes Claude Code to silently skip
// installation or re-download the plugin on every run because it sees the
// cached version as stale.
func TestPluginVersionsMatch(t *testing.T) {
	root := repoRoot(t)

	// Read marketplace.json
	marketplacePath := filepath.Join(root, ".claude-plugin", "marketplace.json")
	marketplaceData, err := os.ReadFile(marketplacePath)
	if err != nil {
		t.Fatalf("cannot read marketplace.json: %v", err)
	}
	var marketplace marketplaceJSON
	if err := json.Unmarshal(marketplaceData, &marketplace); err != nil {
		t.Fatalf("cannot parse marketplace.json: %v", err)
	}

	// Read plugin.json
	pluginPath := filepath.Join(root, "plugin", "claude-code", ".claude-plugin", "plugin.json")
	pluginData, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("cannot read plugin.json: %v", err)
	}
	var plugin pluginJSON
	if err := json.Unmarshal(pluginData, &plugin); err != nil {
		t.Fatalf("cannot parse plugin.json: %v", err)
	}

	// Find the engram plugin entry in marketplace.json
	var marketplaceVersion string
	for _, p := range marketplace.Plugins {
		if p.Name == "engram" {
			marketplaceVersion = p.Version
			break
		}
	}
	if marketplaceVersion == "" {
		t.Fatal("marketplace.json contains no plugin entry named 'engram'")
	}

	if marketplaceVersion != plugin.Version {
		t.Errorf(
			"plugin version mismatch: marketplace.json declares %q but plugin/claude-code/.claude-plugin/plugin.json declares %q — keep them in sync",
			marketplaceVersion,
			plugin.Version,
		)
	}
}
