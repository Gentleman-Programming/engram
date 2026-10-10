package scripts

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const benchmarkHeader = `goos: linux
goarch: amd64
pkg: github.com/Gentleman-Programming/engram/internal/store
cpu: test-cpu
`

func TestPerfRatchetCompare(t *testing.T) {
	shell := perfRatchetShell()
	if shell == "" {
		t.Skip("a usable bash installation is required to test the shell ratchet")
	}

	for _, tt := range []struct {
		name       string
		baseline   string
		candidate  string
		benchstat  string
		wantErr    bool
		wantOutput string
	}{
		{
			name:       "rejects significant regression",
			baseline:   benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v3/", 1) + "BenchmarkSearch_Hit-8\t1\t200 ns/op\n",
			benchstat:  "BenchmarkSearch_Hit-8  100 ns/op  200 ns/op  +100.00%  (p=0.002 n=10)\n",
			wantErr:    true,
			wantOutput: "PERFORMANCE REGRESSIONS",
		},
		{
			name:       "rejects empty baseline",
			baseline:   "",
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v3/", 1) + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			benchstat:  "",
			wantErr:    true,
			wantOutput: "requires non-empty baseline and candidate",
		},
		{
			name:       "rejects empty candidate",
			baseline:   benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			candidate:  "",
			benchstat:  "",
			wantErr:    true,
			wantOutput: "requires non-empty baseline and candidate",
		},
		{
			name:       "normalizes module package header on both inputs",
			baseline:   benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v3/", 1) + "BenchmarkSearch_Hit-8\t1\t105 ns/op\n",
			benchstat:  "BenchmarkSearch_Hit-8  100 ns/op  105 ns/op  +5.00%  (p=0.002 n=10)\n",
			wantOutput: "no statistically significant",
		},
		{
			name:       "normalizes package header across the v2 to v3 module migration",
			baseline:   strings.Replace(benchmarkHeader, "/engram/", "/engram/v2/", 1) + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v3/", 1) + "BenchmarkSearch_Hit-8\t1\t105 ns/op\n",
			benchstat:  "BenchmarkSearch_Hit-8  100 ns/op  105 ns/op  +5.00%  (p=0.002 n=10)\n",
			wantOutput: "no statistically significant",
		},
		{
			name:       "rejects unmatched benchmark sets",
			baseline:   benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v3/", 1) + "BenchmarkSearchContext_Hit-8\t1\t100 ns/op\n",
			benchstat:  "",
			wantErr:    true,
			wantOutput: "benchmark sets do not match",
		},
		{
			name:       "rejects separate configuration tables",
			baseline:   benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			candidate:  "goos: darwin\n" + strings.TrimPrefix(strings.Replace(benchmarkHeader, "/engram/", "/engram/v3/", 1), "goos: linux\n") + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			benchstat:  "",
			wantErr:    true,
			wantOutput: "configurations do not match",
		},
		{
			name:       "rejects report without paired rows",
			baseline:   benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v3/", 1) + "BenchmarkSearch_Hit-8\t1\t105 ns/op\n",
			benchstat:  "name old time/op new time/op delta\nBenchmarkSearch_Hit-8 100 ns/op 105 ns/op +5.00%\n",
			wantErr:    true,
			wantOutput: "does not pair every expected benchmark",
		},
		{
			name:       "rejects partially paired report",
			baseline:   benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\nBenchmarkScanProject_Page5000-8\t1\t100 ns/op\n",
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v3/", 1) + "BenchmarkSearch_Hit-8\t1\t105 ns/op\nBenchmarkScanProject_Page5000-8\t1\t105 ns/op\n",
			benchstat:  "BenchmarkSearch_Hit-8 100 ns/op 105 ns/op +5.00% (p=0.002 n=10)\n",
			wantErr:    true,
			wantOutput: "does not pair every expected benchmark",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			baseline := writeRatchetFixture(t, dir, "baseline.txt", tt.baseline)
			candidate := writeRatchetFixture(t, dir, "candidate.txt", tt.candidate)
			benchstat := writeRatchetFixture(t, dir, "benchstat", "#!/usr/bin/env bash\nprintf '%s' "+shellQuote(tt.benchstat)+"\n")
			if err := os.Chmod(benchstat, 0o755); err != nil {
				t.Fatalf("chmod fake benchstat: %v", err)
			}

			cmd := exec.Command(shell, "perf-ratchet.sh", "--compare", baseline, candidate)
			cmd.Dir = "."
			cmd.Env = append(os.Environ(), "PERF_RATCHET_BENCHSTAT="+benchstat)
			output, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantErr {
				t.Fatalf("perf-ratchet error = %v, wantErr %t\n%s", err, tt.wantErr, output)
			}
			if !strings.Contains(string(output), tt.wantOutput) {
				t.Fatalf("perf-ratchet output = %q, want %q", output, tt.wantOutput)
			}
		})
	}
}

