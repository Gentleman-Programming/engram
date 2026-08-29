package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func callProjectTool(t *testing.T, h server.ToolHandlerFunc, args map[string]any) *mcppkg.CallToolResult {
	t.Helper()
	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
	if err != nil {
		t.Fatalf("handler returned transport error: %v", err)
	}
	return res
}

// ─── Registration / profile contract ─────────────────────────────────────────

func TestResolveToolsProjectsProfile(t *testing.T) {
	result := ResolveTools("projects")
	expected := []string{
		"mem_project_card", "mem_project_upsert", "mem_task_upsert", "mem_task_list",
		"mem_task_link", "mem_evidence_add", "mem_evidence_list", "mem_runbook_index_sync",
		"mem_runbook_find", "mem_context_pack",
	}
	if len(result) != len(expected) {
		t.Fatalf("projects profile has %d tools, expected %d: %v", len(result), len(expected), result)
	}
	for _, tool := range expected {
		if !result[tool] {
			t.Errorf("projects profile missing tool: %s", tool)
		}
	}
}

func TestNewServerWithToolsProjectsProfile(t *testing.T) {
	s := newMCPTestStore(t)
	srv := NewServerWithTools(s, ResolveTools("projects"))
	tools := srv.ListTools()
	if len(tools) != 10 {
		t.Fatalf("expected exactly 10 tools registered for --tools=projects, got %d: %v", len(tools), toolNames(tools))
	}
}

func toolNames(tools map[string]*server.ServerTool) []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	return names
}

// ─── mem_project_card / mem_project_upsert ───────────────────────────────────

func TestProjectCard_NoCardReturnsHint(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "nextcloud"}
	res := callProjectTool(t, handleProjectCard(s, cfg), map[string]any{})
	envelope := callResultJSON(t, res)
	if !res.IsError {
		t.Fatalf("expected IsError for missing card, got %v", envelope)
	}
	if envelope["code"] != "no_card" {
		t.Fatalf("expected code=no_card, got %v", envelope)
	}
}

func TestProjectUpsertThenCard(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "nextcloud"}

	upsertRes := callProjectTool(t, handleProjectUpsert(s, cfg), map[string]any{
		"display_name": "Nextcloud server + apps amx_*",
		"owner":        "owner@example.com",
	})
	if upsertRes.IsError {
		t.Fatalf("unexpected error: %v", callResultJSON(t, upsertRes))
	}
	envelope := callResultJSON(t, upsertRes)
	result, _ := envelope["result"].(map[string]any)
	created, _ := result["created"].(bool)
	if !created {
		t.Fatalf("expected created=true, got %v", envelope)
	}

	cardRes := callProjectTool(t, handleProjectCard(s, cfg), map[string]any{})
	if cardRes.IsError {
		t.Fatalf("unexpected error: %v", callResultJSON(t, cardRes))
	}
	cardEnvelope := callResultJSON(t, cardRes)
	if cardEnvelope["project"] != "nextcloud" {
		t.Fatalf("expected project=nextcloud, got %v", cardEnvelope)
	}
	cardResult, _ := cardEnvelope["result"].(map[string]any)
	if cardResult["counts"] == nil || cardResult["sync"] == nil {
		t.Fatalf("expected counts and sync sections, got %v", cardResult)
	}
}

func TestProjectUpsert_InvalidSlugRejected(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "Not A Slug!"}
	res := callProjectTool(t, handleProjectUpsert(s, cfg), map[string]any{})
	if !res.IsError {
		t.Fatal("expected an error for an invalid slug")
	}
	envelope := callResultJSON(t, res)
	if envelope["code"] != "invalid_slug" {
		t.Fatalf("expected code=invalid_slug, got %v", envelope)
	}
}

// ─── mem_task_upsert / mem_task_list ─────────────────────────────────────────

func TestTaskUpsert_CreateUpdateAndMissingFields(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "nextcloud"}

	res := callProjectTool(t, handleTaskUpsert(s, cfg), map[string]any{
		"jira_key": "PROJ-10336", "title": "Previews return 503", "kind": "incident",
		"jira_status": "In Develop", "jira_status_category": "indeterminate",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", callResultJSON(t, res))
	}
	envelope := callResultJSON(t, res)
	result := envelope["result"].(map[string]any)
	task := result["task"].(map[string]any)
	if task["state"] != "in_progress" {
		t.Fatalf("expected derived state=in_progress, got %v", task)
	}

	missing := callProjectTool(t, handleTaskUpsert(s, cfg), map[string]any{})
	if !missing.IsError || callResultJSON(t, missing)["code"] != "missing_field" {
		t.Fatalf("expected missing_field error, got %v", callResultJSON(t, missing))
	}

	conflict := callProjectTool(t, handleTaskUpsert(s, MCPConfig{DefaultProject: "middleware"}), map[string]any{
		"jira_key": "PROJ-10336", "title": "x", "kind": "bugfix",
	})
	if !conflict.IsError || callResultJSON(t, conflict)["code"] != "task_key_conflict" {
		t.Fatalf("expected task_key_conflict, got %v", callResultJSON(t, conflict))
	}
}

