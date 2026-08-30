package mcp

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	mcppkg "github.com/mark3labs/mcp-go/mcp"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func seedObservation(t *testing.T, s *store.Store, title, content string) int64 {
	t.Helper()
	if err := s.CreateSession("s-partial", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-partial",
		Type:      "architecture",
		Title:     title,
		Content:   content,
		Project:   "engram",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	return id
}

func callGetObservation(t *testing.T, s *store.Store, args map[string]any) string {
	t.Helper()
	res, err := handleGetObservation(s, MCPConfig{})(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: args},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", callResultText(t, res))
	}
	return observationResultText(t, callResultText(t, res))
}

func observationResultText(t *testing.T, raw string) string {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return raw
	}
	result, _ := env["result"].(string)
	if result == "" {
		return raw
	}
	return result
}

func TestHandleGetObservationNoParamsUnchanged(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedObservation(t, s, "Full body", "complete text")
	text := callGetObservation(t, s, map[string]any{"id": float64(id)})
	if !strings.Contains(text, "#"+strconv.FormatInt(id, 10)+" [architecture] Full body") {
		t.Fatalf("expected full header, got %q", text)
	}
	if !strings.Contains(text, "complete text") {
		t.Fatalf("expected full content, got %q", text)
	}
}

func TestHandleGetObservationLimitWithoutOffset(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedObservation(t, s, "design Part A", "abcdefghij")
	text := callGetObservation(t, s, map[string]any{
		"id":    float64(id),
		"limit": float64(4),
	})
	if !strings.Contains(text, "[offset 0, limit 4]") {
		t.Fatalf("expected offset 0 for limit-only, got %q", text)
	}
	if !strings.Contains(text, "abcd") {
		t.Fatalf("expected leading slice, got %q", text)
	}
}

func TestHandleGetObservationRange(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedObservation(t, s, "design Part A", "abcdefghij")
	text := callGetObservation(t, s, map[string]any{
		"id":     float64(id),
		"offset": float64(2),
		"limit":  float64(3),
	})
	if !strings.Contains(text, ` — 10 runes total`) {
		t.Fatalf("expected rune total, got %q", text)
	}
	if !strings.Contains(text, "[offset 2, limit 3]") {
		t.Fatalf("expected range marker, got %q", text)
	}
	if !strings.Contains(text, "cde") {
		t.Fatalf("expected ranged slice, got %q", text)
	}
}

func TestHandleGetObservationFindWindows(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedObservation(t, s, "design Part A", "aaTESTbbTESTcc")
	text := callGetObservation(t, s, map[string]any{
		"id":      float64(id),
		"find":    "TEST",
		"context": float64(2),
	})
	if !strings.Contains(text, "2 matches for") {
		t.Fatalf("expected match count, got %q", text)
	}
	if !strings.Contains(text, "[offset 0]") || !strings.Contains(text, "[offset 6]") {
		t.Fatalf("expected window offsets, got %q", text)
	}
}

func TestHandleGetObservationFindNotFound(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedObservation(t, s, "design Part A", "nope")
	text := callGetObservation(t, s, map[string]any{
		"id":   float64(id),
		"find": "TEST",
	})
	if !strings.Contains(text, "0 matches for") {
		t.Fatalf("expected zero matches, got %q", text)
	}
}

func TestHandleGetObservationOffsetPastEnd(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedObservation(t, s, "short", "abc")
	text := callGetObservation(t, s, map[string]any{
		"id":     float64(id),
		"offset": float64(50),
	})
	if !strings.Contains(text, "[offset 50, limit 2,000]") {
		t.Fatalf("expected empty past-end range, got %q", text)
	}
}

func TestHandleGetObservationMutuallyExclusive(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedObservation(t, s, "x", "body")
	res, err := handleGetObservation(s, MCPConfig{})(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id":     float64(id),
			"offset": float64(0),
			"find":   "body",
		}},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected mutually exclusive error")
	}
	if !strings.Contains(callResultText(t, res), "mutually exclusive") {
		t.Fatalf("error = %q", callResultText(t, res))
	}
}

func TestHandleGetObservationContextRequiresFind(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedObservation(t, s, "x", "body")
	res, err := handleGetObservation(s, MCPConfig{})(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id":      float64(id),
			"context": float64(10),
		}},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError || !strings.Contains(callResultText(t, res), "context requires find") {
		t.Fatalf("error = %q", callResultText(t, res))
	}
}

func TestHandleGetObservationRangeUsesRunesNotBytes(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedObservation(t, s, "utf8", "aé😊z")
	text := callGetObservation(t, s, map[string]any{
		"id":     float64(id),
		"offset": float64(1),
		"limit":  float64(2),
	})
	if !strings.Contains(text, "é😊") {
		t.Fatalf("expected rune slice, got %q", text)
	}
}
