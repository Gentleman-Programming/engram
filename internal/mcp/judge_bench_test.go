package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
	"github.com/mark3labs/mcp-go/client"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const judgeMCPBenchmarkProject = "judge-mcp-benchmark"

type judgeMCPBenchmarkFixture struct {
	store *store.Store
	ids   []string
}

type judgeMCPBenchmarkCall func(string) error

func TestJudgeMCPStdioBenchmarkHelper(t *testing.T) {
	if os.Getenv("ENGRAM_JUDGE_BENCH_STDIO") != "1" {
		return
	}
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = os.Getenv("ENGRAM_DATA_DIR")
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := server.ServeStdio(NewServerWithTools(s, map[string]bool{"mem_judge": true})); err != nil {
		t.Error(err)
	}
}

func newJudgeMCPBenchmarkFixture(b *testing.B, enrolled bool) *judgeMCPBenchmarkFixture {
	b.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		b.Fatalf("default store config: %v", err)
	}
	cfg.DataDir = b.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		b.Fatalf("create benchmark store: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	if err := s.CreateSession("judge-mcp-benchmark-session", judgeMCPBenchmarkProject, cfg.DataDir); err != nil {
		b.Fatalf("create benchmark session: %v", err)
	}
	add := func(label string) string {
		id, err := s.AddObservation(store.AddObservationParams{SessionID: "judge-mcp-benchmark-session", Type: "benchmark", Title: "MCP judge benchmark " + label, Content: "benchmark fixture", Project: judgeMCPBenchmarkProject, Scope: "project"})
		if err != nil {
			b.Fatalf("add benchmark %s observation: %v", label, err)
		}
		observation, err := s.GetObservation(id)
		if err != nil {
			b.Fatalf("get benchmark %s observation: %v", label, err)
		}
		return observation.SyncID
	}
	sourceID, targetID := add("source"), add("target")
	ids := make([]string, 3)
	for i := range ids {
		ids[i] = fmt.Sprintf("rel-judge-mcp-benchmark-%d", i+1)
		if _, err := s.SaveRelation(store.SaveRelationParams{SyncID: ids[i], SourceID: sourceID, TargetID: targetID}); err != nil {
			b.Fatalf("seed benchmark relation %d: %v", i+1, err)
		}
	}
	if enrolled {
		if err := s.EnrollProject(judgeMCPBenchmarkProject); err != nil {
			b.Fatalf("enroll benchmark project: %v", err)
		}
	}
	return &judgeMCPBenchmarkFixture{store: s, ids: ids}
}

func judgeMCPBenchmarkRequest(id string) mcppkg.CallToolRequest {
	return mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"judgment_id": id, "relation": "not_conflict", "confidence": 0.9}}}
}

func callJudgeMCPHandler(handler server.ToolHandlerFunc, id string) error {
	result, err := handler(context.Background(), judgeMCPBenchmarkRequest(id))
	if err != nil {
		return err
	}
	if result == nil || result.IsError {
		return errors.New("mem_judge handler returned an error result")
	}
	return nil
}

