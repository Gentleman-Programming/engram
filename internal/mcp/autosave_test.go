package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ─── hasTrigger ──────────────────────────────────────────────────────────────

func TestHasTriggerDisabledReturnsFalse(t *testing.T) {
	cfg := AutoSaveConfig{Enabled: false}
	if hasTrigger(cfg, autoSaveTriggerSessionEnd) {
		t.Fatal("expected false when Enabled=false")
	}
	if hasTrigger(cfg, autoSaveTriggerPostToolUse) {
		t.Fatal("expected false when Enabled=false")
	}
}

func TestHasTriggerEnabledEmptyTriggersDefaultsToSessionEnd(t *testing.T) {
	cfg := AutoSaveConfig{Enabled: true}
	if !hasTrigger(cfg, autoSaveTriggerSessionEnd) {
		t.Fatal("expected session_end to be implied when Triggers is empty")
	}
	if hasTrigger(cfg, autoSaveTriggerPostToolUse) {
		t.Fatal("expected post_tool_use to be absent when Triggers is empty")
	}
}

func TestHasTriggerExplicitTriggers(t *testing.T) {
	cfg := AutoSaveConfig{
		Enabled:  true,
		Triggers: []string{autoSaveTriggerPostToolUse},
	}
	if hasTrigger(cfg, autoSaveTriggerSessionEnd) {
		t.Fatal("session_end should not be active when not in Triggers list")
	}
	if !hasTrigger(cfg, autoSaveTriggerPostToolUse) {
		t.Fatal("post_tool_use should be active")
	}
}

func TestHasTriggerBothTriggers(t *testing.T) {
	cfg := AutoSaveConfig{
		Enabled:  true,
		Triggers: []string{autoSaveTriggerSessionEnd, autoSaveTriggerPostToolUse},
	}
	if !hasTrigger(cfg, autoSaveTriggerSessionEnd) {
		t.Fatal("session_end should be active")
	}
	if !hasTrigger(cfg, autoSaveTriggerPostToolUse) {
		t.Fatal("post_tool_use should be active")
	}
}

// ─── buildAutoSaveContent ─────────────────────────────────────────────────────

func TestBuildAutoSaveContentGroupsByType(t *testing.T) {
	observations := []store.Observation{
		{Type: "decision", Title: "Use JWT for auth"},
		{Type: "bugfix", Title: "Fix N+1 query"},
		{Type: "decision", Title: "Chose SQLite over Postgres"},
	}

	content := buildAutoSaveContent("sess-001", observations)

	if !strings.Contains(content, "session: sess-001") && !strings.Contains(content, "Session: sess-001") {
		t.Fatalf("content should include session ID, got: %q", content)
	}
	if !strings.Contains(content, "decision") {
		t.Fatalf("content should include type 'decision', got: %q", content)
	}
	if !strings.Contains(content, "bugfix") {
		t.Fatalf("content should include type 'bugfix', got: %q", content)
	}
	if !strings.Contains(content, "Use JWT for auth") {
		t.Fatalf("content should include observation title, got: %q", content)
	}
	if !strings.Contains(content, "Fix N+1 query") {
		t.Fatalf("content should include observation title, got: %q", content)
	}
}

func TestBuildAutoSaveContentSkipsAutoSavedObservations(t *testing.T) {
	autoSource := autoSaveSource
	observations := []store.Observation{
		{Type: "session_summary", Title: "Previous auto-save", ToolName: &autoSource},
		{Type: "decision", Title: "Real decision"},
	}

	content := buildAutoSaveContent("sess-001", observations)

	if strings.Contains(content, "Previous auto-save") {
		t.Fatalf("content should NOT include previous auto-save titles, got: %q", content)
	}
	if !strings.Contains(content, "Real decision") {
		t.Fatalf("content should include non-auto-save titles, got: %q", content)
	}
}

// ─── performSessionEndAutoSave ────────────────────────────────────────────────