func TestPerfRatchetUsageAndEnvironment(t *testing.T) {
	shell := perfRatchetShell()
	if shell == "" {
		t.Skip("a usable bash installation is required to test the shell ratchet")
	}

	tests := []struct {
		name       string
		args       []string
		env        []string
		wantOutput string
		wantErr    bool
	}{
		{
			name:       "rejects unknown flag",
			args:       []string{"--unknown"},
			wantOutput: "Usage: scripts/perf-ratchet.sh",
			wantErr:    true,
		},
		{
			name:       "rejects --against missing argument",
			args:       []string{"--against"},
			wantOutput: "Usage: scripts/perf-ratchet.sh",
			wantErr:    true,
		},
		{
			name:       "rejects --against extra argument",
			args:       []string{"--against", "HEAD", "extra"},
			wantOutput: "Usage: scripts/perf-ratchet.sh",
			wantErr:    true,
		},
		{
			name:       "rejects --compare missing arguments",
			args:       []string{"--compare", "only-one"},
			wantOutput: "Usage: scripts/perf-ratchet.sh",
			wantErr:    true,
		},
		{
			name:       "rejects --bootstrap extra argument",
			args:       []string{"--bootstrap", "extra"},
			wantOutput: "Usage: scripts/perf-ratchet.sh",
			wantErr:    true,
		},
		{
			name:       "rejects --update extra argument",
			args:       []string{"--update", "extra"},
			wantOutput: "Usage: scripts/perf-ratchet.sh",
			wantErr:    true,
		},
		{
			name:       "rejects non-positive PERF_RATCHET_COUNT",
			args:       []string{"--bootstrap"},
			env:        []string{"PERF_RATCHET_COUNT=0"},
			wantOutput: "PERF_RATCHET_COUNT must be a positive integer",
			wantErr:    true,
		},
		{
			name:       "rejects negative PERF_RATCHET_THRESHOLD",
			args:       []string{"--bootstrap"},
			env:        []string{"PERF_RATCHET_THRESHOLD=-5"},
			wantOutput: "PERF_RATCHET_THRESHOLD must be a non-negative number",
			wantErr:    true,
		},
		{
			name:       "rejects invalid PERF_RATCHET_BOOTSTRAP",
			args:       []string{"--bootstrap"},
			env:        []string{"PERF_RATCHET_BOOTSTRAP=2"},
			wantOutput: "PERF_RATCHET_BOOTSTRAP must be 0 or 1",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmdArgs := append([]string{"perf-ratchet.sh"}, tt.args...)
			cmd := exec.Command(shell, cmdArgs...)
			cmd.Dir = "."
			var baseEnv []string
			for _, e := range os.Environ() {
				if !strings.HasPrefix(e, "PERF_RATCHET_") {
					baseEnv = append(baseEnv, e)
				}
			}
			cmd.Env = append(baseEnv, tt.env...)
			output, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantErr {
				t.Fatalf("perf-ratchet error = %v, wantErr %t\n%s", err, tt.wantErr, output)
			}
			if !strings.Contains(string(output), tt.wantOutput) {
				t.Fatalf("perf-ratchet output = %q, want %q", output, tt.wantOutput)
			}
		})
	}
}

