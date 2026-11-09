package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"github.com/mark3labs/mcp-go/server"
)

func callFinalizeTool(t *testing.T, srv *server.MCPServer, arguments map[string]any) (map[string]any, bool) {
	t.Helper()
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_session_finalize", "arguments": arguments}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := json.Marshal(srv.HandleMessage(context.Background(), request))
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Error) > 0 || len(wire.Result.Content) != 1 {
		t.Fatalf("unexpected RPC response: %s", response)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(wire.Result.Content[0].Text), &envelope); err != nil {
		t.Fatalf("non-JSON tool response %s: %v", response, err)
	}
	return envelope, wire.Result.IsError
}

func TestSessionFinalizeMCPRegistration(t *testing.T) {
	s := newMCPTestStore(t)
	for _, profile := range []map[string]bool{nil, ProfileAgent, ProfileAdmin, {"mem_session_finalize": true}} {
		if _, ok := NewServerWithTools(s, profile).ListTools()["mem_session_finalize"]; !ok {
			t.Fatal("mem_session_finalize missing")
		}
	}
	if _, ok := NewServerWithTools(s, map[string]bool{"mem_session_summary": true}).ListTools()["mem_session_finalize"]; ok {
		t.Fatal("finalize leaked into explicit summary allowlist")
	}
}

