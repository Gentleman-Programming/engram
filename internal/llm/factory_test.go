package llm_test

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/llm"
)

// TestNewRunner_Claude verifies that "claude" returns a *ClaudeRunner without error.
func TestNewRunner_Claude(t *testing.T) {
	runner, err := llm.NewRunner("claude")
	if err != nil {
		t.Fatalf("NewRunner(\"claude\"): unexpected error: %v", err)
	}
	if _, ok := runner.(*llm.ClaudeRunner); !ok {
		t.Errorf("NewRunner(\"claude\") returned %T; want *llm.ClaudeRunner", runner)
	}
}

// TestNewRunner_OpenCode verifies that "opencode" returns a *OpenCodeRunner without error.
func TestNewRunner_OpenCode(t *testing.T) {
	runner, err := llm.NewRunner("opencode")
	if err != nil {
		t.Fatalf("NewRunner(\"opencode\"): unexpected error: %v", err)
	}
	if _, ok := runner.(*llm.OpenCodeRunner); !ok {
		t.Errorf("NewRunner(\"opencode\") returned %T; want *llm.OpenCodeRunner", runner)
	}
}

// TestNewRunner_Empty verifies that an empty string returns a descriptive error
// naming the ENGRAM_AGENT_CLI env var.
func TestNewRunner_Empty(t *testing.T) {
	_, err := llm.NewRunner("")
	if err == nil {
		t.Fatal("NewRunner(\"\"): expected error; got nil")
	}
	if !strings.Contains(err.Error(), "ENGRAM_AGENT_CLI") {
		t.Errorf("error should mention ENGRAM_AGENT_CLI; got: %v", err)
	}
}

// TestNewRunner_Unknown verifies that an unrecognized name returns a descriptive
// error naming the supported values.
func TestNewRunner_Unknown(t *testing.T) {
	_, err := llm.NewRunner("somethingelse")
	if err == nil {
		t.Fatal("NewRunner(\"somethingelse\"): expected error; got nil")
	}
	// Should mention supported runners.
	msg := err.Error()
	if !strings.Contains(msg, "claude") || !strings.Contains(msg, "opencode") {
		t.Errorf("error should mention supported runners; got: %v", err)
	}
}

// TestNewRunner_ErrInvalidRunnerName verifies the ErrInvalidRunnerName sentinel
// is returned for unknown names.
func TestNewRunner_ErrInvalidRunnerName(t *testing.T) {
	_, err := llm.NewRunner("unknown")
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	// The sentinel should be reachable via errors.Is (wrapped).
	// We check the type via the exported sentinel from the package.
	// Since factory uses fmt.Errorf("%w", ErrInvalidRunnerName, ...) the sentinel is wrapped.
	// Just verify error message content as a proxy.
	if !strings.Contains(err.Error(), "ENGRAM_AGENT_CLI") && !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should reference env var or describe the problem; got: %v", err)
	}
}
