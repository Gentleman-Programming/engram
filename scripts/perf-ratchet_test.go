package scripts

import (
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
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v2/", 1) + "BenchmarkSearch_Hit-8\t1\t200 ns/op\n",
			benchstat:  "BenchmarkSearch_Hit-8  100 ns/op  200 ns/op  +100.00%  (p=0.002 n=10)\n",
			wantErr:    true,
			wantOutput: "PERFORMANCE REGRESSIONS",
		},
		{
			name:       "rejects empty baseline",
			baseline:   "",
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v2/", 1) + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
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
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v2/", 1) + "BenchmarkSearch_Hit-8\t1\t105 ns/op\n",
			benchstat:  "BenchmarkSearch_Hit-8  100 ns/op  105 ns/op  +5.00%  (p=0.002 n=10)\n",
			wantOutput: "no statistically significant",
		},
		{
			name:       "rejects unmatched benchmark sets",
			baseline:   benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v2/", 1) + "BenchmarkSearchContext_Hit-8\t1\t100 ns/op\n",
			benchstat:  "",
			wantErr:    true,
			wantOutput: "benchmark sets do not match",
		},
		{
			name:       "rejects separate configuration tables",
			baseline:   benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			candidate:  "goos: darwin\n" + strings.TrimPrefix(strings.Replace(benchmarkHeader, "/engram/", "/engram/v2/", 1), "goos: linux\n") + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			benchstat:  "",
			wantErr:    true,
			wantOutput: "configurations do not match",
		},
		{
			name:       "rejects report without paired rows",
			baseline:   benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\n",
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v2/", 1) + "BenchmarkSearch_Hit-8\t1\t105 ns/op\n",
			benchstat:  "name old time/op new time/op delta\nBenchmarkSearch_Hit-8 100 ns/op 105 ns/op +5.00%\n",
			wantErr:    true,
			wantOutput: "does not pair every expected benchmark",
		},
		{
			name:       "rejects partially paired report",
			baseline:   benchmarkHeader + "BenchmarkSearch_Hit-8\t1\t100 ns/op\nBenchmarkScanProject_Page5000-8\t1\t100 ns/op\n",
			candidate:  strings.Replace(benchmarkHeader, "/engram/", "/engram/v2/", 1) + "BenchmarkSearch_Hit-8\t1\t105 ns/op\nBenchmarkScanProject_Page5000-8\t1\t105 ns/op\n",
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