func TestSessionFinalizeMCPAtomicRetry(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("finalize-mcp", "finalize-project", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("finalize-project"); err != nil {
		t.Fatal(err)
	}
	srv := NewServerWithTools(s, map[string]bool{"mem_session_finalize": true})
	summary := "## Goal\nFinalize through public MCP"
	args := map[string]any{"session_id": "finalize-mcp", "summary": summary}
	first, isError := callFinalizeTool(t, srv, args)
	if isError || first["state"] != "finalized" || first["already_finalized"] != false || first["project"] != "finalize-project" {
		t.Fatalf("first: %#v error=%v", first, isError)
	}
	id, ok := first["summary_id"].(float64)
	if !ok {
		t.Fatalf("missing identity: %#v", first)
	}
	obs, err := s.GetObservation(int64(id))
	if err != nil || obs.Content != summary || obs.Type != "session_summary" || obs.SessionID != "finalize-mcp" {
		t.Fatalf("observation: %+v %v", obs, err)
	}
	sess, err := s.GetSession("finalize-mcp")
	if err != nil || sess.EndedAt == nil || sess.Summary != nil {
		t.Fatalf("session: %+v %v", sess, err)
	}
	beforeStats, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	beforeMutations, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	beforeState, err := s.GetSyncState(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatal(err)
	}
	args["summary"] = "Changed retry must not overwrite"
	retry, isError := callFinalizeTool(t, srv, args)
	if isError || retry["state"] != "already_finalized" || retry["already_finalized"] != true || retry["summary_id"] != first["summary_id"] || retry["summary_sync_id"] != first["summary_sync_id"] {
		t.Fatalf("retry: %#v error=%v", retry, isError)
	}
	afterStats, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	afterMutations, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	afterState, err := s.GetSyncState(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeStats, afterStats) || !reflect.DeepEqual(beforeMutations, afterMutations) || !reflect.DeepEqual(beforeState, afterState) {
		t.Fatal("retry changed persistence or sync")
	}
	obs, err = s.GetObservation(int64(id))
	if err != nil || obs.Content != summary {
		t.Fatalf("retry overwrote observation: %+v %v", obs, err)
	}
}

func TestSessionFinalizeMCPPreservesStoredExactIdentity(t *testing.T) {
	for _, withTrimmedSibling := range []bool{false, true} {
		t.Run(map[bool]string{false: "no trimmed sibling", true: "different owner sibling"}[withTrimmedSibling], func(t *testing.T) {
			s := newMCPTestStore(t)
			const exactID = " finalize-padded "
			if err := s.CreateSession(exactID, "finalize-project", ""); err != nil {
				t.Fatal(err)
			}
			if withTrimmedSibling {
				if err := s.CreateSession("finalize-padded", "other-project", ""); err != nil {
					t.Fatal(err)
				}
			}
			result, failed := callFinalizeTool(t, NewServer(s), map[string]any{"session_id": exactID, "summary": "Exact stored identity"})
			if failed || result["session_id"] != exactID || result["project"] != "finalize-project" || result["state"] != "finalized" {
				t.Fatalf("exact identity lost: %#v error=%v", result, failed)
			}
			exact, err := s.GetSession(exactID)
			if err != nil || exact.EndedAt == nil {
				t.Fatalf("exact session not closed: %+v %v", exact, err)
			}
			if withTrimmedSibling {
				sibling, err := s.GetSession("finalize-padded")
				if err != nil || sibling.EndedAt != nil || sibling.Project != "other-project" {
					t.Fatalf("sibling affected: %+v %v", sibling, err)
				}
			} else if _, err := s.GetSession("finalize-padded"); err == nil {
				t.Fatal("implicitly created trimmed sibling")
			}
		})
	}
}

func TestSessionFinalizeMCPRejectedInputsNoWrites(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    map[string]any
		bareEnd bool
		code    string
	}{
		{"missing identity", map[string]any{"summary": "text"}, false, "invalid_arguments"},
		{"wrong identity type", map[string]any{"session_id": 10, "summary": "text"}, false, "invalid_arguments"},
		{"missing summary", map[string]any{"session_id": "finalize-mcp"}, false, "invalid_arguments"},
		{"blank summary", map[string]any{"session_id": "finalize-mcp", "summary": " \n\t"}, false, "invalid_arguments"},
		{"unknown session", map[string]any{"session_id": "missing", "summary": "text"}, false, "unknown_session"},
		{"exact session", map[string]any{"session_id": " finalize-mcp ", "summary": "text"}, false, "unknown_session"},
		{"project mismatch", map[string]any{"session_id": "finalize-mcp", "summary": "text", "project": "other-project"}, false, "session_project_mismatch"},
		{"bare ended", map[string]any{"session_id": "finalize-mcp", "summary": "text"}, true, "session_already_ended"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newMCPTestStore(t)
			if err := s.CreateSession("finalize-mcp", "finalize-project", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.EnrollProject("finalize-project"); err != nil {
				t.Fatal(err)
			}
			if tc.bareEnd {
				if err := s.EndSession("finalize-mcp", "existing free text"); err != nil {
					t.Fatal(err)
				}
			}
			beforeStats, err := s.Stats()
			if err != nil {
				t.Fatal(err)
			}
			beforeSession, err := s.GetSession("finalize-mcp")
			if err != nil {
				t.Fatal(err)
			}
			beforeMutations, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
			if err != nil {
				t.Fatal(err)
			}
			beforeState, err := s.GetSyncState(store.DefaultSyncTargetKey)
			if err != nil {
				t.Fatal(err)
			}
			got, isError := callFinalizeTool(t, NewServer(s), tc.args)
			if !isError || got["error_code"] != tc.code {
				t.Fatalf("response: %#v error=%v want code %s", got, isError, tc.code)
			}
			afterStats, err := s.Stats()
			if err != nil {
				t.Fatal(err)
			}
			afterSession, err := s.GetSession("finalize-mcp")
			if err != nil {
				t.Fatal(err)
			}
			afterMutations, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
			if err != nil {
				t.Fatal(err)
			}
			afterState, err := s.GetSyncState(store.DefaultSyncTargetKey)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeStats, afterStats) || !reflect.DeepEqual(beforeSession, afterSession) || !reflect.DeepEqual(beforeMutations, afterMutations) || !reflect.DeepEqual(beforeState, afterState) {
				t.Fatal("rejected finalize changed stored state")
			}
		})
	}
}
