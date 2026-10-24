package mcp

import (
	"os"
	"strings"
	"testing"
)

// The 2048-rune client truncation ceiling moved to token_budget_test.go, where
// it sits next to the pinned budget it backs up.

func TestServerInstructionsUsesCandidateJudgmentIDs(t *testing.T) {
	const candidateInstruction = "with candidates[].judgment_id"
	const topLevelWarning = "never reuse the top-level judgment_id"

	candidateIndex := strings.Index(serverInstructions, candidateInstruction)
	warningIndex := strings.Index(serverInstructions, topLevelWarning)
	if candidateIndex < 0 || warningIndex < candidateIndex {
		t.Errorf("serverInstructions must require each candidate's judgment_id and prohibit reusing the top-level judgment_id")
	}
}

func TestMCPToolInventoryMatchesDocumentation(t *testing.T) {
	tools := NewServer(newMCPTestStore(t)).ListTools()
	if len(tools) != 27 {
		t.Fatalf("registered %d tools, want 27", len(tools))
	}
	for _, name := range []string{"mem_judge_many", "mem_session_finalize", "mem_consolidate"} {
		if tools[name] == nil {
			t.Errorf("missing integrated tool %s", name)
		}
	}
	docs, err := os.ReadFile("../../DOCS.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"## MCP Tools (27 tools)",
		`<a id="mcp-tools-23-tools"></a>`,
		`<a id="mcp-tools-25-tools"></a>`,
		"### mem_consolidate",
	} {
		if !strings.Contains(string(docs), text) {
			t.Errorf("MCP documentation missing %q", text)
		}
	}
}

func TestServerInstructionsDeliveryGuarantee(t *testing.T) {
	for _, requirement := range []string{
		"Memory operations are internal bookkeeping, never the user-facing answer.",
		"Complete required memory work before composing the completed-task reply;",
		"send the complete answer as the final message of the turn with no later tool calls.",
		"If memory work fails or needs follow-up, still send the answer.",
	} {
		if !strings.Contains(serverInstructions, requirement) {
			t.Errorf("server instructions missing delivery guarantee: %q", requirement)
		}
	}
}
