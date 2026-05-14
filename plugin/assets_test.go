package plugin_test

import (
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