func TestPerfRatchetBootstrap(t *testing.T) {
	shell := perfRatchetShell()
	if shell == "" {
		t.Skip("a usable bash installation is required to test the shell ratchet")
	}

	matchingBenches := benchmarkHeader +
		"BenchmarkScanProject_Page5000-8\t1\t100 ns/op\n" +
		"BenchmarkSearch_AllMode_Hit-8\t1\t100 ns/op\n" +
		"BenchmarkSearch_AnyMode_Hit-8\t1\t100 ns/op\n" +
		"BenchmarkSearchContext_AllMode_Hit-8\t1\t100 ns/op\n" +
		"BenchmarkSearch_Limit20-8\t1\t100 ns/op\n" +
		"BenchmarkSearch_NoHit-8\t1\t100 ns/op\n" +
		"BenchmarkSearch_TypeFilter-8\t1\t100 ns/op\n"

	tests := []struct {
		name       string
		fakeOutput string
		wantErr    bool
		wantOutput string
	}{
		{
			name:       "accepts matching benchmark suite",
			fakeOutput: matchingBenches,
			wantErr:    false,
			wantOutput: "validated its successor against the versioned baseline",
		},
		{
			name:       "rejects mismatched benchmark suite",
			fakeOutput: benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			wantErr:    true,
			wantOutput: "bootstrap benchmark suite does not match the versioned baseline",
		},
		{
			name:       "rejects empty benchmark output",
			fakeOutput: "",
			wantErr:    true,
			wantOutput: "bootstrap requires non-empty versioned baseline and candidate benchmark output",
		},
		{
			name:       "rejects non-empty benchmark output without benchmark rows",
			fakeOutput: benchmarkHeader + "PASS\n",
			wantErr:    true,
			wantOutput: "bootstrap found no benchmark rows in the versioned baseline or candidate output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			binDir := filepath.Join(dir, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatalf("mkdir bin: %v", err)
			}

			// Create a fake `go` script that outputs tt.fakeOutput when invoked as `go test ...`
			fakeGo := filepath.Join(binDir, "go")
			script := "#!/usr/bin/env bash\nprintf '%s' " + shellQuote(tt.fakeOutput) + "\n"
			if err := os.WriteFile(fakeGo, []byte(script), 0o755); err != nil {
				t.Fatalf("write fake go: %v", err)
			}

			cmd := exec.Command(shell, "perf-ratchet.sh", "--bootstrap")
			cmd.Dir = "."
			var env []string
			for _, e := range os.Environ() {
				if !strings.HasPrefix(e, "PATH=") && !strings.HasPrefix(e, "PERF_RATCHET_") {
					env = append(env, e)
				}
			}
			cmd.Env = append(env, "PATH="+binDir+string(filepath.ListSeparator)+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantErr {
				t.Fatalf("perf-ratchet error = %v, wantErr %t\n%s", err, tt.wantErr, output)
			}
			if !strings.Contains(string(output), tt.wantOutput) {
				t.Fatalf("perf-ratchet output = %q, want %q", output, tt.wantOutput)
			}
		})
	}
}

