package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestDoctorFirstGitInspection(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Git integration")
	}
	for _, history := range []string{"", "remote-app"} {
		t.Run("history="+history, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			s := newMCPTestStore(t)
			if history != "" {
				if err := s.CreateSession("history", history, dir); err != nil {
					t.Fatal(err)
				}
			}
			initTestGitRepo(t, dir)
			if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "https://example.test/team/remote-app.git").CombinedOutput(); err != nil {
				t.Fatalf("remote: %s %v", out, err)
			}
			req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"check": "session_project_directory_mismatch"}}}
			res, err := handleDoctor(s, MCPConfig{})(context.Background(), req)
			if err != nil || res.IsError {
				t.Fatalf("doctor failed: %v %v", res, err)
			}
			if _, err := os.Stat(filepath.Join(dir, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
				t.Fatalf("doctor published binding: %v", err)
			}
		})
	}
}

func TestProjectTransitionFirstGitDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Git integration")
	}
	for _, operation := range []string{"current", "save", "search", "start", "all", "explicit", "session", "process", "config"} {
		t.Run(operation, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "local-app")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(dir)
			s := newMCPTestStore(t)
			if err := s.CreateSession("history", "local-app", dir); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddObservation(store.AddObservationParams{SessionID: "history", Project: "local-app", Type: "decision", Title: "historical marker", Content: "keep this memory"}); err != nil {
				t.Fatal(err)
			}
			initTestGitRepo(t, dir)
			if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "https://example.test/team/remote-app.git").CombinedOutput(); err != nil {
				t.Fatalf("remote: %s: %v", out, err)
			}
			cfg := MCPConfig{}
			args := map[string]any{"title": "new marker", "content": "new memory", "type": "decision"}
			switch operation {
			case "search":
				args["query"] = "historical marker"
			case "start":
				args["id"] = "new-session"
				args["directory"] = dir
			case "all":
				args["query"] = "historical marker"
				args["all_projects"] = true
			case "explicit":
				args["project"] = "local-app"
			case "session":
				args["session_id"] = "history"
			case "process":
				cfg.DefaultProject = "local-app"
			case "config":
				if err := os.Mkdir(".engram", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(".engram", "config.json"), []byte(`{"project_name":"local-app"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}}
			var res *mcppkg.CallToolResult
			var err error
			switch operation {
			case "current", "process":
				res, err = handleCurrentProject(s, cfg)(context.Background(), req)
			case "search", "all":
				res, err = handleSearch(s, cfg, NewSessionActivity(time.Minute))(context.Background(), req)
			case "start":
				res, err = handleSessionStart(s, cfg, NewSessionActivity(time.Minute))(context.Background(), req)
			default:
				res, err = handleSave(s, cfg, NewSessionActivity(time.Minute))(context.Background(), req)
			}
			if err != nil {
				t.Fatal(err)
			}
			body := callResultJSON(t, res)
			switch operation {
			case "current":
				if body["error_hint"] == nil || body["project"] != "" || res.IsError {
					t.Fatalf("expected informational conflict: %v", body)
				}
			case "save", "search", "start":
				if !res.IsError || body["error_code"] != "project_transition_conflict" {
					t.Fatalf("expected transition conflict: %v", body)
				}
			case "all":
				if res.IsError {
					t.Fatalf("global recovery blocked: %v", body)
				}
			default:
				if res.IsError || body["project"] != "local-app" {
					t.Fatalf("explicit identity must remain usable: %v", body)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
				t.Fatalf("must not publish implicit binding: %v", err)
			}
			rows, err := s.Search("historical marker", store.SearchOptions{Project: "local-app", Limit: 5})
			if err != nil || len(rows) != 1 {
				t.Fatalf("historical memory changed: rows=%d err=%v", len(rows), err)
			}
		})
	}
}
