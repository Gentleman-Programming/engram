package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeContractCommandProcess(t *testing.T) {
	if os.Getenv("ENGRAM_TEST_RUNTIME_CONTRACT_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(run(os.Args[i+1:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(2)
}

func TestRuntimeContractRepositoryProjection(t *testing.T) {
	root := filepath.Join("..", "..")
	file := filepath.Join(root, "plugin", "pi", "runtime-contract.json")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeContractCommandProcess$", "--", "-check")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "ENGRAM_TEST_RUNTIME_CONTRACT_PROCESS=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if err != nil || stdout.String() != "Pi HTTP runtime projection is current\n" || stderr.String() != "" {
		t.Fatalf("repository projection check: exit=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("repository projection check modified the artifact: %v", err)
	}
}

func TestRuntimeContractCommand(t *testing.T) {
	cwd := t.TempDir()
	command := func(args ...string) (int, string, string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestRuntimeContractCommandProcess$", "--"}, args...)...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), "ENGRAM_TEST_RUNTIME_CONTRACT_PROCESS=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if err == nil {
			return 0, stdout.String(), stderr.String()
		}
		if failure, ok := err.(*exec.ExitError); ok {
			return failure.ExitCode(), stdout.String(), stderr.String()
		}
		t.Fatalf("command unavailable: %v", err)
		return -1, "", ""
	}
	code, generated, stderr := command()
	if code != 0 || stderr != "" || !strings.HasSuffix(generated, "\n") {
		t.Fatalf("generate exit=%d stdout=%q stderr=%q", code, generated, stderr)
	}
	var projection struct {
		ContractID string                     `json:"contract_id"`
		Revision   int                        `json:"revision"`
		Operations map[string]json.RawMessage `json:"operations"`
		InstanceID *string                    `json:"instance_id"`
	}
	if err := json.Unmarshal([]byte(generated), &projection); err != nil || projection.ContractID != "engram.pi-http-runtime/v1" || projection.Revision != 1 || len(projection.Operations) != 23 || projection.InstanceID != nil {
		t.Fatalf("invalid projection: %+v error=%v", projection, err)
	}
	file := filepath.Join(cwd, "plugin", "pi", "runtime-contract.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(generated), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := command("-check")
	if code != 0 || stdout != "Pi HTTP runtime projection is current\n" || stderr != "" {
		t.Fatalf("check exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, args := range [][]string{{"unexpected"}, {"-unknown"}} {
		code, stdout, stderr = command(args...)
		if code != 2 || stdout != "" || stderr == "" {
			t.Errorf("rejected args %v: exit=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
		data, err := os.ReadFile(file)
		if err != nil || string(data) != generated {
			t.Errorf("rejected arguments modified the projection: %v", err)
		}
	}
	code, stdout, stderr = command("-help")
	if code != 0 || stdout != "" || !strings.Contains(stderr, "-check") {
		t.Errorf("help exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if err := os.WriteFile(file, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = command("-check")
	if code != 1 || stdout != "" || stderr != "runtime-contract: expected projection is stale; regenerate with go run ./tools/runtime-contract\n" {
		t.Errorf("stale check exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "stale\n" {
		t.Errorf("stale check modified the projection: %v", err)
	}
}