func callJudgeMCPInProcess(s *server.MCPServer, id string) error {
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_judge", "arguments": judgeMCPBenchmarkRequest(id).Params.Arguments}})
	if err != nil {
		return err
	}
	response, err := json.Marshal(s.HandleMessage(context.Background(), request))
	if err != nil {
		return err
	}
	var envelope struct {
		Error  json.RawMessage `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return err
	}
	if len(envelope.Error) != 0 && string(envelope.Error) != "null" {
		return errors.New("in-process MCP server returned a protocol error")
	}
	var result mcppkg.CallToolResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return err
	}
	if result.IsError {
		return errors.New("in-process MCP server returned an error result")
	}
	return nil
}

func newJudgeMCPStdioCall(b *testing.B, fixture *judgeMCPBenchmarkFixture) judgeMCPBenchmarkCall {
	c, err := client.NewStdioMCPClient(os.Args[0], append(os.Environ(), "ENGRAM_JUDGE_BENCH_STDIO=1", "ENGRAM_DATA_DIR="+fixture.store.DataDir()), "-test.run=^TestJudgeMCPStdioBenchmarkHelper$")
	if err != nil {
		b.Fatalf("start stdio benchmark helper: %v", err)
	}
	b.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	init := mcppkg.InitializeRequest{}
	init.Params.ProtocolVersion = mcppkg.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcppkg.Implementation{Name: "judge-benchmark", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		b.Fatalf("initialize stdio benchmark helper: %v", err)
	}
	return func(id string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		request := judgeMCPBenchmarkRequest(id)
		request.Params.Name = "mem_judge"
		result, err := c.CallTool(ctx, request)
		if err != nil {
			return err
		}
		if result == nil || result.IsError {
			return errors.New("stdio MCP server returned an error result")
		}
		return nil
	}
}

func benchmarkJudgeMCPCalls(b *testing.B, fixture *judgeMCPBenchmarkFixture, call judgeMCPBenchmarkCall, calls int, concurrent bool) {
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !concurrent {
			for _, id := range fixture.ids[:calls] {
				if err := call(id); err != nil {
					b.Fatal(err)
				}
			}
			continue
		}
		b.StopTimer()
		start, errs := make(chan struct{}), make(chan error, calls)
		var ready sync.WaitGroup
		ready.Add(calls)
		for _, id := range fixture.ids[:calls] {
			go func(id string) { ready.Done(); <-start; errs <- call(id) }(id)
		}
		ready.Wait()
		b.StartTimer()
		close(start)
		for range calls {
			if err := <-errs; err != nil {
				b.StopTimer()
				b.Fatal(err)
			}
		}
		b.StopTimer()
	}
}

func benchmarkJudgeMCPLayer(b *testing.B, newCall func(*testing.B, *judgeMCPBenchmarkFixture) judgeMCPBenchmarkCall) {
	for _, scenario := range []struct {
		name                 string
		enrolled, concurrent bool
		calls                int
	}{
		{"One/Unenrolled", false, false, 1}, {"One/Enrolled", true, false, 1},
		{"SequentialThree/Unenrolled", false, false, 3}, {"ConcurrentThree/Unenrolled", false, true, 3},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			fixture := newJudgeMCPBenchmarkFixture(b, scenario.enrolled)
			benchmarkJudgeMCPCalls(b, fixture, newCall(b, fixture), scenario.calls, scenario.concurrent)
		})
	}
}

// BenchmarkJudgeMCP compares direct, queued, and in-process JSON-RPC layers.
// InProcessJSONRPC includes JSON serialization but excludes stdio/process transport.
func BenchmarkJudgeMCP(b *testing.B) {
	for _, layer := range []struct {
		name string
		call func(*testing.B, *judgeMCPBenchmarkFixture) judgeMCPBenchmarkCall
	}{
		{"DirectHandler", func(_ *testing.B, f *judgeMCPBenchmarkFixture) judgeMCPBenchmarkCall {
			handler := handleJudge(f.store, NewSessionActivity(10*time.Minute))
			return func(id string) error { return callJudgeMCPHandler(handler, id) }
		}},
		{"QueuedHandler", func(b *testing.B, f *judgeMCPBenchmarkFixture) judgeMCPBenchmarkCall {
			queue := newWriteQueue(defaultMCPWriteQueueSize)
			b.Cleanup(func() { close(queue.jobs) })
			handler := queuedWriteHandler(queue, handleJudge(f.store, NewSessionActivity(10*time.Minute)))
			return func(id string) error { return callJudgeMCPHandler(handler, id) }
		}},
		{"InProcessJSONRPC", func(_ *testing.B, f *judgeMCPBenchmarkFixture) judgeMCPBenchmarkCall {
			s := NewServerWithTools(f.store, map[string]bool{"mem_judge": true})
			return func(id string) error { return callJudgeMCPInProcess(s, id) }
		}},
		{"StdioProcess", newJudgeMCPStdioCall},
	} {
		b.Run(layer.name, func(b *testing.B) { benchmarkJudgeMCPLayer(b, layer.call) })
	}
}
