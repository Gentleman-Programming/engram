package plugin_test

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestCodexSubagentStopAlwaysEmitsHookEnvelope(t *testing.T) {
	bash := codexTestBash(t)
	requireCodexUnixTools(t, bash)
	script := repoRoot(t) + "/plugin/codex/scripts/subagent-stop.sh"
	for _, input := range []string{"", `{}`, `{"cwd":"/missing","last_assistant_message":"captured"}`} {
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
