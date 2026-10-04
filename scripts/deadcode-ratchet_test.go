package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeadcodeRatchetToolchain(t *testing.T) {
	shell := perfRatchetShell()
	if shell == "" {
		t.Skip("a usable bash installation is required to test the shell ratchet")
	}
	script, err := os.ReadFile("deadcode-ratchet.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, module, mode, want string
		baseline, wantBaseline   string
		override, fail, wantErr  bool
	}{
		{name: "repository minimum", module: "module fixture\ngo 1.25.10\ntoolchain go1.26.1\n", want: "go1.25.10+auto|run golang.org/x/tools/cmd/deadcode@v0.30.0 ./..."},
		{name: "alternate minimum", module: "module fixture\n go 1.24 // minimum\n", want: "go1.24.0+auto|run golang.org/x/tools/cmd/deadcode@v0.30.0 ./..."},
		{name: "modern naming cutoff", module: "go 1.21\n", want: "go1.21.0+auto|run golang.org/x/tools/cmd/deadcode@v0.30.0 ./..."},
		{name: "historical naming cutoff", module: "go 1.20\n", want: "go1.20+auto|run golang.org/x/tools/cmd/deadcode@v0.30.0 ./..."},
		{name: "older historical release", module: "go 1.19\n", want: "go1.19+auto|run golang.org/x/tools/cmd/deadcode@v0.30.0 ./..."},
		{name: "explicit zero patch", module: "go 1.21.0\n", want: "go1.21.0+auto|run golang.org/x/tools/cmd/deadcode@v0.30.0 ./..."},
		{name: "explicit historical patch", module: "go 1.20.1\n", want: "go1.20.1+auto|run golang.org/x/tools/cmd/deadcode@v0.30.0 ./..."},
		{name: "missing directive", module: "module fixture\ntoolchain go1.26.1\n", want: "valid go directive", wantErr: true},
		{name: "malformed directive", module: "module fixture\ngo go1.25.10\n", want: "valid go directive", wantErr: true},
		{name: "duplicate directive", module: "go 1.25.10\ngo 1.24\n", want: "valid go directive", wantErr: true},
		{name: "missing module", want: "valid go directive", wantErr: true},
		{name: "invalid update", module: "go invalid\n", mode: "--update", want: "valid go directive", wantErr: true},
		{name: "override bypass", module: "go invalid\n", override: true, want: "local|./..."},
		{name: "compare bypass", mode: "--compare", want: "no newly unreachable functions"},
		{name: "default failure", module: "go 1.25.10\n", fail: true, wantErr: true, want: "GOTOOLCHAIN=go1.25.10+auto"},
		{name: "successful update", module: "go 1.25.10\n", mode: "--update", baseline: "internal/store/store.go\tStore.Delete\n", wantBaseline: "internal/store/store.go\tStore.Save\n", want: "go1.25.10+auto|run golang.org/x/tools/cmd/deadcode@v0.30.0 ./..."},
		{name: "update failure", module: "go 1.25.10\n", mode: "--update", fail: true, wantErr: true, want: "GOTOOLCHAIN=go1.25.10+auto"},
		{name: "override update failure", mode: "--update", override: true, fail: true, wantErr: true, want: "refusing to overwrite the baseline"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			scripts := filepath.Join(dir, "scripts")
			if err := os.Mkdir(scripts, 0o755); err != nil {
				t.Fatal(err)
			}
			path := writeRatchetFixture(t, scripts, "deadcode-ratchet.sh", string(script))
			if tt.module != "" {
				writeRatchetFixture(t, dir, "go.mod", tt.module)
			}
			const debt = "internal/store/store.go\tStore.Save\n"
			initialBaseline := debt
			if tt.baseline != "" {
				initialBaseline = tt.baseline
			}
			wantBaseline := initialBaseline
			if tt.wantBaseline != "" {
				wantBaseline = tt.wantBaseline
			}
			baseline := writeRatchetFixture(t, dir, "baseline.txt", initialBaseline)
			log := filepath.Join(dir, "invocation.txt")
			fake := writeRatchetFixture(t, dir, "go", "#!/usr/bin/env bash\nprintf '%s|%s\\n' \"$GOTOOLCHAIN\" \"$*\" >\"$INVOCATION\"\nif [[ $FAIL_ANALYZER == 1 ]]; then echo 'partial analyzer output'; echo 'underlying toolchain failure' >&2; exit 17; fi\nprintf '%s\\n' 'internal/store/store.go:42:7: unreachable func: Store.Save'\n")
			if err := os.Chmod(fake, 0o755); err != nil {
				t.Fatal(err)
			}
			args := []string{path}
			if tt.mode != "" {
				args = append(args, tt.mode)
			}
			if tt.mode == "--compare" {
				args = append(args, baseline, baseline)
			}
			cmd := exec.Command(shell, args...)
			// Remove inherited settings so these fixtures own every analyzer input.
			for _, env := range os.Environ() {
				if !strings.HasPrefix(env, "GOTOOLCHAIN=") && !strings.HasPrefix(env, "DEADCODE_RATCHET_") && !strings.HasPrefix(env, "PATH=") {
					cmd.Env = append(cmd.Env, env)
				}
			}
			cmd.Env = append(cmd.Env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "GOTOOLCHAIN=local", "DEADCODE_RATCHET_BASELINE="+baseline, "INVOCATION="+log, "FAIL_ANALYZER=0")
			if tt.override {
				cmd.Env = append(cmd.Env, "DEADCODE_RATCHET_ANALYZER="+fake)
			}
			if tt.fail {
				cmd.Env = append(cmd.Env, "FAIL_ANALYZER=1")
			}
			output, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %t\n%s", err, tt.wantErr, output)
			}
			invocation, logErr := os.ReadFile(log)
			if tt.wantErr || tt.mode == "--compare" {
				if !strings.Contains(string(output), tt.want) {
					t.Fatalf("output = %q, want %q", output, tt.want)
				}
				if !tt.fail && !os.IsNotExist(logErr) {
					t.Fatalf("Go unexpectedly invoked: %q (%v)", invocation, logErr)
				}
			} else if logErr != nil || strings.TrimSpace(string(invocation)) != tt.want {
				t.Fatalf("invocation = %q (%v), want %q", invocation, logErr, tt.want)
			}
			if tt.fail {
				if logErr != nil {
					t.Fatalf("analyzer was not invoked: %v", logErr)
				}
				if !strings.Contains(string(output), "underlying toolchain failure") || strings.Contains(string(output), "no newly unreachable functions") {
					t.Fatalf("failure diagnostic = %q", output)
				}
			}
			if tt.mode == "--update" && !tt.wantErr {
				wantOutput := "updated " + baseline + " with deadcode v0.30.0 output\n"
				if string(output) != wantOutput {
					t.Fatalf("output = %q, want %q", output, wantOutput)
				}
			}
			got, err := os.ReadFile(baseline)
			if err != nil || string(got) != wantBaseline {
				t.Fatalf("baseline = %q (%v), want %q", got, err, wantBaseline)
			}
		})
	}
}

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