func TestPerformSessionEndAutoSaveNoObservations(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("empty-sess", "testproject", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Should not save anything — no observations in session.
	if err := performSessionEndAutoSave(s, "empty-sess", "testproject"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	obs, err := s.AllObservations("testproject", "project", 100)
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(obs) != 0 {
		t.Fatalf("expected 0 observations, got %d", len(obs))
	}
}

func TestPerformSessionEndAutoSaveCreatesConsolidationObservation(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("sess-abc", "proj-x", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Add a couple of observations to the session.
	_, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-abc",
		Type:      "decision",
		Title:     "Use gRPC",
		Content:   "We decided to use gRPC for service communication.",
		Project:   "proj-x",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	_, err = s.AddObservation(store.AddObservationParams{
		SessionID: "sess-abc",
		Type:      "bugfix",
		Title:     "Fix race condition",
		Content:   "Fixed a race condition in the request handler.",
		Project:   "proj-x",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	if err := performSessionEndAutoSave(s, "sess-abc", "proj-x"); err != nil {
		t.Fatalf("auto-save: %v", err)
	}

	obs, err := s.AllObservations("proj-x", "project", 100)
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}

	// Expect 2 original + 1 auto-save = 3 total.
	if len(obs) != 3 {
		t.Fatalf("expected 3 observations (2 original + 1 auto-save), got %d", len(obs))
	}

	// Find the auto-save observation.
	var autoSaveObs *store.Observation
	for i := range obs {
		if obs[i].ToolName != nil && *obs[i].ToolName == autoSaveSource {
			autoSaveObs = &obs[i]
			break
		}
	}
	if autoSaveObs == nil {
		t.Fatalf("expected an observation with ToolName=%q", autoSaveSource)
	}
	if autoSaveObs.Type != "session_summary" {
		t.Fatalf("expected type=session_summary, got %q", autoSaveObs.Type)
	}
	if !strings.Contains(autoSaveObs.Content, "Use gRPC") {
		t.Fatalf("auto-save content should reference observation titles, got: %q", autoSaveObs.Content)
	}
	if !strings.Contains(autoSaveObs.Content, "Fix race condition") {
		t.Fatalf("auto-save content should reference observation titles, got: %q", autoSaveObs.Content)
	}
	// topic_key should be set for dedup.
	if autoSaveObs.TopicKey == nil {
		t.Fatalf("expected auto-save to have a topic_key for idempotency")
	}
	if !strings.HasPrefix(*autoSaveObs.TopicKey, autoSaveTopicKeyPrefix) {
		t.Fatalf("expected topic_key to start with %q, got %q", autoSaveTopicKeyPrefix, *autoSaveObs.TopicKey)
	}
}

func TestPerformSessionEndAutoSaveIsIdempotent(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("sess-dedup", "proj-y", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-dedup",
		Type:      "decision",
		Title:     "Idempotency test decision",
		Content:   "This is the content for the idempotency test.",
		Project:   "proj-y",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Call twice — should upsert, not create two auto-save observations.
	for i := 0; i < 2; i++ {
		if err := performSessionEndAutoSave(s, "sess-dedup", "proj-y"); err != nil {
			t.Fatalf("call %d: auto-save error: %v", i+1, err)
		}
	}

	obs, err := s.AllObservations("proj-y", "project", 100)
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}

	// 1 original + 1 auto-save (upserted, not duplicated).
	if len(obs) != 2 {
		t.Fatalf("expected 2 observations (idempotent upsert), got %d", len(obs))
	}

	// Count auto-saves.
	autoCount := 0
	for _, o := range obs {
		if o.ToolName != nil && *o.ToolName == autoSaveSource {
			autoCount++
		}
	}
	if autoCount != 1 {
		t.Fatalf("expected exactly 1 auto-save observation (idempotent), got %d", autoCount)
	}
}

// ─── handleSessionEnd with auto-save ─────────────────────────────────────────

func TestHandleSessionEndTriggersAutoSaveWhenEnabled(t *testing.T) {
	s := newMCPTestStore(t)

	// Set up a session with an observation.
	if err := s.CreateSession("auto-sess-1", "auto-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := s.AddObservation(store.AddObservationParams{
		SessionID: "auto-sess-1",
		Type:      "pattern",
		Title:     "Use table-driven tests",
		Content:   "Prefer table-driven tests for all handler coverage.",
		Project:   "auto-proj",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	cfg := MCPConfig{
		DefaultProject: "auto-proj",
		AutoSave: AutoSaveConfig{
			Enabled:  true,
			Triggers: []string{autoSaveTriggerSessionEnd},
		},
	}
	activity := NewSessionActivity(10 * time.Minute)
	h := handleSessionEnd(s, cfg, activity)

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":      "auto-sess-1",
		"summary": "Wrapped up the testing session.",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", callResultText(t, res))
	}

	// Verify an auto-save observation was created.
	obs, err := s.AllObservations("auto-proj", "project", 100)
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}

	var found bool
	for _, o := range obs {
		if o.ToolName != nil && *o.ToolName == autoSaveSource {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected an auto-save observation after session-end, got observations: %v", obs)
	}
}

func TestHandleSessionEndNoAutoSaveWhenDisabled(t *testing.T) {
	s := newMCPTestStore(t)

	if err := s.CreateSession("no-auto-sess", "no-auto-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := s.AddObservation(store.AddObservationParams{
		SessionID: "no-auto-sess",
		Type:      "pattern",
		Title:     "Some pattern",
		Content:   "Pattern content that should not be auto-saved.",
		Project:   "no-auto-proj",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// AutoSave disabled (default).
	cfg := MCPConfig{DefaultProject: "no-auto-proj"}
	activity := NewSessionActivity(10 * time.Minute)
	h := handleSessionEnd(s, cfg, activity)

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id": "no-auto-sess",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", callResultText(t, res))
	}

	obs, err := s.AllObservations("no-auto-proj", "project", 100)
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}

	// Only the 1 original observation; no auto-save.
	if len(obs) != 1 {
		t.Fatalf("expected 1 observation (no auto-save), got %d", len(obs))
	}
	for _, o := range obs {
		if o.ToolName != nil && *o.ToolName == autoSaveSource {
			t.Fatalf("unexpected auto-save observation when AutoSave.Enabled=false")
		}
	}
}

// ─── wrapWithPostToolUseCapture ───────────────────────────────────────────────

func TestWrapWithPostToolUseCapturNoOpWhenDisabled(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{} // AutoSave.Enabled=false

	called := false
	h := server.ToolHandlerFunc(func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		called = true
		return mcppkg.NewToolResultText("result"), nil
	})

	wrapped := wrapWithPostToolUseCapture(h, s, cfg)

	// Wrapped function should be the same pointer when disabled (no-op).
	// We verify by checking that the original handler is still called and
	// no passive capture happens.
	if err := s.CreateSession("wrap-sess", "wrap-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := wrapped(context.Background(), mcppkg.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected underlying handler to be called")
	}

	// No observations should have been created.
	obs, _ := s.AllObservations("wrap-proj", "project", 100)
	if len(obs) != 0 {
		t.Fatalf("expected 0 observations, got %d", len(obs))
	}
}

