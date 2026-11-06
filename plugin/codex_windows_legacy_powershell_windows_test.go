//go:build windows

package plugin_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexWindowsLegacyPowerShellCancellation(t *testing.T) {
	const modeKey = "ENGRAM_TEST_LEGACY_POWERSHELL_DEADLINE"
	if os.Getenv(modeKey) == "1" {
		script := filepath.Join(t.TempDir(), "owned-deadline.ps1")
		// Sleep in PowerShell itself; cancellation creates no fixture child.
		if err := os.WriteFile(script, []byte(`[Console]::Out.Write('owned-started'); [Console]::Error.Write('owned-stderr'); [Threading.Thread]::Sleep(20000)`), 0600); err != nil {
			t.Fatal(err)
		}
		_, _, code := runCodexWindowsPowerShell(t, script, "", nil, 5*time.Second)
		t.Fatalf("deadline helper returned ordinary exit=%d", code)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, self, "-test.run=^TestCodexWindowsLegacyPowerShellCancellation$", "-test.v")
	child.Env = append(os.Environ(), modeKey+"=1")
	output, err := child.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("diagnostic subprocess canceled: %v", ctx.Err())
	}
	for _, required := range []string{"run PowerShell adapter canceled: context deadline exceeded", "elapsed:", "process error:", "stdout=", "stderr="} {
		if err == nil || !strings.Contains(string(output), required) {
			t.Fatalf("error=%v output=%s, missing %q", err, output, required)
		}
	}
	t.Logf("observed cancellation diagnostic:\n%s", output)
}

func TestCodexWindowsLegacyPowerShellExitCodes(t *testing.T) {
	for _, want := range []int{0, 1, 23} {
		t.Run(fmt.Sprint(want), func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "owned-exit.ps1")
			source := fmt.Sprintf(`[Console]::Out.Write('owned-stdout'); [Console]::Error.Write('owned-stderr'); exit %d`, want)
			if err := os.WriteFile(script, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, code := runCodexWindowsPowerShell(t, script, "", nil, 5*time.Second)
			if code != want || stdout != "owned-stdout" || stderr != "owned-stderr" {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want%d and exact streams", code, stdout, stderr, want)
			}
		})
	}
}

func TestCodexWindowsSubagentTraceOnCancellation(t *testing.T) {
	const modeKey = "ENGRAM_TEST_SUBAGENT_TRACE_DEADLINE"
	if os.Getenv(modeKey) == "1" {
		source, err := os.ReadFile(filepath.Join(repoRoot(t), "plugin", "codex", "scripts", "subagent-stop.ps1"))
		if err != nil {
			t.Fatal(err)
		}
		const anchor = "$rawInput = [Console]::In.ReadToEnd()"
		if strings.Count(string(source), anchor) != 1 {
			t.Fatal("negative control requires one stdin anchor")
		}
		// This delay is confined to the owned negative-control copy. It
		// creates no child and lets Cleanup observe a partial phase trace.
		source = []byte(strings.Replace(string(source), anchor, anchor+"\n[Console]::Out.Write('owned-started'); [Console]::Error.Write('owned-stderr'); [Threading.Thread]::Sleep(20000)", 1))
		_, adapterPath := newTracedCodexSubagentCopy(t, source, false)
		_, _, code := runCodexWindowsPowerShell(t, adapterPath, "", nil, 5*time.Second)
		t.Fatalf("deadline helper returned ordinary exit=%d", code)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, self, "-test.run=^TestCodexWindowsSubagentTraceOnCancellation$", "-test.v")
	child.Env = append(os.Environ(), modeKey+"=1")
	output, err := child.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("negative-control subprocess canceled: %v", ctx.Err())
	}
	for _, required := range []string{"run PowerShell adapter canceled: context deadline exceeded", "test-owned SubagentStop phases:", " entry elapsed_ms=", " stdin-consumed elapsed_ms="} {
		if err == nil || !strings.Contains(string(output), required) {
			t.Fatalf("error=%v output=%s, missing %q", err, output, required)
		}
	}
	t.Logf("retained partial trace after Fatal:\n%s", output)
}
