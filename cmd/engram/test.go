package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Gentleman-Programming/engram/v2/internal/selftest"
)

const (
	testExitSuccess = 0
	testExitUsage   = 1
	testExitFailure = 2
)

var runSelfTest = selftest.Run

// cmdTest runs the local, isolated binary self-test command. It intentionally
// takes explicit arguments so main can dispatch it before configuration setup.
func cmdTest(args []string) int {
	options, jsonOutput, help, err := parseTestArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n\n", err)
		printTestUsage()
		return testExitUsage
	}
	if help {
		printTestUsage()
		return testExitSuccess
	}

	report := runSelfTest(options)
	if jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "error: write JSON self-test report: %v\n", err)
			return testExitFailure
		}
	} else {
		printTestReport(report)
	}
	if report.Passed {
		return testExitSuccess
	}
	return testExitFailure
}

func parseTestArgs(args []string) (selftest.Options, bool, bool, error) {
	options := selftest.Options{Suite: selftest.SuiteAll}
	jsonOutput := false
	help := false
	suiteSeen := false

	for _, arg := range args {
		switch arg {
		case "--quick":
			options.Quick = true
		case "--json":
			jsonOutput = true
		case "--help", "-h", "help":
			help = true
		default:
			if strings.HasPrefix(arg, "-") {
				return selftest.Options{}, false, false, fmt.Errorf("unknown test flag %q", arg)
			}
			if suiteSeen {
				return selftest.Options{}, false, false, fmt.Errorf("only one test suite may be selected")
			}
			suite := strings.ToLower(strings.TrimSpace(arg))
			switch suite {
			case selftest.SuiteAll, selftest.SuiteReliability, selftest.SuitePerformance:
				options.Suite = suite
				suiteSeen = true
			default:
				return selftest.Options{}, false, false, fmt.Errorf("unknown test suite %q", arg)
			}
		}
	}
	return options, jsonOutput, help, nil
}

func printTestUsage() {
	fmt.Println("usage: engram test [reliability|performance] [--quick] [--json]")
	fmt.Println()
	fmt.Println("Run isolated local reliability and performance self-tests.")
	fmt.Println("Without a suite, runs reliability and performance. --quick bounds the smoke run.")
	fmt.Println("--json writes the stable engram-self-test/v1 report to stdout.")
	fmt.Println("The test uses a fresh temporary directory and never reads configured data or cloud settings.")
}

func printTestReport(report selftest.Report) {
	mode := "full"
	if report.Quick {
		mode = "quick"
	}
	fmt.Printf("Engram self-test (%s suite, %s mode)\n", report.Suite, mode)
	for _, scenario := range report.Scenarios {
		status := "PASS"
		if !scenario.Passed {
			status = "FAIL"
		}
		fmt.Printf("%s  %s/%s  %dms", status, scenario.Suite, scenario.Name, scenario.DurationMS)
		if operations, ok := scenario.Metrics["operations"]; ok {
			fmt.Printf("  %.0f operations", operations)
		}
		if throughput, ok := scenario.Metrics["throughput_ops_per_second"]; ok {
			fmt.Printf("  %.1f ops/s", throughput)
		}
		if scenario.Error != "" {
			fmt.Printf("  %s", scenario.Error)
		}
		fmt.Println()
	}
	status := "PASS"
	if !report.Passed {
		status = "FAIL"
	}
	fmt.Printf("Result: %s (%dms)\n", status, report.DurationMS)
}
