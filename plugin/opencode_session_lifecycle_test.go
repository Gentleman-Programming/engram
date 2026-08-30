package plugin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeAdaptersHandleEmptyResponsesAndEndSessionsOnce(t *testing.T) {
	root := repoRoot(t)
	canonicalPath := filepath.Join(root, "plugin", "opencode", "engram.ts")
	embeddedPath := filepath.Join(root, "internal", "setup", "plugins", "opencode", "engram.ts")

	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical OpenCode adapter: %v", err)
	}
	embedded, err := os.ReadFile(embeddedPath)
	if err != nil {
		t.Fatalf("read embedded OpenCode adapter: %v", err)
	}
	if string(canonical) != string(embedded) {
		t.Fatal("OpenCode adapter copies differ; keep the distributable and setup copies synchronized")
	}

	source := string(canonical)
	for _, required := range []string{
		"if (!res.ok) return null",
		"const text = await res.text()",
		"if (!text.trim()) return {}",
		"return JSON.parse(text)",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("engramFetch must contain %q", required)
		}
	}

	deletedStart := strings.Index(source, `if (event.type === "session.deleted")`)
	if deletedStart == -1 {
		t.Fatal("session.deleted handler not found")
	}
	deletedHandler := source[deletedStart:]
	if strings.Count(deletedHandler, "/end") != 1 {
		t.Errorf("session.deleted must issue exactly one /end request, found %d", strings.Count(deletedHandler, "/end"))
	}
	if strings.Contains(deletedHandler, "let ended") || strings.Contains(deletedHandler, "ended == null") || strings.Contains(deletedHandler, "ended != null") {
		t.Error("session.deleted must not retry /end or condition local cleanup on its response")
	}

	endRequest := strings.Index(deletedHandler, "/end")
	knownSessionDelete := strings.Index(deletedHandler, "knownSessions.delete(sessionId)")
	if endRequest == -1 || knownSessionDelete == -1 || knownSessionDelete < endRequest {
		t.Error("session.deleted must remove the session from knownSessions after its single /end request")
	}
}