func TestTaskList_DefaultsToActive(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "nextcloud"}
	callProjectTool(t, handleTaskUpsert(s, cfg), map[string]any{"jira_key": "PROJ-1", "title": "open one", "kind": "bugfix"})
	callProjectTool(t, handleTaskUpsert(s, cfg), map[string]any{"jira_key": "PROJ-2", "title": "done one", "kind": "bugfix", "state": "done"})

	res := callProjectTool(t, handleTaskList(s, cfg), map[string]any{})
	envelope := callResultJSON(t, res)
	result := envelope["result"].(map[string]any)
	if int(result["total"].(float64)) != 1 {
		t.Fatalf("expected 1 active task by default, got %v", result)
	}
}

// ─── mem_task_link ────────────────────────────────────────────────────────────

func TestTaskLink_CrossProjectRejected(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "nextcloud"}
	callProjectTool(t, handleTaskUpsert(s, cfg), map[string]any{"jira_key": "PROJ-1", "title": "t", "kind": "bugfix"})

	if err := s.CreateSession("s1", "middleware", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{SessionID: "s1", Type: "manual", Title: "t", Content: "c", Project: "middleware"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	res := callProjectTool(t, handleTaskLink(s, cfg), map[string]any{
		"task": "PROJ-1", "observation_id": float64(obsID),
	})
	if !res.IsError || callResultJSON(t, res)["code"] != "cross_project_link" {
		t.Fatalf("expected cross_project_link, got %v", callResultJSON(t, res))
	}
}

func TestTaskLink_UnknownTask(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "nextcloud"}
	res := callProjectTool(t, handleTaskLink(s, cfg), map[string]any{"task": "PROJ-999", "observation_id": float64(1)})
	if !res.IsError || callResultJSON(t, res)["code"] != "unknown_task" {
		t.Fatalf("expected unknown_task, got %v", callResultJSON(t, res))
	}
}

// ─── mem_evidence_add / mem_evidence_list ────────────────────────────────────

func TestEvidenceAdd_InvalidSha256(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "nextcloud"}
	callProjectTool(t, handleTaskUpsert(s, cfg), map[string]any{"jira_key": "PROJ-1", "title": "t", "kind": "incident"})

	res := callProjectTool(t, handleEvidenceAdd(s, cfg), map[string]any{
		"task": "PROJ-1", "path": "a.png", "sha256": "not-a-hash", "kind": "png", "proves": "p",
	})
	if !res.IsError || callResultJSON(t, res)["code"] != "invalid_sha256" {
		t.Fatalf("expected invalid_sha256, got %v", callResultJSON(t, res))
	}
}

func TestEvidenceAdd_AbsolutePathRejected(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "nextcloud"}
	callProjectTool(t, handleTaskUpsert(s, cfg), map[string]any{"jira_key": "PROJ-1", "title": "t", "kind": "incident"})

	res := callProjectTool(t, handleEvidenceAdd(s, cfg), map[string]any{
		"task": "PROJ-1", "path": "/etc/passwd", "sha256": "9f2b1c0a7e4d5b6c8a1f3e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f0e1d2c3b",
		"kind": "png", "proves": "p",
	})
	if !res.IsError || callResultJSON(t, res)["code"] != "absolute_path_rejected" {
		t.Fatalf("expected absolute_path_rejected, got %v", callResultJSON(t, res))
	}
}

func TestEvidenceAddAndList_Duplicate(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "nextcloud"}
	callProjectTool(t, handleTaskUpsert(s, cfg), map[string]any{"jira_key": "PROJ-1", "title": "t", "kind": "incident"})

	sha := "9f2b1c0a7e4d5b6c8a1f3e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f0e1d2c3b"
	args := map[string]any{"task": "PROJ-1", "path": "a.png", "sha256": sha, "kind": "png", "proves": "p"}
	first := callProjectTool(t, handleEvidenceAdd(s, cfg), args)
	if first.IsError {
		t.Fatalf("unexpected error: %v", callResultJSON(t, first))
	}
	second := callProjectTool(t, handleEvidenceAdd(s, cfg), args)
	result := callResultJSON(t, second)["result"].(map[string]any)
	if dup, _ := result["duplicate"].(bool); !dup {
		t.Fatalf("expected duplicate=true on the second call, got %v", result)
	}

	listRes := callProjectTool(t, handleEvidenceList(s, cfg), map[string]any{"task": "PROJ-1"})
	listResult := callResultJSON(t, listRes)["result"].(map[string]any)
	if int(listResult["total"].(float64)) != 1 {
		t.Fatalf("expected exactly 1 evidence row, got %v", listResult)
	}
}

// ─── mem_runbook_index_sync / mem_runbook_find ───────────────────────────────