func TestPerfRatchetAgainst(t *testing.T) {
	one := benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n"
	two := one + "BenchmarkScanProject_Page5000-8\t1\t100 ns/op\n"
	const passed = "no statistically significant performance regression beyond +25%\n"
	const bootstrap = "bootstrap: reference ratchet-reference lacks the complete benchmark suite; validated its successor against the versioned baseline and intentionally skipped cross-host timing comparison\n"
	for _, tt := range []struct {
		name, reference, candidate, baseline, diagnostic, diff string
		bootstrap, compared                                    bool
		fail                                                   string
		code                                                   int
		stdout                                                 string
	}{
		{name: "compares both revisions", reference: one, candidate: one, compared: true, stdout: passed},
		{name: "equal suites still compare with bootstrap enabled", reference: one, candidate: one, bootstrap: true, compared: true, stdout: passed},
		{name: "bootstraps a strict subset across host configurations", reference: one, candidate: two, bootstrap: true, stdout: bootstrap},
		{name: "bootstraps an empty reference suite", reference: benchmarkHeader, candidate: two, bootstrap: true, stdout: bootstrap},
		{
			name: "rejects a subset without bootstrap permission", reference: one, candidate: two, code: 1,
			diagnostic: "perf ratchet benchmark sets do not match; refusing a vacuous comparison\n",
			diff:       "@@ -1 +1,2 @@\n+ScanProject_Page5000\n Search_Hit\n",
		},
		{
			name: "rejects renamed reference benchmarks", reference: strings.ReplaceAll(one, "Search_Hit", "Old_Hit"), candidate: one, bootstrap: true, code: 1,
			diagnostic: "perf ratchet benchmark sets do not match; refusing a vacuous comparison\n",
			diff:       "@@ -1 +1 @@\n-Old_Hit\n+Search_Hit\n",
		},
		{
			name: "rejects different same-runner configurations", reference: one, candidate: strings.ReplaceAll(one, "test-cpu", "other-cpu"), bootstrap: true, code: 1,
			diagnostic: "perf ratchet benchmark configurations do not match after package normalization; refusing separate benchstat tables\n",
			diff:       "@@ -1,4 +1,4 @@\n-cpu: test-cpu\n+cpu: other-cpu\n goarch: amd64\n goos: linux\n pkg: github.com/Gentleman-Programming/engram/v3/internal/store\n",
		},
		{
			name: "rejects bootstrap outside the versioned suite", reference: one, candidate: two, baseline: one, bootstrap: true, code: 1,
			diagnostic: "bootstrap benchmark suite does not match the versioned baseline\n",
			diff:       "@@ -1 +1,2 @@\n+ScanProject_Page5000\n Search_Hit\n",
		},
		{name: "rejects an empty bootstrap baseline", reference: one, candidate: two, baseline: "empty", bootstrap: true, code: 1, diagnostic: "bootstrap requires non-empty versioned baseline and candidate benchmark output\n"},
		{name: "cleans up after reference benchmark failure", reference: one, candidate: one, fail: "reference", code: 7, diagnostic: "fixture benchmark failed\n"},
		{name: "cleans up after candidate benchmark failure", reference: one, candidate: one, fail: "candidate", code: 7, diagnostic: "fixture benchmark failed\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			baseline := tt.baseline
			switch baseline {
			case "":
				// Bootstrap validates identities, not the baseline-producing host.
				baseline = strings.ReplaceAll(tt.candidate, "test-cpu", "baseline-cpu")
			case "empty":
				baseline = ""
			}
			f := newPerfRatchetOperation(t, tt.reference, tt.candidate, baseline)
			env := []string{"RATCHET_TEST_FAIL=" + tt.fail}
			if tt.bootstrap {
				env = append(env, "PERF_RATCHET_BOOTSTRAP=1")
			}
			stdout, stderr, code := f.run(t, env, "--against", "ratchet-reference")
			wantStderr := "Preparing worktree (detached HEAD " + f.revision + ")\n" + tt.diagnostic
			if tt.diff != "" {
				wantStderr += "--- baseline\n+++ candidate\n" + tt.diff
			}
			if stdout != tt.stdout || normalizePerfRatchetDiff(stderr) != wantStderr || code != tt.code {
				t.Fatalf("exit=%d stdout=%q stderr=%q; want exit=%d stdout=%q stderr=%q", code, stdout, stderr, tt.code, tt.stdout, wantStderr)
			}
			f.assertFile(t, ".perf-baseline.txt", baseline)
			calls := "reference\n"
			if tt.fail != "reference" {
				calls += "candidate\n"
			}
			f.assertFile(t, "benchmark-calls", calls)
			if tt.compared {
				f.assertFile(t, "benchstat-calls", "benchstat\n")
				f.assertFile(t, "compared-old", strings.ReplaceAll(tt.reference, "/engram/", "/engram/v3/"))
				f.assertFile(t, "compared-new", strings.ReplaceAll(tt.candidate, "/engram/", "/engram/v3/"))
			} else if _, err := os.Stat(filepath.Join(f.root, "benchstat-calls")); !os.IsNotExist(err) {
				t.Fatalf("unexpected benchstat execution: %v", err)
			}
			f.assertRepositoryUnchanged(t)
		})
	}
}

