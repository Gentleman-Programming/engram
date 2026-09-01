package plugin_test

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexSubagentStopAlwaysEmitsHookEnvelope(t *testing.T) {
	bash := codexTestBash(t)
	requireCodexUnixTools(t, bash)
	script := repoRoot(t) + "/plugin/codex/scripts/subagent-stop.sh"
	missingCWD := filepath.Join(t.TempDir(), "missing")
	inputWithMissingCWD, err := json.Marshal(struct {
		CWD                  string `json:"cwd"`
		LastAssistantMessage string `json:"last_assistant_message"`
	}{
		CWD:                  missingCWD,
		LastAssistantMessage: "captured",
	})
	if err != nil {
		t.Fatalf("marshal test input: %v", err)
	}
	for _, input := range []string{"", `{}`, string(inputWithMissingCWD)} {
		run := exec.Command(bash, script)
		run.Stdin = strings.NewReader(input)
		var stdout, stderr bytes.Buffer
		run.Stdout = &stdout
		run.Stderr = &stderr
		err := run.Run()
		if err != nil {
			t.Fatalf("input %q: %v", input, err)
		}
		if stdout.String() != "{}\n" {
			t.Fatalf("input %q stdout=%q, want exactly a JSON envelope", input, stdout.String())
		}
		var envelope map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatalf("input %q emitted invalid JSON %q: %v", input, stdout.String(), err)
		}
	}
}