func TestRunbookIndexSync_SkipsTemplate(t *testing.T) {
	s := newMCPTestStore(t)
	res := callProjectTool(t, handleRunbookIndexSync(s), map[string]any{
		"entries": []any{
			map[string]any{
				"id": "RB-000", "vault_path": "Runbooks/Templates/Auth Issue Template.md", "title": "tpl",
				"service": "middleware", "category": "auth", "status": "draft", "tags": []any{"template"},
			},
		},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", callResultJSON(t, res))
	}
	var out map[string]any
	if err := unmarshalToolText(t, res, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	skipped, _ := out["skipped"].([]any)
	if len(skipped) != 1 {
		t.Fatalf("expected 1 skipped entry, got %v", out)
	}
}

func TestRunbookIndexSync_InvalidStatusIsSkippedNotRejected(t *testing.T) {
	// status outside the enum is documented as a `skipped` case (RFC §5.8),
	// not a hard validation failure — it must not produce entries_rejected.
	s := newMCPTestStore(t)
	res := callProjectTool(t, handleRunbookIndexSync(s), map[string]any{
		"entries": []any{
			map[string]any{
				"id": "RB-001", "vault_path": "Runbooks/RB-001.md", "title": "t",
				"service": "nextcloud", "category": "auth", "status": "open",
			},
		},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", callResultJSON(t, res))
	}
	var out map[string]any
	if err := unmarshalToolText(t, res, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	skipped, _ := out["skipped"].([]any)
	if len(skipped) != 1 {
		t.Fatalf("expected 1 skipped entry (invalid_status), got %v", out)
	}
}

func TestRunbookIndexSync_AllRejectedReturnsError(t *testing.T) {
	s := newMCPTestStore(t)
	res := callProjectTool(t, handleRunbookIndexSync(s), map[string]any{
		"entries": []any{
			map[string]any{
				"id": "RB-001", "vault_path": "Runbooks/RB-001.md",
				// title/service/category missing: fails structural parsing.
			},
		},
	})
	if !res.IsError || callResultJSON(t, res)["code"] != "entries_rejected" {
		t.Fatalf("expected entries_rejected, got %v", callResultJSON(t, res))
	}
}

func TestRunbookFind_Contract(t *testing.T) {
	s := newMCPTestStore(t)
	callProjectTool(t, handleRunbookIndexSync(s), map[string]any{
		"entries": []any{
			map[string]any{
				"id": "RB-003", "vault_path": "Runbooks/RB-003.md", "title": "Preview endpoint slow or failing",
				"service": "nextcloud", "category": "performance", "status": "verified",
				"symptoms": []any{"GET /index.php/core/preview returns 503"},
			},
		},
	})
	res := callProjectTool(t, handleRunbookFind(s), map[string]any{"query": "preview 503", "project": "nextcloud"})
	if res.IsError {
		t.Fatalf("unexpected error: %v", callResultJSON(t, res))
	}
	var out map[string]any
	if err := unmarshalToolText(t, res, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if int(out["total"].(float64)) != 1 {
		t.Fatalf("expected 1 match, got %v", out)
	}
}

func TestRunbookFind_MissingQuery(t *testing.T) {
	s := newMCPTestStore(t)
	res := callProjectTool(t, handleRunbookFind(s), map[string]any{})
	if !res.IsError || callResultJSON(t, res)["code"] != "missing_field" {
		t.Fatalf("expected missing_field, got %v", callResultJSON(t, res))
	}
}

// ─── mem_context_pack ─────────────────────────────────────────────────────────

func TestContextPack_MarkdownAndJSON(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "nextcloud"}
	callProjectTool(t, handleTaskUpsert(s, cfg), map[string]any{"jira_key": "PROJ-1", "title": "Fix previews", "kind": "incident"})

	md := callProjectTool(t, handleContextPack(s, cfg), map[string]any{"task": "PROJ-1"})
	if md.IsError {
		t.Fatalf("unexpected error: %v", callResultJSON(t, md))
	}
	mdEnvelope := callResultJSON(t, md)
	if _, ok := mdEnvelope["result"].(string); !ok {
		t.Fatalf("expected markdown result to be a string, got %T", mdEnvelope["result"])
	}

	js := callProjectTool(t, handleContextPack(s, cfg), map[string]any{"task": "PROJ-1", "format": "json"})
	jsEnvelope := callResultJSON(t, js)
	if _, ok := jsEnvelope["result"].(map[string]any); !ok {
		t.Fatalf("expected json result to be an object, got %T", jsEnvelope["result"])
	}
}

func TestContextPack_UnknownTask(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "nextcloud"}
	res := callProjectTool(t, handleContextPack(s, cfg), map[string]any{"task": "PROJ-999"})
	if !res.IsError || callResultJSON(t, res)["code"] != "unknown_task" {
		t.Fatalf("expected unknown_task, got %v", callResultJSON(t, res))
	}
}

func unmarshalToolText(t *testing.T, res *mcppkg.CallToolResult, out any) error {
	t.Helper()
	return json.Unmarshal([]byte(callResultText(t, res)), out)
}
