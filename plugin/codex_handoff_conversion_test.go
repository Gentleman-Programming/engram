package plugin_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCodexHandoffConversionFailures(t *testing.T) {
	const scenarioKey = "ENGRAM_TEST_HANDOFF_CONVERSION_CASE"
	const converterKey = "ENGRAM_TEST_HANDOFF_CONVERTER"
	if scenario := os.Getenv(scenarioKey); scenario != "" {
		got := codexHandoffConvertPaths(t, codexIntentFixture{cygpath: os.Getenv(converterKey)}, []string{scenario})
		if strings.HasPrefix(scenario, "success") && !reflect.DeepEqual(got, []string{"/one"}) {
			t.Fatalf("converted paths = %q", got)
		}
		// A negative case reaching here must exit successfully so the parent
		// detects a missing rejection, rather than manufacturing its own failure.
		return
	}

	dir := t.TempDir()
	source := `package main
import ("os"; "time")
func main() {
 switch os.Args[3] {
 case "exit": os.Exit(23)
 case "timeout": time.Sleep(time.Minute)
 case "count": _, _ = os.Stdout.WriteString("/one\n/two\n")
 case "relative": _, _ = os.Stdout.WriteString("relative\n")
 case "empty": _, _ = os.Stdout.WriteString("\n")
 case "embedded-cr": _, _ = os.Stdout.WriteString("/one\rbroken\n")
 case "extra-line": _, _ = os.Stdout.WriteString("/one\n\n")
 case "success-lf": _, _ = os.Stdout.WriteString("/one\n")
 case "success-crlf": _, _ = os.Stdout.WriteString("/one\r\n")
 default: os.Exit(91)
 }
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	converter := filepath.Join(dir, "controlled-converter.exe")
	build := exec.Command("go", "build", "-o", converter, "main.go")
	build.Dir = dir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build controlled converter: %v: %s", err, output)
	}
	for _, tc := range []struct{ name, diagnostic string }{
		{"exit", "exit status 23"},
		{"timeout", "context deadline exceeded"},
		{"count", "fixture path conversion count mismatch"},
		{"relative", "invalid converted fixture path"},
		{"empty", "invalid converted fixture path"},
		{"embedded-cr", "invalid converted fixture path"},
		{"extra-line", "fixture path conversion count mismatch"},
		{"success-lf", ""},
		{"success-crlf", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCodexHandoffConversionFailures$", "-test.v")
			cmd.WaitDelay = time.Second
			cmd.Env = append(os.Environ(), scenarioKey+"="+tc.name, converterKey+"="+converter)
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("conversion probe exceeded its outer bound: %v", ctx.Err())
			}
			if tc.diagnostic == "" {
				if err != nil {
					t.Fatalf("valid conversion failed: %v: %s", err, output)
				}
				return
			}
			failed, ok := err.(*exec.ExitError)
			if !ok || failed.ExitCode() != 1 || !strings.Contains(string(output), tc.diagnostic) {
				t.Fatalf("conversion probe error=%v, want test exit1 containing %q; output=%s", err, tc.diagnostic, output)
			}
		})
	}
	t.Run("no converter", func(t *testing.T) {
		paths := []string{"/one", "/two"}
		if got := codexHandoffConvertPaths(t, codexIntentFixture{}, paths); !reflect.DeepEqual(got, paths) {
			t.Fatalf("passthrough paths = %q", got)
		}
	})
}
