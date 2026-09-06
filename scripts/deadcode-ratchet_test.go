package scripts

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDeadcodeRatchetCompare(t *testing.T) {
	shell := perfRatchetShell()
	if shell == "" {
		t.Skip("a usable bash installation is required to test the shell ratchet")
	}

	for _, tt := range []struct {
		name, baseline, candidate, wantOutput string
		wantErr                               bool
	}{
		{
			name:       "accepts matching baseline",
			baseline:   "internal/store/store.go\tStore.Save\n",
			candidate:  "internal/store/store.go\tStore.Save\n",
			wantOutput: "no newly unreachable functions",
		},
		{
			name:       "accepts CRLF baseline",
			baseline:   "internal/store/store.go\tStore.Save\r\n",
			candidate:  "internal/store/store.go\tStore.Save\n",
			wantOutput: "no newly unreachable functions",
		},
		{
			name:       "rejects newly unreachable function",
			baseline:   "internal/store/store.go\tStore.Save\n",
			candidate:  "internal/store/store.go\tStore.Delete\ninternal/store/store.go\tStore.Save\n",
			wantErr:    true,
			wantOutput: "NEW UNREACHABLE FUNCTIONS",
		},
		{
			name:       "allows removed baseline function",
			baseline:   "internal/store/store.go\tStore.Delete\ninternal/store/store.go\tStore.Save\n",
			candidate:  "internal/store/store.go\tStore.Save\n",
			wantOutput: "dead-code debt tightened: 1 baseline entries were removed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			baseline := writeRatchetFixture(t, dir, "baseline.txt", tt.baseline)
			candidate := writeRatchetFixture(t, dir, "candidate.txt", tt.candidate)
			output, err := exec.Command(shell, "deadcode-ratchet.sh", "--compare", baseline, candidate).CombinedOutput()
			if (err != nil) != tt.wantErr {
				t.Fatalf("deadcode-ratchet error = %v, wantErr %t\n%s", err, tt.wantErr, output)
			}
			if !strings.Contains(string(output), tt.wantOutput) {
				t.Fatalf("deadcode-ratchet output = %q, want %q", output, tt.wantOutput)
			}
		})
	}
}

func TestDeadcodeRatchetRejectsAnalyzerFailure(t *testing.T) {
	shell := perfRatchetShell()
	if shell == "" {
		t.Skip("a usable bash installation is required to test the shell ratchet")
	}

	dir := t.TempDir()
	baseline := writeRatchetFixture(t, dir, "baseline.txt", "internal/store/store.go\tStore.Save\n")
	analyzer := writeRatchetFixture(t, dir, "deadcode", "#!/usr/bin/env bash\nexit 17\n")
	if err := os.Chmod(analyzer, 0o755); err != nil {
		t.Fatalf("chmod fake analyzer: %v", err)
	}
	cmd := exec.Command(shell, "deadcode-ratchet.sh")
	cmd.Env = append(os.Environ(), "DEADCODE_RATCHET_BASELINE="+baseline, "DEADCODE_RATCHET_ANALYZER="+analyzer)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("deadcode-ratchet unexpectedly passed\n%s", output)
	}
	if !strings.Contains(string(output), "deadcode analyzer failed") {
		t.Fatalf("deadcode-ratchet output = %q, want analyzer failure", output)
	}
}

func TestDeadcodeRatchetRejectsMalformedAnalyzerOutput(t *testing.T) {
	shell := perfRatchetShell()
	if shell == "" {
		t.Skip("a usable bash installation is required to test the shell ratchet")
	}

	dir := t.TempDir()
	baseline := writeRatchetFixture(t, dir, "baseline.txt", "internal/store/store.go\tStore.Save\n")
	analyzer := writeRatchetFixture(t, dir, "deadcode", "#!/usr/bin/env bash\nprintf '%s\\n' 'unexpected analyzer output'\n")
	if err := os.Chmod(analyzer, 0o755); err != nil {
		t.Fatalf("chmod fake analyzer: %v", err)
	}
	cmd := exec.Command(shell, "deadcode-ratchet.sh")
	cmd.Env = append(os.Environ(), "DEADCODE_RATCHET_BASELINE="+baseline, "DEADCODE_RATCHET_ANALYZER="+analyzer)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("deadcode-ratchet unexpectedly passed\n%s", output)
	}
	if !strings.Contains(string(output), "deadcode emitted unrecognized output; refusing an incomplete comparison") {
		t.Fatalf("deadcode-ratchet output = %q, want malformed output refusal", output)
	}
}

func TestDeadcodeRatchetNormalizesAnalyzerOutput(t *testing.T) {
	shell := perfRatchetShell()
	if shell == "" {
		t.Skip("a usable bash installation is required to test the shell ratchet")
	}

	dir := t.TempDir()
	baseline := writeRatchetFixture(t, dir, "baseline.txt", "internal/store/store.go\tStore.Save\n")
	analyzer := writeRatchetFixture(t, dir, "deadcode", "#!/usr/bin/env bash\nprintf '%s\\n' 'internal\\store\\store.go:42:7: unreachable func: Store.Save'\n")
	if err := os.Chmod(analyzer, 0o755); err != nil {
		t.Fatalf("chmod fake analyzer: %v", err)
	}
	cmd := exec.Command(shell, "deadcode-ratchet.sh")
	cmd.Env = append(os.Environ(), "DEADCODE_RATCHET_BASELINE="+baseline, "DEADCODE_RATCHET_ANALYZER="+analyzer)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deadcode-ratchet error = %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "no newly unreachable functions") {
		t.Fatalf("deadcode-ratchet output = %q, want successful normalized comparison", output)
	}
}
