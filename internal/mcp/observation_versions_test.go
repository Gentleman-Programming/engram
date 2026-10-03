package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestGetObservationWithoutIncludeHistoryIsUnchanged(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Use middleware for JWT validation.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Build history so the default path must prove it stays hidden.
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Move auth to gateway.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	}); err != nil {
		t.Fatalf("upsert observation: %v", err)
	}

	handler := handleGetObservation(s, MCPConfig{})
	res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id": float64(id),
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	envelope := callResultJSON(t, res)
	if _, ok := envelope["version_count"]; ok {
		t.Fatalf("version_count must be absent when include_history is omitted, got %v", envelope["version_count"])
	}
	if _, ok := envelope["history"]; ok {
		t.Fatalf("history must be absent when include_history is omitted")
	}
	text, ok := envelope["result"].(string)
	if !ok {
		t.Fatalf("result is not a string: %T", envelope["result"])
	}
	if strings.Contains(text, "History") || strings.Contains(text, "Version 1") {
		t.Fatalf("default output leaked history: %q", text)
	}
	if !strings.Contains(text, "gateway") {
		t.Fatalf("expected current content in default output, got %q", text)
	}
}

func TestGetObservationWithIncludeHistoryRendersVersions(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "JWT only.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	for _, content := range []string{"JWT + refresh rotation.", "OAuth2 via gateway."} {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "s1",
			Type:      "architecture",
			Title:     "Auth architecture",
			Content:   content,
			Project:   "engram",
			Scope:     "project",
			TopicKey:  "architecture/auth-model",
		}); err != nil {
			t.Fatalf("upsert %q: %v", content, err)
		}
	}

	handler := handleGetObservation(s, MCPConfig{})
	res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":              float64(id),
		"include_history": true,
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	envelope := callResultJSON(t, res)
	if envelope["version_count"] != float64(2) {
		t.Fatalf("version_count = %v, want 2", envelope["version_count"])
	}
	if _, ok := envelope["history_truncated"]; ok {
		t.Fatalf("history_truncated must be absent when the full history fits one page")
	}
	if _, ok := envelope["history_from_version"]; ok {
		t.Fatalf("history_from_version must be absent when nothing is omitted")
	}
	if _, ok := envelope["history_cursor"]; ok {
		t.Fatalf("history_cursor must be absent when no older versions remain")
	}
	history, ok := envelope["history"].([]any)
	if !ok {
		t.Fatalf("history is not an array: %T", envelope["history"])
	}
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}

	// Each rendered history entry carries the full contract: version, title,
	// content, and created_at. version/title/content are fixture-controlled and
	// asserted exactly; created_at is the database capture timestamp, asserted
	// present and non-empty because the DB stamps it at capture time.
	wantHistory := []struct {
		version int
		title   string
		content string
	}{
		{version: 1, title: "Auth architecture", content: "JWT only."},
		{version: 2, title: "Auth architecture", content: "JWT + refresh rotation."},
	}
	for i, raw := range history {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("history[%d] is not an object: %T", i, raw)
		}
		want := wantHistory[i]
		if entry["version"] != float64(want.version) {
			t.Fatalf("history[%d].version = %v, want %d", i, entry["version"], want.version)
		}
		if entry["title"] != want.title {
			t.Fatalf("history[%d].title = %v, want %q", i, entry["title"], want.title)
		}
		if entry["content"] != want.content {
			t.Fatalf("history[%d].content = %v, want %q", i, entry["content"], want.content)
		}
		createdAt, ok := entry["created_at"].(string)
		if !ok || createdAt == "" {
			t.Fatalf("history[%d].created_at = %v, want a non-empty capture timestamp string", i, entry["created_at"])
		}
	}

	text, ok := envelope["result"].(string)
	if !ok {
		t.Fatalf("result is not a string: %T", envelope["result"])
	}
	if !strings.Contains(text, "Version 1") || !strings.Contains(text, "Version 2") {
		t.Fatalf("history section headers missing from output: %q", text)
	}
	if !strings.Contains(text, "JWT only.") || !strings.Contains(text, "JWT + refresh rotation.") {
		t.Fatalf("previous contents missing from history section: %q", text)
	}
	if !strings.Contains(text, "OAuth2 via gateway.") {
		t.Fatalf("current content missing from output: %q", text)
	}
}

