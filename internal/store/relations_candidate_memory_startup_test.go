package store

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"modernc.org/libc"
)

const candidateMemoryCountersEnv = "ENGRAM_TEST_SQLITE_MEMORY_COUNTERS"
const candidateMemorySelectorError = "candidate memory startup: ENGRAM_TEST_SQLITE_MEMORY_COUNTERS=1 requires -test.run=^$ and -test.bench=^BenchmarkCandidateQuerySQLiteMemory$\n"

// This opt-in affects only the Go test binary, never engram production startup.
// Parse the effective flags (including duplicate selectors) before configuring
// SQLite. Ordinary tests/other benchmarks cannot accidentally inherit the mode.
func configureCandidateMemoryBenchmarkStartup() error {
	mode := os.Getenv(candidateMemoryCountersEnv)
	if mode == "" {
		return nil
	}
	if mode != "1" {
		return fmt.Errorf("%s accepts only 1", candidateMemoryCountersEnv)
	}
	if !flag.Parsed() {
		flag.Parse()
	}
	if flag.Lookup("test.run").Value.String() != "^$" || flag.Lookup("test.bench").Value.String() != "^BenchmarkCandidateQuerySQLiteMemory$" {
		return errors.New(strings.TrimSuffix(strings.TrimPrefix(candidateMemorySelectorError, "candidate memory startup: "), "\n"))
	}
	tls := libc.NewTLS()
	defer tls.Close()
	if rc := configureCandidateSQLiteMemstatus(tls, true); rc != 0 {
		return fmt.Errorf("SQLite MEMSTATUS configuration failed before opening fixtures: %d", rc)
	}
	return nil
}

// Check startup through the actual test binary, not a mocked gate. A rejected
// opt-in must exit before tests or fixtures run; default startup still works.
func TestCandidateMemoryBenchmarkStartup(t *testing.T) {
	for _, tc := range []struct {
		name, mode, run, bench string
		exit                   int
		out, errout            string
	}{
		{"default", "", "^TestCandidateQueryBenchmarkMatchesPublicLookup$", "", 0, "PASS\n", ""},
		{"invalid value", "true", "^$", "^$", 2, "", "candidate memory startup: ENGRAM_TEST_SQLITE_MEMORY_COUNTERS accepts only 1\n"},
		{"wrong selector", "1", "^$", "^$", 2, "", candidateMemorySelectorError},
		{"tests selected", "1", "^TestCandidateQueryBenchmarkMatchesPublicLookup$", "^BenchmarkCandidateQuerySQLiteMemory$", 2, "", candidateMemorySelectorError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run="+tc.run, "-test.bench="+tc.bench, "-test.count=1")
			if tc.mode != "" {
				// Rejected scopes need no fixture/benchmark execution, even if the gate regresses.
				cmd.Args = append(cmd.Args, "-test.list=^$")
			}
			for _, entry := range os.Environ() {
				key := strings.ToUpper(strings.SplitN(entry, "=", 2)[0])
				if key != candidateMemoryCountersEnv && key != candidateCounterProbeEnv {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			if tc.mode != "" {
				cmd.Env = append(cmd.Env, candidateMemoryCountersEnv+"="+tc.mode)
			}
			var out, errout bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errout
			runErr := cmd.Run()
			code := 0
			if runErr != nil {
				var exit *exec.ExitError
				if !errors.As(runErr, &exit) {
					t.Fatal(runErr)
				}
				code = exit.ExitCode()
			}
			if code != tc.exit || out.String() != tc.out || errout.String() != tc.errout {
				t.Fatalf("startup: exit=%d stdout=%q stderr=%q, want exit=%d stdout=%q stderr=%q", code, out.String(), errout.String(), tc.exit, tc.out, tc.errout)
			}
		})
	}
}