func TestPerfRatchetUpdate(t *testing.T) {
	baseline := benchmarkHeader + "BenchmarkOld_Hit-8\t1\t200 ns/op\n"
	candidate := strings.ReplaceAll(benchmarkHeader, "/engram/", "/engram/v2/") + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n"
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("benchmark_failure_%t", fail), func(t *testing.T) {
			f := newPerfRatchetOperation(t, baseline, candidate, baseline)
			env := []string{"PERF_RATCHET_COUNT=3"}
			if fail {
				env = append(env, "RATCHET_TEST_FAIL=candidate")
			}
			stdout, stderr, code := f.run(t, env, "--update")
			wantStdout := "updated " + f.shellRoot + "/.perf-baseline.txt with 3 samples per benchmark\n"
			wantStderr, wantCode, wantBaseline := "", 0, strings.ReplaceAll(candidate, "/engram/v2/", "/engram/v3/")
			if fail {
				wantStdout, wantStderr, wantCode, wantBaseline = "", "fixture benchmark failed\n", 7, baseline
			}
			if stdout != wantStdout || stderr != wantStderr || code != wantCode {
				t.Fatalf("exit=%d stdout=%q stderr=%q; want exit=%d stdout=%q stderr=%q", code, stdout, stderr, wantCode, wantStdout, wantStderr)
			}
			f.assertFile(t, ".perf-baseline.txt", wantBaseline)
			f.assertFile(t, "benchmark-calls", "candidate\n")
			if _, err := os.Stat(filepath.Join(f.root, "benchstat-calls")); !os.IsNotExist(err) {
				t.Fatalf("update must not invoke benchstat: %v", err)
			}
			if fail {
				f.assertRepositoryUnchanged(t)
			} else if got := f.git(t, "status", "--porcelain"); got != " M .perf-baseline.txt\n" {
				t.Fatalf("update changed unexpected tracked files: %q", got)
			}
			f.assertWorktreeCleanup(t)
		})
	}
}

// Git and the production script are real; only timing-dependent executables are fixtures.
type perfRatchetOperation struct {
	root, shell, shellRoot, revision, head string
	env                                    []string
}