func TestGetObservationIncludeHistoryEmptyHistoryStillReportsCount(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s1",
		Type:      "note",
		Title:     "Touched once",
		Content:   "No evolution yet.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	handler := handleGetObservation(s, MCPConfig{})
	res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":              float64(id),
		"include_history": true,
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	envelope := callResultJSON(t, res)
	if envelope["version_count"] != float64(0) {
		t.Fatalf("version_count = %v, want 0", envelope["version_count"])
	}
	// The response contract always carries history as an array when
	// include_history is requested: an observation with no captured versions
	// renders an empty array, never an absent key or null.
	history, ok := envelope["history"].([]any)
	if !ok {
		t.Fatalf("history must be present as an array, got %T (%v)", envelope["history"], envelope["history"])
	}
	if len(history) != 0 {
		t.Fatalf("history = %v, want empty", history)
	}
	text, _ := envelope["result"].(string)
	if strings.Contains(text, "History") {
		t.Fatalf("empty history should not render a history section, got %q", text)
	}
}

// seedIncludeHistoryObservation creates an observation with the given number of
// captured versions (contents state-00 ... state-N) and returns its id.
func seedIncludeHistoryObservation(t *testing.T, s *store.Store, versionCount int) int64 {
	t.Helper()
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var id int64
	for i := 0; i <= versionCount; i++ {
		var err error
		id, err = s.AddObservation(store.AddObservationParams{
			SessionID: "s1",
			Type:      "architecture",
			Title:     "Auth architecture",
			Content:   fmt.Sprintf("state-%02d", i),
			Project:   "engram",
			Scope:     "project",
			TopicKey:  "architecture/auth-model",
		})
		if err != nil {
			t.Fatalf("add state %d: %v", i, err)
		}
	}
	return id
}

func TestGetObservationIncludeHistoryTruncatesToBoundedPage(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedIncludeHistoryObservation(t, s, 52)

	handler := handleGetObservation(s, MCPConfig{})
	res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":              float64(id),
		"include_history": true,
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	envelope := callResultJSON(t, res)
	if envelope["version_count"] != float64(52) {
		t.Fatalf("version_count = %v, want truthful total 52", envelope["version_count"])
	}
	history, ok := envelope["history"].([]any)
	if !ok {
		t.Fatalf("history is not an array: %T", envelope["history"])
	}
	if len(history) != store.DefaultObservationVersionPageSize {
		t.Fatalf("history length = %d, want bounded page of %d", len(history), store.DefaultObservationVersionPageSize)
	}
	if envelope["history_truncated"] != true {
		t.Fatalf("history_truncated = %v, want true", envelope["history_truncated"])
	}
	if envelope["history_from_version"] != float64(3) {
		t.Fatalf("history_from_version = %v, want 3", envelope["history_from_version"])
	}
	if envelope["history_cursor"] != float64(3) {
		t.Fatalf("history_cursor = %v, want 3", envelope["history_cursor"])
	}

	// The bounded page must stay chronological: version 3 is the oldest shown.
	first, ok := history[0].(map[string]any)
	if !ok || first["version"] != float64(3) {
		t.Fatalf("first history entry = %v, want version 3", history[0])
	}
	last, ok := history[len(history)-1].(map[string]any)
	if !ok || last["version"] != float64(52) {
		t.Fatalf("last history entry = %v, want version 52", history[len(history)-1])
	}

	text, ok := envelope["result"].(string)
	if !ok {
		t.Fatalf("result is not a string: %T", envelope["result"])
	}
	if !strings.Contains(text, "History (50 of 52 previous versions — older versions omitted):") {
		t.Fatalf("truncation not stated in output: %q", text)
	}
	if !strings.Contains(text, "Version 3") || !strings.Contains(text, "state-02") {
		t.Fatalf("oldest shown version missing from output: %q", text)
	}
	if strings.Contains(text, "state-00") || strings.Contains(text, "state-01") {
		t.Fatalf("omitted older versions leaked into output: %q", text)
	}
	if !strings.Contains(text, "state-52") {
		t.Fatalf("current content missing from output: %q", text)
	}

	// The emitted cursor must be usable: a follow-up request submitting it
	// fetches the strictly older page without gaps or duplicates.
	cursor, ok := envelope["history_cursor"].(float64)
	if !ok {
		t.Fatalf("history_cursor is not a number: %T", envelope["history_cursor"])
	}
	res2, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":              float64(id),
		"include_history": true,
		"history_cursor":  cursor,
	}}})
	if err != nil {
		t.Fatalf("cursor handler error: %v", err)
	}
	if res2.IsError {
		t.Fatalf("cursor request unexpected error: %s", callResultText(t, res2))
	}

	envelope2 := callResultJSON(t, res2)
	if envelope2["version_count"] != float64(52) {
		t.Fatalf("older page version_count = %v, want truthful total 52", envelope2["version_count"])
	}
	if _, ok := envelope2["history_truncated"]; ok {
		t.Fatalf("older page must not claim truncation when it reaches the first version")
	}
	if _, ok := envelope2["history_from_version"]; ok {
		t.Fatalf("older page must not emit history_from_version when nothing remains")
	}
	if _, ok := envelope2["history_cursor"]; ok {
		t.Fatalf("older page must not emit history_cursor when nothing remains")
	}
	history2, ok := envelope2["history"].([]any)
	if !ok {
		t.Fatalf("older page history is not an array: %T", envelope2["history"])
	}
	if len(history2) != 2 {
		t.Fatalf("older page history length = %d, want 2 (versions 1-2)", len(history2))
	}
	for i, want := range []struct {
		version int
		content string
	}{{1, "state-00"}, {2, "state-01"}} {
		entry, ok := history2[i].(map[string]any)
		if !ok {
			t.Fatalf("older page history[%d] is not an object: %T", i, history2[i])
		}
		if entry["version"] != float64(want.version) {
			t.Fatalf("older page history[%d].version = %v, want %d", i, entry["version"], want.version)
		}
		if entry["content"] != want.content {
			t.Fatalf("older page history[%d].content = %v, want %q", i, entry["content"], want.content)
		}
	}
	text2, ok := envelope2["result"].(string)
	if !ok {
		t.Fatalf("older page result is not a string: %T", envelope2["result"])
	}
	if !strings.Contains(text2, "History (2 previous versions):") {
		t.Fatalf("older page history header missing: %q", text2)
	}
	if strings.Contains(text2, "Version 3") || strings.Contains(text2, "state-02") {
		t.Fatalf("older page re-shipped versions from the first page: %q", text2)
	}
}

