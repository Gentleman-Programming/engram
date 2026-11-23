package mcp

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestMemSave_RejectsIdenticalAmbiguousProjectChoices(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(parent, "unused-git-config"))
	t.Setenv("ENGRAM_PROJECT", "")
	makeRepos := func(root string, names ...string) {
		t.Helper()
		for _, name := range names {
			child := filepath.Join(root, name)
			if err := os.Mkdir(child, 0o700); err != nil {
				t.Fatal(err)
			}
			initTestGitRepo(t, child)
		}
	}
	makeRepos(parent, " folded-project", "folded-project")
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
	start := func(title, sessionID string) (map[string]any, map[string]any) {
		t.Helper()
		args := map[string]any{"title": title, "content": "isolated ambiguous project", "type": "manual", "session_id": sessionID}
		initial := call(args)
		body := callResultJSON(t, initial)
		if !initial.IsError || body["error_code"] != "ambiguous_project" || body["recovery_token"] == nil {
			t.Fatalf("expected real ambiguous detection: %v", body)
		}
		return args, body
	}
	// An already known bucket exercises the bare explicit-override path.
	// This path must respect the existing ambiguous collision guard.
	if err := s.CreateSession("existing-session", "folded-project", filepath.Join(parent, "folded-project")); err != nil {
		t.Fatal(err)
	}
	const explicitTitle = "explicit ambiguous name must not save"
	explicit := call(map[string]any{"title": explicitTitle, "content": "different checkout",
		"type": "manual", "project": "folded-project"})
	if body := callResultJSON(t, explicit); !explicit.IsError || body["error_code"] != "project_name_collision" {
		t.Errorf("bare explicit selection of the backed ambiguous project: %s", callResultText(t, explicit))
	}
	rows, err := s.Search(explicitTitle, store.SearchOptions{Project: "folded-project", Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Errorf("explicit colliding choice persisted %d observations, err=%v", len(rows), err)
	}

	const rejectedTitle = "same choice must not save"
	args, discovery := start(rejectedTitle, "folded-session")
	choices, ok := discovery["available_projects"].([]any)
	if !ok || len(choices) != 2 || choices[0] != "folded-project" || choices[1] != "folded-project" {
		t.Fatalf("fixture did not yield two identical choices: %v", discovery)
	}
	args["project"] = "folded-project"
	args["project_choice_reason"] = project.SourceUserSelectedAfterAmbiguousProject
	args["recovery_token"] = discovery["recovery_token"]
	result := call(args)
	if body := callResultJSON(t, result); !result.IsError || body["error_code"] != "project_name_collision" {
		t.Errorf("colliding recovery did not report project_name_collision: %s", callResultText(t, result))
	}
	if session, err := s.GetSession("folded-session"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("rejected recovery changed session: %+v, %v", session, err)
	}
	rows, err = s.Search(rejectedTitle, store.SearchOptions{Project: "folded-project", Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Errorf("colliding recovery persisted %d observations, err=%v", len(rows), err)
	}

	uniqueParent := t.TempDir()
	makeRepos(uniqueParent, "unrelated-project", "another-project")
	t.Chdir(uniqueParent)
	args, discovery = start("unique recovery remains usable", "unrelated-session")
	args["project"] = "unrelated-project"
	args["project_choice_reason"] = project.SourceUserSelectedAfterAmbiguousProject

	if token, ok := discovery["recovery_token"].(string); !ok || token == "" {
		t.Fatalf("unique recovery choices did not produce a token: %v", discovery)
	} else {
		args["recovery_token"] = token
	}
	selected := call(args)
	if selected.IsError {
		t.Fatalf("the unambiguous choice in the same workspace did not recover: %s", callResultText(t, selected))
	}
	if session, err := s.GetSession("unrelated-session"); err != nil || session.Project != "unrelated-project" ||
		session.Directory != filepath.Join(uniqueParent, "unrelated-project") {
		t.Fatalf("unique recovery session=%+v, err=%v", session, err)
	}
}
