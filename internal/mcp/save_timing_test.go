package mcp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestHandleSaveTimingOptIn(t *testing.T) {
	for _, mode := range []string{"", "0", "true", "1"} {
		t.Run("mode="+mode, func(t *testing.T) {
			t.Setenv("ENGRAM_MEM_SAVE_TIMING", mode)
			s := newMCPTestStore(t)
			if err := s.CreateSession("timing-session", "timing-test", ""); err != nil {
				t.Fatal(err)
			}
			h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
			call := func(title string) *mcppkg.CallToolResult {
				t.Helper()
				r, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
					"title": title, "content": "private-body-marker", "project": "timing-test", "session_id": "timing-session", "scope": "project", "capture_prompt": false,
				}}})
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			seed := call("private-title-marker authentication tokens")
			if seed.IsError {
				t.Fatal(callResultText(t, seed))
			}
			old := os.Stderr
			pipeR, pipeW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stderr = pipeW
			t.Cleanup(func() { os.Stderr = old; pipeW.Close(); pipeR.Close() })
			result := call("private-title-marker authentication sessions")
			before, err := s.Stats()
			if err != nil {
				t.Fatal(err)
			}
			rejected := call("")
			after, err := s.Stats()
			if err != nil {
				t.Fatal(err)
			}
			if before.TotalObservations != after.TotalObservations || before.TotalSessions != after.TotalSessions || before.TotalPrompts != after.TotalPrompts {
				t.Fatal("rejected input changed counters")
			}
			relations, err := s.CountRelations(store.ListRelationsOptions{})
			if err != nil || relations != 1 {
				t.Fatalf("pending relations = %d, err = %v", relations, err)
			}
			os.Stderr = old
			pipeW.Close()
			output, err := io.ReadAll(pipeR)
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError {
				t.Fatal(callResultText(t, result))
			}
			if !rejected.IsError || callResultText(t, rejected) != "Failed to save: observation title is required" {
				t.Fatalf("empty title rejection changed: %s", callResultText(t, rejected))
			}
			var envelope map[string]any
			if err := json.Unmarshal([]byte(callResultText(t, result)), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope["judgment_required"] != true {
				t.Fatalf("candidate detection changed: %v", envelope)
			}
			candidates := envelope["candidates"].([]any)
			if len(candidates) != 1 {
				t.Fatalf("want one candidate, got %d", len(candidates))
			}
			candidate := candidates[0].(map[string]any)
			judgmentID, ok := candidate["judgment_id"].(string)
			if !ok || judgmentID == "" {
				t.Fatal("pending relation missing")
			}
			relation, err := s.GetRelation(judgmentID)
			if err != nil || relation.JudgmentStatus != store.JudgmentStatusPending {
				t.Fatalf("pending relation: %v, err=%v", relation, err)
			}
			savedID, ok := envelope["id"].(float64)
			if !ok {
				t.Fatal("saved ID missing")
			}
			observation, err := s.GetObservation(int64(savedID))
			if err != nil || observation.Title != "private-title-marker authentication sessions" || observation.Content != "private-body-marker" {
				t.Fatalf("saved memory changed: %v, err=%v", observation, err)
			}
			if mode != "1" {
				if len(output) != 0 {
					t.Fatalf("diagnostics must be disabled: %s", output)
				}
				return
			}
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			if len(lines) != 2 {
				t.Fatalf("want one diagnostic per call, got %q", output)
			}
			for i, line := range lines {
				const prefix = "engram: mem_save_timing "
				if !strings.HasPrefix(line, prefix) {
					t.Fatalf("unexpected diagnostic: %q", line)
				}
				var record map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, prefix)), &record); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"total_ms", "save_ms", "candidate_lookup_ms", "relation_insert_ms"} {
					value, ok := record[key].(float64)
					if !ok || value < 0 {
						t.Fatalf("invalid %s: %v", key, record)
					}
				}
				wantStatus := "saved"
				if i == 1 {
					wantStatus = "not_saved"
				}
				if record["status"] != wantStatus {
					t.Fatalf("status: %v", record)
				}
				if len(record) != 5 {
					t.Fatalf("unexpected fields: %v", record)
				}
			}
			for _, secret := range []string{"private-title-marker", "private-body-marker", "timing-test"} {
				if strings.Contains(string(output), secret) {
					t.Fatalf("private data in diagnostic: %s", output)
				}
			}
		})
	}
}