func newPerfRatchetOperation(t *testing.T, reference, candidate, baseline string) *perfRatchetOperation {
	t.Helper()
	shell := perfRatchetShell()
	if shell == "" {
		t.Skip("a usable bash installation is required to test the shell ratchet")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required to test detached benchmark worktrees")
	}
	f := &perfRatchetOperation{root: t.TempDir(), shell: shell}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "PERF_RATCHET_") && !strings.HasPrefix(e, "RATCHET_TEST_") && !strings.HasPrefix(e, "GIT_") && !strings.HasPrefix(e, "BASH_ENV=") && !strings.HasPrefix(e, "ENV=") {
			f.env = append(f.env, e)
		}
	}
	f.env = append(f.env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "LC_ALL=C", "PERF_RATCHET_COUNT=2", "PERF_RATCHET_THRESHOLD=25", "PERF_RATCHET_BOOTSTRAP=0")
	for _, dir := range []string{"scripts", "bin"} {
		if err := os.Mkdir(filepath.Join(f.root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile("perf-ratchet.sh")
	if err != nil {
		t.Fatal(err)
	}
	writeRatchetFixture(t, f.root, "scripts/perf-ratchet.sh", string(data))
	writeRatchetFixture(t, f.root, ".perf-baseline.txt", baseline)
	writeRatchetFixture(t, f.root, ".gitignore", "bin/\n*.bench\nbenchmark-calls\nbenchstat-calls\ncompared-*\n")
	writeRatchetFixture(t, f.root, "reference.bench", reference)
	writeRatchetFixture(t, f.root, "candidate.bench", candidate)
	writeRatchetFixture(t, f.root, "revision.txt", "reference\n")
	f.git(t, "-c", "init.templateDir=", "init", "-q", "-b", "main")
	f.git(t, "config", "core.autocrlf", "false")
	f.git(t, "add", ".")
	f.git(t, "-c", "user.name=Ratchet Test", "-c", "user.email=ratchet@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	f.git(t, "tag", "ratchet-reference")
	writeRatchetFixture(t, f.root, "revision.txt", "candidate\n")
	f.git(t, "add", "revision.txt")
	f.git(t, "-c", "user.name=Ratchet Test", "-c", "user.email=ratchet@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "candidate")
	f.head = f.git(t, "rev-parse", "HEAD")
	f.revision = strings.TrimSpace(f.git(t, "rev-parse", "--short", "ratchet-reference"))
	cmd := exec.Command(shell, "-c", "pwd")
	cmd.Dir, cmd.Env = f.root, f.env
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	f.shellRoot = strings.TrimSpace(string(output))
	f.executable(t, "go", `[[ "$*" == "test -run ^$ -bench fixture -benchtime=1s -count $PERF_RATCHET_COUNT ./internal/store" ]] || exit 99
case "$PWD" in
  "$RATCHET_TEST_ROOT") lane=candidate ;;
  "$RATCHET_TEST_ROOT"/.perf-ratchet-*) lane=reference ;;
  *) exit 98 ;;
esac
[[ "$(cat revision.txt)" == "$lane" ]] || exit 96
printf '%s\n' "$lane" >> "$RATCHET_TEST_ROOT/benchmark-calls"
if [[ "${RATCHET_TEST_FAIL:-}" == "$lane" ]]; then
  printf 'fixture benchmark failed\n' >&2
  exit 7
fi
cat "$RATCHET_TEST_ROOT/$lane.bench"
`)
	f.executable(t, "benchstat", `[[ $# == 2 ]] || exit 97
printf 'benchstat\n' >> "$RATCHET_TEST_ROOT/benchstat-calls"
cp "$1" "$RATCHET_TEST_ROOT/compared-old"
cp "$2" "$RATCHET_TEST_ROOT/compared-new"
printf 'BenchmarkSearch_Hit-8 100 ns/op 100 ns/op +0.00%% (p=1.000 n=2)\n'
`)
	return f
}

func (f *perfRatchetOperation) executable(t *testing.T, name, body string) {
	t.Helper()
	path := writeRatchetFixture(t, filepath.Join(f.root, "bin"), name, "#!/usr/bin/env bash\nset -eu\n"+body)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f *perfRatchetOperation) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = f.root, f.env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func (f *perfRatchetOperation) run(t *testing.T, env []string, args ...string) (string, string, int) {
	t.Helper()
	wrapper := `export RATCHET_TEST_ROOT="$PWD"
export PATH="$PWD/bin:$PATH" PERF_RATCHET_BENCHSTAT="$PWD/bin/benchstat" PERF_RATCHET_BENCHES=fixture
exec "$BASH" scripts/perf-ratchet.sh "$@"`
	cmd := exec.Command(f.shell, append([]string{"-c", wrapper, "ratchet"}, args...)...)
	cmd.Dir, cmd.Env = f.root, append(append([]string{}, f.env...), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return stdout.String(), stderr.String(), code
}

func (f *perfRatchetOperation) assertFile(t *testing.T, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, name))
	if err != nil || string(data) != want {
		t.Fatalf("%s = %q, err=%v; want %q", name, data, err, want)
	}
}

func (f *perfRatchetOperation) assertRepositoryUnchanged(t *testing.T) {
	t.Helper()
	if got := f.git(t, "status", "--porcelain"); got != "" {
		t.Fatalf("ratchet modified repository: %q", got)
	}
	f.assertWorktreeCleanup(t)
}

func (f *perfRatchetOperation) assertWorktreeCleanup(t *testing.T) {
	t.Helper()
	if got := f.git(t, "rev-parse", "HEAD"); got != f.head {
		t.Fatalf("ratchet changed HEAD: %q, want %q", got, f.head)
	}
	if got := f.git(t, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
		t.Fatalf("detached worktree registration leaked: %s", got)
	}
	paths, err := filepath.Glob(filepath.Join(f.root, ".perf-ratchet-*"))
	if err != nil || len(paths) != 0 {
		t.Fatalf("detached worktree directory leaked: %v, err=%v", paths, err)
	}
}

func normalizePerfRatchetDiff(stderr string) string {
	// Diff headers contain temporary paths and timestamps; preserve every other byte.
	lines := strings.SplitAfter(stderr, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "--- ") {
			lines[i] = "--- baseline\n"
		} else if strings.HasPrefix(line, "+++ ") {
			lines[i] = "+++ candidate\n"
		}
	}
	return strings.Join(lines, "")
}

func perfRatchetShell() string {
	candidates := []string{"bash"}
	if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
		candidates = append(candidates, filepath.Join(programFiles, "Git", "bin", "bash.exe"))
	}
	for _, candidate := range candidates {
		if err := exec.Command(candidate, "--version").Run(); err == nil {
			return candidate
		}
	}
	return ""
}

func writeRatchetFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
