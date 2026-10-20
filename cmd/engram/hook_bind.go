package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/hostbind"
)

// hostBindingDataDir resolves the directory where host bindings are kept. It
// is the same directory the MCP server's store uses, so a binding published by
// a hook is visible to `engram mcp` without any server round trip.
// It applies the same ENGRAM_DATA_DIR override main() applies to the store.
var hostBindingDataDir = hostBindingDataDirFromEnvOrConfig

func hostBindingDataDirFromEnvOrConfig() string {
	if dir := os.Getenv("ENGRAM_DATA_DIR"); strings.TrimSpace(dir) != "" {
		return dir
	}
	cfg, err := storeDefaultConfig()
	if err != nil {
		return ""
	}
	return cfg.DataDir
}

// hostProcessPID returns the PID of the process that launched this hook. It is a
// seam so tests can bind to a known process.
var hostProcessPID = os.Getppid

// publishClaudeHostBinding links the Claude host process to the session it
// reported. It is best effort by design: a hook verdict or SessionStart must
// never fail because the binding could not be written, and an absent binding
// only means the MCP server keeps its existing resolution (#1693).
func publishClaudeHostBinding(sessionID string, hostPID int) {
	if strings.TrimSpace(sessionID) == "" || hostPID <= 1 {
		return
	}
	dir := hostBindingDataDir()
	if dir == "" {
		return
	}
	_ = hostbind.Publish(dir, hostPID, sessionID)
}

// cmdClaudeBindSession is invoked by Claude's SessionStart hook (startup,
// resume, clear, fork) to refresh the host-to-session binding. The hook is
// registered in exec form (no shell), so this command's parent is the Claude
// process itself. It prints nothing (plain stdout on SessionStart would be
// added to the model context) and always succeeds so it can never disturb
// session start.
func cmdClaudeBindSession() {
	if runningUnderPi() {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(input, &payload) != nil {
		return
	}
	sessionID, ok := claudeHookRequiredString(payload, "session_id")
	if !ok {
		return
	}
	publishClaudeHostBinding(sessionID, hostProcessPID())
}
