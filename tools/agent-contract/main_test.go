package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	engrammcp "github.com/Gentleman-Programming/engram/v3/internal/mcp"
)

func buildGenerator(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("generator integration tests build and execute an external Go binary")
	}
	binary := filepath.Join(t.TempDir(), "agent-contract.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("build generator: %v\n%s", err, output)
	}
	return binary
}

func runGenerator(t *testing.T, binary, root string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = root
	command.Env = append(os.Environ(), "ENGRAM_DATA_DIR="+filepath.Join(root, "must-not-open-a-store"))
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

func TestGeneratorWritesChecksAndPreservesStaleArtifact(t *testing.T) {
	binary, root := buildGenerator(t), t.TempDir()
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

func TestGeneratorHelpAndRejectedArgumentsHaveNoEffects(t *testing.T) {
	binary, root := buildGenerator(t), t.TempDir()
	code, stdout, stderr := runGenerator(t, binary, root, "-h")
	wantHelp := "Usage: agent-contract [-output path] [-check]\n\nGenerate the Go-owned MCP agent-contract foundation without opening a store.\n  -output path  Artifact path (default docs/contracts/agent-contract-v1.json).\n  -check        Compare without writing; exit 1 for a missing or stale artifact.\n"
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
