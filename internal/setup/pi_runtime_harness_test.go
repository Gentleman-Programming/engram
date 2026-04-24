package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiExtensionRuntimeHarnessDeterministic(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for pi runtime harness")
	}

	scriptPath := filepath.Join("..", "..", "plugin", "pi", "test", "runtime-harness.mjs")
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("stat runtime harness script: %v", err)
	}

	t.Run("source extension", func(t *testing.T) {
		runPiRuntimeHarness(t, nodePath, scriptPath, filepath.Join("..", "..", "plugin", "pi", "extensions", "engram.ts"))
	})

	t.Run("embedded extension", func(t *testing.T) {
		raw, err := piReadFile("plugins/pi/extensions/engram.ts")
		if err != nil {
			t.Fatalf("read embedded pi extension: %v", err)
		}

		tmpPath := filepath.Join(t.TempDir(), "engram-embedded.ts")
		if err := os.WriteFile(tmpPath, raw, 0644); err != nil {
			t.Fatalf("write embedded extension fixture: %v", err)
		}

		runPiRuntimeHarness(t, nodePath, scriptPath, tmpPath)
	})
}

func runPiRuntimeHarness(t *testing.T, nodePath, scriptPath, extensionPath string) {
	t.Helper()

	flagSets := [][]string{{"--experimental-strip-types"}, {"--experimental-transform-types"}}
	var optionErrOutput string

	for _, flags := range flagSets {
		args := append(append([]string{}, flags...), scriptPath, extensionPath)
		cmd := exec.Command(nodePath, args...)
		output, err := cmd.CombinedOutput()
		if err == nil {
			return
		}

		stderr := string(output)
		if strings.Contains(stderr, "bad option") || strings.Contains(stderr, "unknown option") {
			optionErrOutput = stderr
			continue
		}

		t.Fatalf("pi runtime harness failed (%v):\n%s", err, stderr)
	}

	if optionErrOutput != "" {
		t.Skipf("node in this environment does not support TypeScript runtime flags needed by harness:\n%s", optionErrOutput)
	}

	t.Fatal("pi runtime harness did not execute")
}
