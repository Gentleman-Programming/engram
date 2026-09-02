package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/selftest"
	versioncheck "github.com/Gentleman-Programming/engram/v2/internal/version"
)

func TestParseTestArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantSuite  string
		wantQuick  bool
		wantJSON   bool
		wantHelp   bool
		wantErrMsg string
	}{
		{name: "default", wantSuite: selftest.SuiteAll},
		{name: "performance quick JSON", args: []string{"performance", "--quick", "--json"}, wantSuite: selftest.SuitePerformance, wantQuick: true, wantJSON: true},
		{name: "help", args: []string{"--help"}, wantSuite: selftest.SuiteAll, wantHelp: true},
		{name: "unknown suite", args: []string{"load"}, wantErrMsg: "unknown test suite"},
		{name: "multiple suites", args: []string{"reliability", "performance"}, wantErrMsg: "only one test suite"},
		{name: "unknown flag", args: []string{"--keep"}, wantErrMsg: "unknown test flag"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			options, jsonOutput, help, err := parseTestArgs(tc.args)
			if tc.wantErrMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Fatalf("parseTestArgs(%v) error = %v, want %q", tc.args, err, tc.wantErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTestArgs(%v): %v", tc.args, err)
			}
			if options.Suite != tc.wantSuite || options.Quick != tc.wantQuick || jsonOutput != tc.wantJSON || help != tc.wantHelp {
				t.Fatalf("parseTestArgs(%v) = (%#v, %t, %t), want suite=%q quick=%t json=%t help=%t", tc.args, options, jsonOutput, help, tc.wantSuite, tc.wantQuick, tc.wantJSON, tc.wantHelp)
			}
		})
	}
}

func TestCmdTestJSONShapeAndSuiteSelection(t *testing.T) {
	oldRunSelfTest := runSelfTest
	var gotOptions selftest.Options
	runSelfTest = func(options selftest.Options) selftest.Report {
		gotOptions = options
		return selftest.Report{
			SchemaVersion: "engram-self-test/v1",
			Suite:         options.Suite,
			Quick:         options.Quick,
			Passed:        true,
			Scenarios: []selftest.Scenario{{
				Name: "store_search", Suite: selftest.SuitePerformance, Passed: true,
				Metrics: map[string]float64{"operations": 20, "throughput_ops_per_second": 100},
			}},
		}
	}
	t.Cleanup(func() { runSelfTest = oldRunSelfTest })

	stdout, stderr := captureOutput(t, func() {
		if code := cmdTest([]string{"performance", "--quick", "--json"}); code != testExitSuccess {
			t.Fatalf("cmdTest exit code = %d, want %d", code, testExitSuccess)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if gotOptions.Suite != selftest.SuitePerformance || !gotOptions.Quick {
		t.Fatalf("run options = %#v", gotOptions)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("JSON output %q: %v", stdout, err)
	}
	for _, key := range []string{"schema_version", "suite", "quick", "passed", "duration_ms", "scenarios"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("JSON report missing top-level key %q: %#v", key, got)
		}
	}
	if got["schema_version"] != "engram-self-test/v1" || got["suite"] != selftest.SuitePerformance || got["passed"] != true {
		t.Fatalf("unexpected JSON report: %#v", got)
	}
}

func TestCmdTestFailureAndUsageExitCodes(t *testing.T) {
	oldRunSelfTest := runSelfTest
	runSelfTest = func(selftest.Options) selftest.Report {
		return selftest.Report{Suite: selftest.SuiteAll, Scenarios: []selftest.Scenario{{Name: "store_search", Suite: selftest.SuitePerformance, Error: "injected failure"}}}
	}
	t.Cleanup(func() { runSelfTest = oldRunSelfTest })

	stdout, stderr := captureOutput(t, func() {
		if code := cmdTest(nil); code != testExitFailure {
			t.Fatalf("failed self-test exit code = %d, want %d", code, testExitFailure)
		}
	})
	if stderr != "" || !strings.Contains(stdout, "FAIL") || !strings.Contains(stdout, "injected failure") {
		t.Fatalf("failed self-test output = (%q, %q)", stdout, stderr)
	}

	stdout, stderr = captureOutput(t, func() {
		if code := cmdTest([]string{"--keep"}); code != testExitUsage {
			t.Fatalf("invalid usage exit code = %d, want %d", code, testExitUsage)
		}
	})
	if !strings.Contains(stderr, "unknown test flag") || !strings.Contains(stdout, "usage: engram test") {
		t.Fatalf("invalid usage output = (%q, %q)", stdout, stderr)
	}
}

func TestCmdTestHelpDoesNotRunScenarios(t *testing.T) {
	oldRunSelfTest := runSelfTest
	runSelfTest = func(selftest.Options) selftest.Report {
		t.Fatal("self-test runner must not run for help")
		return selftest.Report{}
	}
	t.Cleanup(func() { runSelfTest = oldRunSelfTest })

	stdout, stderr := captureOutput(t, func() {
		if code := cmdTest([]string{"--help"}); code != testExitSuccess {
			t.Fatalf("help exit code = %d", code)
		}
	})
	if stderr != "" || !strings.Contains(stdout, "usage: engram test") {
		t.Fatalf("help output = (%q, %q)", stdout, stderr)
	}
}

func TestMainTestDispatchDoesNotTouchConfiguredDataOrCheckUpdates(t *testing.T) {
	dataDir := t.TempDir()
	sentinel := filepath.Join(dataDir, "must-remain-untouched")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	t.Setenv("ENGRAM_DATA_DIR", dataDir)
	withArgs(t, "engram", "test", "--quick")

	oldRunSelfTest, oldCheckForUpdates := runSelfTest, checkForUpdates
	runSelfTest = func(options selftest.Options) selftest.Report {
		if !options.Quick {
			t.Fatal("quick option was not forwarded")
		}
		return selftest.Report{Suite: selftest.SuiteAll, Quick: true, Passed: true}
	}
	checkForUpdates = func(string) versioncheck.CheckResult {
		t.Fatal("update check must not run before self-test dispatch")
		return versioncheck.CheckResult{}
	}
	t.Cleanup(func() {
		runSelfTest, checkForUpdates = oldRunSelfTest, oldCheckForUpdates
	})

	_, stderr := captureOutput(t, main)
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("read configured data directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(sentinel) {
		t.Fatalf("configured data directory changed: %v", entries)
	}
}
