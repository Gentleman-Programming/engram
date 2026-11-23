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

func TestMemSave_RecoveryRejectsCollisionWithUnlistedSibling(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(parent, "unused-git-config"))
	t.Setenv("ENGRAM_PROJECT", "")
	for _, name := range []string{"a--project", "a-project", "b--project", "b-project"} {
		if err := os.Mkdir(filepath.Join(parent, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Discover actual iteration order before creating Git metadata. Make the
	// first entry an unrelated repo and the other group's two entries a pair
	// of colliding repos. The first group's non-Git sibling is a control.
	directory, err := os.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := directory.ReadDir(-1)
	_ = directory.Close()
	if err != nil || len(entries) != 4 {
		t.Fatalf("read fixture children: %v, %v", entries, err)
	}
	unrelated := entries[0].Name()
	unrelatedProject, _ := store.NormalizeProject(unrelated)
	var colliding []string
	for _, entry := range entries[1:] {
		normalized, _ := store.NormalizeProject(entry.Name())
		if normalized != unrelatedProject {
			colliding = append(colliding, entry.Name())
		}
	}
	if len(colliding) != 2 {
		t.Fatalf("expected two entries in the other normalized group: %v", colliding)
	}
	visible, omitted := colliding[0], colliding[1]
	stored, _ := store.NormalizeProject(visible)
	for _, name := range []string{unrelated, visible, omitted} {
		initTestGitRepo(t, filepath.Join(parent, name))
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
	expectCollision := func(result *mcppkg.CallToolResult) {
		t.Helper()
		if body := callResultJSON(t, result); !result.IsError || body["error_code"] != "project_name_collision" {
			t.Errorf("colliding write did not report project_name_collision: %s", callResultText(t, result))
		}
	}
	assertAbsent := func(title, session string) {
		t.Helper()
		if got, err := s.GetSession(session); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("colliding write changed session: %+v, %v", got, err)
		}
		obs, err := s.Search(title, store.SearchOptions{Project: stored, Limit: 10})
		if err != nil || len(obs) != 0 {
			t.Errorf("colliding write persisted %d observations, err=%v", len(obs), err)
		}
	}
	const title = "unlisted collision rejected"
	const sessionID = "unlisted-collision-session"
	args := map[string]any{"title": title, "content": "synthetic recovery", "type": "manual", "session_id": sessionID}
	initial := call(args)
	body := callResultJSON(t, initial)
	choices, ok := body["available_projects"].([]any)
	if !initial.IsError || body["error_code"] != "ambiguous_project" || !ok || len(choices) != 2 ||
		((choices[0] != unrelated || choices[1] != visible) && (choices[1] != unrelated || choices[0] != visible)) {
		t.Fatalf("expected native detector to report both visible choices and omit %q: %v", omitted, body)
	}
	token, ok := body["recovery_token"].(string)
	if !ok || token == "" {
		t.Fatalf("two reported exact paths should be usable for recovery: %v", body)
	}
	args["project"] = visible
	args["project_choice_reason"] = project.SourceUserSelectedAfterAmbiguousProject
	args["recovery_token"] = token
	expectCollision(call(args))
	assertAbsent(title, sessionID)

	if err := s.CreateSession("existing-owner", stored, filepath.Join(parent, omitted)); err != nil {
		t.Fatal(err)
	}
	const explicitTitle = "unlisted explicit collision rejected"
	expectCollision(call(map[string]any{"title": explicitTitle, "content": "synthetic explicit selection", "type": "manual", "project": stored}))
	assertAbsent(explicitTitle, "manual-save-"+stored)

	// A refusal must leave the token usable for the safe choice.
	args["project"] = unrelated
	args["title"] = "unrelated recovery still saves"
	if result := call(args); result.IsError {
		t.Fatalf("unrelated recovery with same token: %s", callResultText(t, result))
	}
	if got, err := s.GetSession(sessionID); err != nil || got.Project != unrelatedProject || got.Directory != filepath.Join(parent, unrelated) {
		t.Fatalf("unrelated recovery changed association: %+v %v", got, err)
	}

	// Selecting a stored project from an unambiguous directory stays valid.
	t.Chdir(filepath.Join(parent, unrelated))
	if result := call(map[string]any{"title": "deliberate reuse stays available", "content": "synthetic override", "type": "manual", "project": stored}); result.IsError {
		t.Fatalf("ordinary explicit override: %s", callResultText(t, result))
	}
}
