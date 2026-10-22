package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/embedding"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

type semanticToolProvider struct {
	err   error
	texts [][]string
}

func (p *semanticToolProvider) Resolve(context.Context) (embedding.Identity, error) {
	return embedding.Identity{Model: "local:latest", Digest: strings.Repeat("a", 64)}, p.err
}
func (p *semanticToolProvider) Embed(ctx context.Context, id embedding.Identity, texts []string) ([][]float64, error) {
	p.texts = append(p.texts, append([]string(nil), texts...))
	vectors := make([][]float64, len(texts))
	for i := range vectors {
		vectors[i] = []float64{1, 0}
	}
	return vectors, nil
}

func TestSemanticToolSchemaAndRoundTrip(t *testing.T) {
	p := &semanticToolProvider{}
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()
	cfg.EmbeddingProvider = p
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.CreateSession("semantic", "alpha", "/synthetic"); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(store.AddObservationParams{SessionID: "semantic", Title: "source", Content: "alpha content", Project: "alpha", Scope: "project", Type: "decision"})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(s)
	tool := srv.GetTool("mem_search")
	if tool == nil {
		t.Fatal("missing search tool")
	}
	schema := tool.Tool.InputSchema.Properties["search_mode"]
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"enum":["lexical","semantic"]`) || !strings.Contains(string(encoded), "default") {
		t.Fatalf("schema: %s", encoded)
	}
	if srv.GetTool("mem_embed") != nil {
		t.Fatal("indexing must not be a tool")
	}
	h := handleSearch(s, MCPConfig{}, NewSessionActivity(time.Minute))
	call := func(mode, format string) *mcppkg.CallToolResult {
		t.Helper()
		args := map[string]any{"query": "unrelated", "project": "alpha", "scope": "project", "type": "decision"}
		if mode != "" {
			args["search_mode"] = mode
		}
		if format != "" {
			args["response_format"] = format
		}
		result, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	r := call("semantic", "")
	if !r.IsError || !strings.Contains(callResultText(t, r), "engram embed") || len(p.texts) != 0 {
		t.Fatalf("missing: %+v calls=%v", r, p.texts)
	}
	if result, err := s.BuildSemanticIndex(context.Background(), store.SearchOptions{Project: "alpha"}); err != nil || result.Indexed != 1 {
		t.Fatalf("build %v %v", result, err)
	}
	before, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"", "compact"} {
		r = call("semantic", format)
		text := callResultText(t, r)
		if r.IsError || !strings.Contains(text, `"similarity":1`) || !strings.Contains(text, `"retrieval_mode":"semantic"`) || !strings.Contains(text, "potentially related") {
			t.Fatalf("result: %s", text)
		}
	}
	if len(p.texts) != 3 || !reflect.DeepEqual(p.texts[1], []string{"unrelated"}) || !reflect.DeepEqual(p.texts[2], []string{"unrelated"}) {
		t.Fatalf("query generated documents: %v", p.texts)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled, err := h(ctx, mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"query": "unrelated", "search_mode": "semantic", "all_projects": true}}})
	if err != nil || !canceled.IsError || !strings.Contains(callResultText(t, canceled), "canceled") {
		t.Fatalf("cancellation: %+v %v", canceled, err)
	}
	p.err = embedding.ErrUnavailable
	r = call("semantic", "")
	if !r.IsError || !strings.Contains(callResultText(t, r), "engram embed") {
		t.Fatal(callResultText(t, r))
	}
	for _, mode := range []string{"", "lexical"} {
		r = call(mode, "")
		if r.IsError || strings.Contains(callResultText(t, r), "retrieval_mode") {
			t.Fatal(callResultText(t, r))
		}
	}
	after, err := s.GetObservation(id)
	if err != nil || !reflect.DeepEqual(before, after) || len(p.texts) != 3 {
		t.Fatalf("query changed source or embedded documents: %v calls=%v", err, p.texts)
	}
	p.err = nil
	content := "edited"
	if _, err := s.UpdateObservation(id, store.UpdateObservationParams{Content: &content}); err != nil {
		t.Fatal(err)
	}
	r = call("semantic", "")
	if !r.IsError || !strings.Contains(callResultText(t, r), "engram embed") || len(p.texts) != 3 {
		t.Fatal(callResultText(t, r))
	}
}

func TestSemanticToolInvalidQuery(t *testing.T) {
	h := handleSearch(newMCPTestStore(t), MCPConfig{}, NewSessionActivity(time.Minute))
	for _, query := range []any{nil, true, "   "} {
		r, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"query": query, "search_mode": "semantic", "all_projects": true}}})
		if err != nil || !r.IsError || !strings.Contains(callResultText(t, r), "query must be a nonempty string") {
			t.Fatalf("query=%v result=%+v err=%v", query, r, err)
		}
	}
}

func TestSemanticToolValidation(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSearch(s, MCPConfig{}, NewSessionActivity(time.Minute))
	invalidHandler := handleSearch(nil, MCPConfig{}, nil)
	for _, mode := range []any{nil, true, 23, "", "wrong", "semantic"} {
		args := map[string]any{"query": "hello", "all_projects": true, "search_mode": mode}
		if mode == "semantic" {
			args["match_mode"] = "all"
		}
		r, err := invalidHandler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
		if err != nil || !r.IsError || !strings.Contains(callResultText(t, r), "search_mode") {
			t.Errorf("mode=%v result=%+v err=%v", mode, r, err)
		}
	}
	r, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"query": "hello", "all_projects": true, "search_mode": "semantic"}}})
	if err != nil || !r.IsError || !strings.Contains(callResultText(t, r), "engram embed") {
		t.Fatalf("unconfigured result=%+v err=%v", r, err)
	}
}
