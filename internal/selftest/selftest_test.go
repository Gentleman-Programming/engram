package selftest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunQuickAllSuitesPass(t *testing.T) {
	report := Run(Options{Quick: true})
	if !report.Passed {
		t.Fatalf("quick report failed: %#v", report)
	}
	if report.SchemaVersion != "engram-self-test/v1" || report.Suite != SuiteAll || len(report.Scenarios) != 3 {
		t.Fatalf("unexpected report shape: %#v", report)
	}
	for _, scenario := range report.Scenarios {
		if !scenario.Passed || scenario.Name == "" || scenario.Suite == "" {
			t.Fatalf("unexpected scenario: %#v", scenario)
		}
		if scenario.Suite == SuitePerformance && (scenario.Metrics["operations"] != 20 || scenario.Metrics["throughput_ops_per_second"] <= 0) {
			t.Fatalf("performance scenario did not report quick-run metrics: %#v", scenario)
		}
	}
}

func TestRunSelectsOneSuite(t *testing.T) {
	tests := []struct {
		suite        string
		wantScenario []string
	}{
		{
			suite:        SuiteReliability,
			wantScenario: []string{"database_save_search_context", "concurrent_local_writes"},
		},
		{
			suite:        SuitePerformance,
			wantScenario: []string{"store_search"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.suite, func(t *testing.T) {
			report := Run(Options{Suite: tt.suite, Quick: true})
			if !report.Passed || report.Suite != tt.suite {
				t.Fatalf("report = %#v", report)
			}
			if len(report.Scenarios) != len(tt.wantScenario) {
				t.Fatalf("scenario count = %d, want %d: %#v", len(report.Scenarios), len(tt.wantScenario), report.Scenarios)
			}

			gotScenarios := make(map[string]bool, len(report.Scenarios))
			for _, scenario := range report.Scenarios {
				if scenario.Suite != tt.suite {
					t.Fatalf("scenario %q has suite %q, want %q", scenario.Name, scenario.Suite, tt.suite)
				}
				gotScenarios[scenario.Name] = true
			}
			for _, wantScenario := range tt.wantScenario {
				if !gotScenarios[wantScenario] {
					t.Fatalf("scenarios = %#v, missing %q", report.Scenarios, wantScenario)
				}
			}
		})
	}
}

func TestRunUsesFreshAbsoluteTemporaryDirectoryAndCleansUp(t *testing.T) {
	root := t.TempDir()
	oldNewTempDir, oldRemoveAll := newTempDir, removeAll
	newTempDir = func(dir, pattern string) (string, error) {
		if dir != "" {
			t.Fatalf("temporary directory parent = %q, want system temporary directory", dir)
		}
		return os.MkdirTemp(root, pattern)
	}
	removeAll = os.RemoveAll
	t.Cleanup(func() {
		newTempDir, removeAll = oldNewTempDir, oldRemoveAll
	})

	report := Run(Options{Quick: true})
	if !report.Passed {
		t.Fatalf("report = %#v", report)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read temporary root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("self-test temporary directory was not removed: %v", entries)
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("test root is not absolute: %q", root)
	}
}