func TestGetObservationIncludeHistoryValidatesCursor(t *testing.T) {
	s := newMCPTestStore(t)
	id := seedIncludeHistoryObservation(t, s, 3)

	handler := handleGetObservation(s, MCPConfig{})
	for _, bad := range []any{float64(-1), float64(1.5), "not-a-number"} {
		res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id":              float64(id),
			"include_history": true,
			"history_cursor":  bad,
		}}})
		if err != nil {
			t.Fatalf("bad cursor %v: handler error: %v", bad, err)
		}
		if !res.IsError {
			t.Fatalf("bad cursor %v: expected tool error", bad)
		}
		if text := callResultText(t, res); !strings.Contains(text, "history_cursor must be a non-negative integer") {
			t.Fatalf("bad cursor %v: error text = %q", bad, text)
		}
	}

	// A cursor at or below the first version number is not an error: it yields
	// an empty older page while the truthful total stays visible.
	res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":              float64(id),
		"include_history": true,
		"history_cursor":  float64(1),
	}}})
	if err != nil {
		t.Fatalf("exhausted cursor handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("exhausted cursor unexpected error: %s", callResultText(t, res))
	}
	envelope := callResultJSON(t, res)
	if envelope["version_count"] != float64(3) {
		t.Fatalf("exhausted cursor version_count = %v, want 3", envelope["version_count"])
	}
	history, ok := envelope["history"].([]any)
	if !ok || len(history) != 0 {
		t.Fatalf("exhausted cursor history = %v, want empty array", envelope["history"])
	}
	if _, ok := envelope["history_truncated"]; ok {
		t.Fatalf("exhausted cursor must not emit truncation fields")
	}
}

func TestGetObservationIncludeHistoryFailureIsDeterministic(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s1",
		Type:      "note",
		Title:     "Touched once",
		Content:   "No evolution yet.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Break only the version-history read so the observation lookup still
	// succeeds and the include_history error path is the one exercised.
	if _, err := s.DB().Exec(`DROP TABLE observation_versions`); err != nil {
		t.Fatalf("drop version table: %v", err)
	}

	handler := handleGetObservation(s, MCPConfig{})
	res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":              float64(id),
		"include_history": true,
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected include_history to surface a tool error when the history read fails")
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "Failed to read observation history") {
		t.Fatalf("error text = %q, want history failure prefix", text)
	}
}
