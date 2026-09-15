package mcp

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	mcppkg "github.com/mark3labs/mcp-go/mcp"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
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
	id := seedObservation(t, s, "design Part A", "aaTESTyyyyyTESTcc")
	text := callGetObservation(t, s, map[string]any{
		"id":      float64(id),
		"find":    "TEST",
		"context": float64(2),
	})
	if !strings.Contains(text, "2 matches for") {
		t.Fatalf("expected match count, got %q", text)
	}
	if !strings.Contains(text, "[offset 0]") || !strings.Contains(text, "[offset 9]") {
		t.Fatalf("expected window offsets, got %q", text)
	}
	if strings.Contains(text, "yyyyTEST") {
		t.Fatalf("expected distant text to remain outside both windows, got %q", text)
	}
}

func TestHandleGetObservationFindMergesOverlappingOccurrences(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedObservation(t, s, "overlap", "aaaaa")
	text := callGetObservation(t, s, map[string]any{
		"id":      float64(id),
		"find":    "aa",
		"context": float64(0),
	})
	if !strings.Contains(text, "4 matches for") {
		t.Fatalf("expected overlapping occurrence count, got %q", text)
	}
	if strings.Count(text, "[offset ") != 1 || strings.Count(text, "aaaaa") != 1 {
		t.Fatalf("expected one merged window with one body, got %q", text)
	}
}

func TestHandleGetObservationFindDefaultContextDoesNotDuplicateContent(t *testing.T) {
	s := newMCPTestStore(t)
	find := "needle"
	content := strings.Repeat("a", store.DefaultFindContext+100) + find + strings.Repeat("b", 100) + find + strings.Repeat("c", store.DefaultFindContext+100)
	id := seedObservation(t, s, "repeated", content)
	text := callGetObservation(t, s, map[string]any{
		"id":   float64(id),
		"find": find,
	})
	if !strings.Contains(text, "2 matches for") {
		t.Fatalf("expected occurrence count, got %q", text)
	}
	if strings.Count(text, "[offset ") != 1 || strings.Count(text, find) != 3 {
		t.Fatalf("expected one merged body containing each match once, got %q", text)
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

func TestHandleGetObservationRejectsUnsafeOptionalNumbers(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedObservation(t, s, "numbers", "abcdef")
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"fractional offset", map[string]any{"id": float64(id), "offset": 1.5}, "offset must be a safe integer"},
		{"fractional limit", map[string]any{"id": float64(id), "limit": 1.5}, "limit must be a safe integer"},
		{"fractional context", map[string]any{"id": float64(id), "find": "cd", "context": 1.5}, "context must be a safe integer"},
		{"out of range offset", map[string]any{"id": float64(id), "offset": math.MaxFloat64}, "offset must be a safe integer"},
		{"out of range limit", map[string]any{"id": float64(id), "limit": math.MaxFloat64}, "limit must be a safe integer"},
		{"out of range context", map[string]any{"id": float64(id), "find": "cd", "context": math.MaxFloat64}, "context must be a safe integer"},
		{"nan offset", map[string]any{"id": float64(id), "offset": math.NaN()}, "offset must be a safe integer"},
		{"infinite limit", map[string]any{"id": float64(id), "limit": math.Inf(1)}, "limit must be a safe integer"},
		{"wrong type offset", map[string]any{"id": float64(id), "offset": "1"}, "offset must be a safe integer"},
		{"wrong type limit", map[string]any{"id": float64(id), "limit": true}, "limit must be a safe integer"},
		{"wrong type context", map[string]any{"id": float64(id), "find": "cd", "context": "1"}, "context must be a safe integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := handleGetObservation(s, MCPConfig{})(context.Background(), mcppkg.CallToolRequest{
				Params: mcppkg.CallToolParams{Arguments: tc.args},
			})
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}
			if !res.IsError || !strings.Contains(callResultText(t, res), tc.want) {
				t.Fatalf("result = %q, want tool error containing %q", callResultText(t, res), tc.want)
			}
		})
	}
}

func TestHandleGetObservationNegativeOptionalNumbersUseStoreValidation(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedObservation(t, s, "numbers", "abcdef")
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"offset", map[string]any{"id": float64(id), "offset": float64(-1)}, "offset must be >= 0"},
		{"limit", map[string]any{"id": float64(id), "limit": float64(-1)}, "limit must be >= 0"},
		{"context", map[string]any{"id": float64(id), "find": "cd", "context": float64(-1)}, "context must be >= 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := handleGetObservation(s, MCPConfig{})(context.Background(), mcppkg.CallToolRequest{
				Params: mcppkg.CallToolParams{Arguments: tc.args},
			})
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}
			if !res.IsError || !strings.Contains(callResultText(t, res), tc.want) {
				t.Fatalf("result = %q, want store validation %q", callResultText(t, res), tc.want)
			}
		})
	}
}
