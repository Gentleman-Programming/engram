package plugin_test

import (
	"os"
	"strings"
	"testing"
)

func TestCodexFixtureCLIReused(t *testing.T) {
	var first string
	t.Run("initial owner", func(t *testing.T) {
		first = buildCodexFixtureCLI(t)
	})
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("fixture did not survive completed subtest: %v", err)
	}
	second := buildCodexFixtureCLI(t)
	if first != second {
		t.Fatalf("fixture rebuilt: %q != %q", first, second)
	}
}

func TestCodexWindowsEndEnvPortIsolation(t *testing.T) {
	t.Setenv("ENGRAM_URL", "http://inherited.invalid:9999")
	t.Setenv("ENGRAM_SOCKET", "inherited-socket")
	t.Setenv("ENGRAM_PORT", "9999")
	const port = "invalid"
	effective := make(map[string]string)
	for _, item := range codexWindowsEndEnv(t, port) {
		key, value, found := strings.Cut(item, "=")
		if found {
			effective[strings.ToUpper(key)] = value
		}
	}
	for key, want := range map[string]string{"ENGRAM_URL": "", "ENGRAM_SOCKET": "", "ENGRAM_PORT": port} {
		if got, present := effective[key]; !present || got != want {
			t.Errorf("effective %s=%q (present=%t), want %q", key, got, present, want)
		}
	}
}
