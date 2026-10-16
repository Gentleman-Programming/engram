package mcp

import (
	"context"
	"encoding/json"
	"errors"
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
			t.Cleanup(func() {
				os.Stderr = old
				for _, pipe := range []*os.File{pipeW, pipeR} {
					if err := pipe.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
						t.Errorf("close diagnostic pipe: %v", err)
					}
				}
			})
			result := call("private-title-marker authentication sessions")
			before, err := s.Stats()
			if err != nil {
				t.Fatal(err)
			}
			rejected := call("")
			// Make only this isolated fixture connection read-only. Valid
			// input reaches AddObservation, but its write must fail.
			if _, err := s.DB().Exec("PRAGMA query_only = ON"); err != nil {
				t.Fatal(err)
			}
			_, writeErr := s.AddObservation(store.AddObservationParams{
				SessionID: "timing-session", Project: "timing-test", Scope: "project",
				Title: "private-title-marker failed save", Content: "private-body-marker", Type: "manual",
			})
			if writeErr == nil {
				t.Fatal("read-only fixture must reject AddObservation")
			}
			failedSave := call("private-title-marker failed save")
			if _, err := s.DB().Exec("PRAGMA query_only = OFF"); err != nil {
				t.Fatal(err)
			}
			if !failedSave.IsError || callResultText(t, failedSave) != "Failed to save: "+writeErr.Error() {
				t.Fatalf("store failure changed: %s", callResultText(t, failedSave))
			}
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
			if err := pipeW.Close(); err != nil {
				t.Fatalf("close diagnostic writer: %v", err)
			}
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
			if len(lines) != 3 {
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
				if i > 0 {
					wantStatus = "not_saved"
					if record["candidate_lookup_ms"] != float64(0) || record["relation_insert_ms"] != float64(0) {
						t.Fatalf("failed save reached candidate detection: %v", record)
					}
				}
				if i == 1 && record["save_ms"] != float64(0) {
					t.Fatalf("title rejection reached AddObservation: %v", record)
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
