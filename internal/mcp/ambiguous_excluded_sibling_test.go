package mcp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/project"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestMemSave_AmbiguousRecoveryIgnoresExcludedGitSiblings(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("ENGRAM_PROJECT", "")
	const wanted = "node_modules"
	for _, name := range []string{"active-repo", " " + wanted, wanted} {
		// Child discovery and recovery both identify repositories by this marker.
		if err := os.MkdirAll(filepath.Join(parent, name, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(parent)
	s := newMCPTestStore(t)
	handler := handleSave(s, MCPConfig{}, NewSessionActivity(time.Minute))
	call := func(args map[string]any) *mcppkg.CallToolResult {
		t.Helper()
		result, err := handler(t.Context(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	for _, choice := range []string{wanted, "active-repo"} {
		args := map[string]any{
			"session_id": "session-" + choice,
			"title":      "recovery for " + choice,
			"content":    "synthetic recovery",
			"type":       "manual",
		}
		initial := call(args)
		body := callResultJSON(t, initial)
		choices, ok := body["available_projects"].([]any)
		if !initial.IsError || body["error_code"] != "ambiguous_project" || !ok ||
			len(choices) != 2 || choices[0] != "active-repo" || choices[1] != wanted {
			t.Fatalf("expected two eligible choices: %v", body)
		}
		token, ok := body["recovery_token"].(string)
		if !ok || token == "" {
			t.Fatalf("ignored Git directory suppressed the recovery token for %q: %v", choice, body)
		}
		args["project"] = choice
		args["project_choice_reason"] = project.SourceUserSelectedAfterAmbiguousProject
		args["recovery_token"] = token
		if result := call(args); result.IsError {
			t.Fatalf("expected valid recovery for %q: %s", choice, callResultText(t, result))
		}
		expectedDir := filepath.Join(parent, choice)
		if choice == wanted {
			expectedDir = filepath.Join(parent, " "+wanted)
		}
		expectedDir, err := filepath.EvalSymlinks(expectedDir)
		if err != nil {
			t.Fatal(err)
		}
		session, err := s.GetSession("session-" + choice)
		if err != nil || session.Project != choice || session.Directory != expectedDir {
			t.Fatalf("incorrect recovery binding for %q: %+v, %v", choice, session, err)
		}
	}
}
