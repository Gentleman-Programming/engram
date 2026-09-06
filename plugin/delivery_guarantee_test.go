package plugin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryProtocolDeliveryGuarantee(t *testing.T) {
	targets := []string{
		"plugin/pi/index.ts",
		"plugin/claude-code/scripts/session-start.sh",
		"plugin/claude-code/scripts/post-compaction.sh",
		"plugin/claude-code/skills/memory/SKILL.md",
		"plugin/codex/scripts/session-start.sh",
		"plugin/codex/scripts/post-compaction.sh",
		"plugin/codex/skills/memory/SKILL.md",
		"skills/memory-protocol/SKILL.md",
		"DOCS.md",
		"docs/PLUGINS.md",
	}
	requirements := deliveryGuaranteeRequirements()

	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(repoRoot(t), target))
			if err != nil {
				t.Fatalf("read protocol surface: %v", err)
			}
			for _, requirement := range requirements {
				if !strings.Contains(string(content), requirement) {
					t.Errorf("missing delivery guarantee: %q", requirement)
				}
			}
		})
	}
}

func deliveryGuaranteeRequirements() []string {
	return []string{
		"Memory operations are internal bookkeeping, never the user-facing answer.",
		"Complete required memory work before composing the completed-task reply;",
		"send the complete answer as the final message of the turn with no later tool calls.",
		"If memory work fails or needs follow-up, still send the answer.",
	}
}