func TestWrapWithPostToolUseCaptureExtractsLearnings(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("ptuse-sess", "ptuse-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := MCPConfig{
		DefaultProject: "ptuse-proj",
		AutoSave: AutoSaveConfig{
			Enabled:  true,
			Triggers: []string{autoSaveTriggerPostToolUse},
		},
	}

	// Handler that returns a result containing a Key Learnings section.
	resultText := `Summary of work done.

## Key Learnings:
1. Always use context cancellation in long-running goroutines to avoid leaks.
2. The normalized_hash column prevents duplicate observations from being stored.
`
	h := server.ToolHandlerFunc(func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		return mcppkg.NewToolResultText(resultText), nil
	})

	wrapped := wrapWithPostToolUseCapture(h, s, cfg)
	_, err := wrapped(context.Background(), mcppkg.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify learnings were captured as observations.
	obs, err := s.AllObservations("ptuse-proj", "project", 100)
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(obs) == 0 {
		t.Fatalf("expected captured learning observations, got none")
	}

	// All auto-captures should carry the "auto" tool name.
	for _, o := range obs {
		if o.ToolName == nil || *o.ToolName != autoSaveSource {
			t.Fatalf("expected ToolName=%q, got %v", autoSaveSource, o.ToolName)
		}
	}
}

func TestWrapWithPostToolUseCaptureSkipsErrorResults(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("ptu-err-sess", "ptu-err-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := MCPConfig{
		DefaultProject: "ptu-err-proj",
		AutoSave: AutoSaveConfig{
			Enabled:  true,
			Triggers: []string{autoSaveTriggerPostToolUse},
		},
	}

	// Handler that returns an error result with a Key Learnings section.
	h := server.ToolHandlerFunc(func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		r := mcppkg.NewToolResultError("## Key Learnings:\n1. Error results should not be scanned.")
		return r, nil
	})

	wrapped := wrapWithPostToolUseCapture(h, s, cfg)
	_, err := wrapped(context.Background(), mcppkg.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	obs, _ := s.AllObservations("ptu-err-proj", "project", 100)
	if len(obs) != 0 {
		t.Fatalf("expected 0 observations (error results skipped), got %d", len(obs))
	}
}

func TestWrapWithPostToolUseCaptureSkipsResultsWithoutLearnings(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{
		DefaultProject: "ptu-noop-proj",
		AutoSave: AutoSaveConfig{
			Enabled:  true,
			Triggers: []string{autoSaveTriggerPostToolUse},
		},
	}

	h := server.ToolHandlerFunc(func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		return mcppkg.NewToolResultText("No learnings section here. Just plain text."), nil
	})

	wrapped := wrapWithPostToolUseCapture(h, s, cfg)
	_, err := wrapped(context.Background(), mcppkg.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No observations created because no "## Key Learnings:" section exists.
	if err := s.CreateSession("ptu-noop-sess", "ptu-noop-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obs, _ := s.AllObservations("ptu-noop-proj", "project", 100)
	if len(obs) != 0 {
		t.Fatalf("expected 0 observations, got %d", len(obs))
	}
}

func TestWrapWithPostToolUseCaptureDeduplicates(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("ptu-dedup-sess", "ptu-dedup-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := MCPConfig{
		DefaultProject: "ptu-dedup-proj",
		AutoSave: AutoSaveConfig{
			Enabled:  true,
			Triggers: []string{autoSaveTriggerPostToolUse},
		},
	}

	resultText := `## Key Learnings:
1. Use context.WithTimeout for all outbound HTTP requests to avoid hanging.
`
	h := server.ToolHandlerFunc(func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		return mcppkg.NewToolResultText(resultText), nil
	})
	wrapped := wrapWithPostToolUseCapture(h, s, cfg)

	// Call twice with the same content — second call should be a dedup.
	for i := 0; i < 2; i++ {
		_, err := wrapped(context.Background(), mcppkg.CallToolRequest{})
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
	}

	obs, err := s.AllObservations("ptu-dedup-proj", "project", 100)
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	// Only 1 observation — second call deduped via normalized_hash.
	if len(obs) != 1 {
		t.Fatalf("expected 1 observation after dedup, got %d", len(obs))
	}
}
