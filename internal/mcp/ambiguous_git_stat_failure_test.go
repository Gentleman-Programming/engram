package mcp

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestMemSave_AmbiguousSiblingStatFailureRefusesWithoutPrivatePath(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(parent, "unused-git-config"))
	t.Setenv("ENGRAM_PROJECT", "")
	const selected = "selected-project"
	for _, name := range []string{"a-control", selected, " selected-project"} {
		if err := os.Mkdir(filepath.Join(parent, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a-control", selected} {
		initTestGitRepo(t, filepath.Join(parent, name))
	}
	brokenGit := filepath.Join(parent, " selected-project", ".git")
	if err := os.Symlink(".git", brokenGit); err != nil {
		t.Skipf("cannot create a symlink loop on this filesystem: %v", err)
	}
	if _, err := os.Stat(brokenGit); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Skipf("filesystem did not report an indeterminate Git marker: %v", err)
	}
	t.Chdir(parent)

	s := newMCPTestStore(t)
	if err := s.CreateSession("existing-owner", selected, filepath.Join(parent, selected)); err != nil {
		t.Fatal(err)
	}
	handler := handleSave(s, MCPConfig{}, NewSessionActivity(time.Minute))
	const title = "cannot verify sibling must not save"
	result, err := handler(t.Context(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"project": selected, "title": title, "content": "synthetic fixture", "type": "manual",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	body := callResultJSON(t, result)
	if !result.IsError || body["error_code"] != "ambiguous_project" {
		t.Errorf("expected ambiguity refusal for indeterminate sibling: %s", callResultText(t, result))
	}
	if text := callResultText(t, result); strings.Contains(text, parent) || strings.Contains(text, strings.ReplaceAll(parent, `\`, `\\`)) || strings.Contains(text, ".git") {
		t.Errorf("response exposed private verifier details: %s", text)
	}
	if got, err := s.GetSession("manual-save-" + selected); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("indeterminate sibling changed session: %+v, %v", got, err)
	}
	if rows, err := s.Search(title, store.SearchOptions{Project: selected, Limit: 10}); err != nil || len(rows) != 0 {
		t.Errorf("indeterminate sibling wrote %d observations, err=%v", len(rows), err)
	}
}
