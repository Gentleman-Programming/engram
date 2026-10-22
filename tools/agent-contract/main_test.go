package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	engrammcp "github.com/Gentleman-Programming/engram/v3/internal/mcp"
)

func TestMain(m *testing.M) {
	// Child processes execute the same CLI entry point without a second build.
	if os.Getenv("ENGRAM_AGENT_CONTRACT_TEST_MAIN") == "1" {
		os.Exit(run(os.Args[1:], os.Stderr))
	}
	os.Exit(m.Run())
}

func generatorExecutable(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("generator integration tests execute an external process")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("locate generator test executable: %v", err)
	}
	return binary
}

func runGenerator(t *testing.T, binary, root string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = root
	command.Env = append(os.Environ(),
		"ENGRAM_DATA_DIR="+filepath.Join(root, "must-not-open-a-store"),
		"ENGRAM_AGENT_CONTRACT_TEST_MAIN=1",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("generator timed out: %v\n%s", ctx.Err(), stderr.String())
	}
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), stdout.String(), stderr.String()
	}
	t.Fatal(err)
	return -1, stdout.String(), stderr.String()
}

func TestGeneratorUsesCurrentExecutableWithoutGoToolchain(t *testing.T) {
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	binary := generatorExecutable(t)
	if binary != want {
		t.Fatalf("generator subprocess must reuse the test executable: got %q, want %q", binary, want)
	}
	root := t.TempDir()
	code, stdout, stderr := runGenerator(t, binary, root, "-h")
	if code != 0 || stdout != "" || stderr != help {
		t.Fatalf("help without Go toolchain: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("help created files or data: %v, %v", entries, err)
	}
}

func TestGeneratorWritesChecksAndPreservesStaleArtifact(t *testing.T) {
	binary, root := generatorExecutable(t), t.TempDir()
	path := filepath.Join(root, "contract.json")
	code, stdout, stderr := runGenerator(t, binary, root, "-output", path)
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("generate: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	want, err := engrammcp.AgentContractJSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("generated artifact mismatch: %v", err)
	}
	code, stdout, stderr = runGenerator(t, binary, root, "-check", "-output", path)
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("check: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stale := []byte("stale artifact\n")
	if err := os.WriteFile(path, stale, 0600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runGenerator(t, binary, root, "-check", "-output", path)
	wantError := fmt.Sprintf("agent contract is stale: %s; regenerate with go generate ./internal/mcp\n", path)
	if code != 1 || stdout != "" || stderr != wantError {
		t.Fatalf("stale check: exit=%d stdout=%q stderr=%q, want stderr=%q", code, stdout, stderr, wantError)
	}
	got, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(got, stale) {
		t.Fatalf("check modified a stale artifact: %v", err)
	}
	missing := filepath.Join(root, "missing", "contract.json")
	_, readError := os.ReadFile(missing)
	code, stdout, stderr = runGenerator(t, binary, root, "-check", "-output", missing)
	if code != 1 || stdout != "" || stderr != fmt.Sprintf("read agent contract: %v\n", readError) {
		t.Fatalf("missing check: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "contract.json" {
		t.Fatalf("check/export created unexpected state (including a data directory): %v, %v", entries, err)
	}
}

func TestProjectionMetadataCurrent(t *testing.T) {
	binary, root := generatorExecutable(t), t.TempDir()
	path := filepath.Join(root, "contract.json")
	code, stdout, stderr := runGenerator(t, binary, root, "-output", path)
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("export: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Revision   int    `json:"revision"`
		Status     string `json:"status"`
		Operations map[string]struct {
			Semantics struct {
				PiDifferences []string `json:"pi_differences"`
				ProjectPolicy string   `json:"project_policy"`
				TargetRule    string   `json:"target_rule"`
			} `json:"semantics"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if document.Revision != 2 || document.Status != "mcp-foundation" {
		t.Fatalf("metadata correction promoted contract: %+v", document)
	}
	for _, tc := range []struct {
		name     string
		facts    []string
		obsolete string
	}{
		{"mem_consolidate", []string{"HTTP", "fingerprint", "auto-confirm", "replay"}, "Go/MCP-only"},
		{"mem_judge_many", []string{"HTTP", "ordered", "provenance", "replay"}, "Go/MCP-only"},
		{"mem_session_finalize", []string{"confirmed", "host", "model", "explicit", "unknown", "replay"}, "Go/MCP-only"},
		{"mem_suggest_topic_key", []string{"HTTP", "Go", "envelope"}, "own slug"},
		{"mem_update", []string{"find", "replace", "Go", "envelope"}, "omits find"},
		{"mem_current_project", []string{"intentional", "canonical cwd", "directory", "host", "fallback"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Join(document.Operations[tc.name].Semantics.PiDifferences, " ")
			for _, fact := range tc.facts {
				if !strings.Contains(text, fact) {
					t.Errorf("projection missing %q: %s", fact, text)
				}
			}
			if tc.obsolete != "" && strings.Contains(text, tc.obsolete) {
				t.Errorf("obsolete projection claim %q: %s", tc.obsolete, text)
			}
		})
	}
	review := document.Operations["mem_review"].Semantics
	if !strings.Contains(review.ProjectPolicy, "ignores the project argument") || !strings.Contains(review.TargetRule, "explicit project confines") {
		t.Error("review current behavior must remain distinct from proposed target")
	}
	for _, name := range []string{"mem_save", "mem_search", "mem_context", "mem_get_observation"} {
		if len(document.Operations[name].Semantics.PiDifferences) == 0 {
			t.Errorf("valid transport/runtime differences erased for %s", name)
		}
	}
	code, stdout, stderr = runGenerator(t, binary, root, "-check", "-output", path)
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("check: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) || bytes.Contains(raw, []byte("\r")) {
		t.Fatalf("check changed artifact or export was not LF: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "contract.json" {
		t.Fatalf("export/check created unexpected state: %v, %v", entries, err)
	}
}

func TestGeneratorHelpAndRejectedArgumentsHaveNoEffects(t *testing.T) {
	binary, root := generatorExecutable(t), t.TempDir()
	code, stdout, stderr := runGenerator(t, binary, root, "-h")
	wantHelp := "Usage: agent-contract [-output path] [-check]\n\nGenerate the Go-owned MCP agent-contract foundation without opening a store.\n  -output path  Artifact path (default docs/contracts/agent-contract-v1.json).\n  -check        Compare without writing; exit 1 for a missing or stale artifact.\n\nVerify Pi model-input projections without writing:\n  agent-contract -verify-pi capture.json\n  Cannot combine with explicitly supplied -output or -check.\n  Exit 0: scope match; 1: refusal; 2: usage or capture unavailable.\n  Not semantic parity, installed-host compatibility or release approval.\n"
	if code != 0 || stdout != "" || stderr != wantHelp {
		t.Fatalf("help: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, tc := range []struct {
		args  []string
		error string
	}{
		{[]string{"-output", ""}, "output path must not be blank\n"},
		{[]string{"unexpected"}, "unexpected positional arguments\n"},
	} {
		code, stdout, stderr = runGenerator(t, binary, root, tc.args...)
		if code != 2 || stdout != "" || stderr != tc.error {
			t.Fatalf("reject %v: exit=%d stdout=%q stderr=%q", tc.args, code, stdout, stderr)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("help/rejection created files or data: %v, %v", entries, err)
	}
}

func TestPiInputUsageAndRefusal(t *testing.T) {
	binary, root := generatorExecutable(t), t.TempDir()
	capture := filepath.Join(root, "capture.json")
	if err := os.WriteFile(capture, []byte("null"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		args   []string
		code   int
		stderr string
	}{
		{"invalid-capture", []string{"-verify-pi", capture}, 1, "pi input: invalid capture\n"},
		{"missing-file", []string{"-verify-pi", filepath.Join(root, "private-missing")}, 2, "pi input: capture unavailable\n"},
		{"blank", []string{"-verify-pi="}, 2, "verify-pi path must not be blank\n"},
		{"whitespace", []string{"-verify-pi", " "}, 2, "verify-pi path must not be blank\n"},
		{"check", []string{"-verify-pi", capture, "-check"}, 2, "verify-pi cannot be combined with output or check\n"},
		{"explicit-false", []string{"-check=false", "-verify-pi", capture}, 2, "verify-pi cannot be combined with output or check\n"},
		{"output-default", []string{"-output", "docs/contracts/agent-contract-v1.json", "-verify-pi", capture}, 2, "verify-pi cannot be combined with output or check\n"},
		{"blank-output", []string{"-verify-pi", capture, "-output="}, 2, "verify-pi cannot be combined with output or check\n"},
		{"positional", []string{"-verify-pi", capture, "unexpected"}, 2, "unexpected positional arguments\n"},
		{"missing-value", []string{"-verify-pi"}, 2, "flag needs an argument: -verify-pi\n" + help},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errout := runGenerator(t, binary, root, tc.args...)
			if code != tc.code || out != "" || errout != tc.stderr {
				t.Errorf("exit=%d stdout=%q stderr=%q; want exit=%d stderr=%q", code, out, errout, tc.code, tc.stderr)
			}
			raw, err := os.ReadFile(capture)
			if err != nil || string(raw) != "null" {
				t.Fatal("capture changed", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != "capture.json" {
				t.Fatal("rejection wrote state", entries, err)
			}
		})
	}
}

func TestPiInputLegacyModes(t *testing.T) {
	t.Run("generate-check", TestGeneratorWritesChecksAndPreservesStaleArtifact)
	t.Run("help-no-toolchain", TestGeneratorUsesCurrentExecutableWithoutGoToolchain)
	t.Run("help-and-arguments", TestGeneratorHelpAndRejectedArgumentsHaveNoEffects)
}
