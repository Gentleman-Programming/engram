package tui

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// ─── OSC 52 sequence generation ──────────────────────────────────────────────

func TestOSC52SequenceForContent(t *testing.T) {
	content := "hello world"
	seq := osc52Sequence(content)

	wantPrefix := "\x1b]52;c;"
	wantSuffix := "\x07"
	wantB64 := base64.StdEncoding.EncodeToString([]byte(content))

	if !strings.HasPrefix(seq, wantPrefix) {
		t.Fatalf("sequence does not start with OSC 52 prefix: %q", seq)
	}
	if !strings.HasSuffix(seq, wantSuffix) {
		t.Fatalf("sequence does not end with BEL: %q", seq)
	}
	middle := strings.TrimPrefix(strings.TrimSuffix(seq, wantSuffix), wantPrefix)
	if middle != wantB64 {
		t.Fatalf("base64 payload = %q, want %q", middle, wantB64)
	}
}

func TestOSC52SequenceEmptyContent(t *testing.T) {
	seq := osc52Sequence("")
	wantB64 := base64.StdEncoding.EncodeToString([]byte(""))
	if !strings.Contains(seq, wantB64) {
		t.Fatalf("empty content sequence should still contain valid base64: %q", seq)
	}
}

func TestOSC52SequenceUnicodeContent(t *testing.T) {
	content := "Decisión de arquitectura 🐘"
	seq := osc52Sequence(content)
	wantB64 := base64.StdEncoding.EncodeToString([]byte(content))
	if !strings.Contains(seq, wantB64) {
		t.Fatalf("unicode content not properly encoded: %q", seq)
	}
}

// ─── copyToClipboard command ─────────────────────────────────────────────────

func TestCopyToClipboardReturnsClipboardCopiedMsg(t *testing.T) {
	cmd := copyToClipboard("test content")
	if cmd == nil {
		t.Fatal("copyToClipboard should return a non-nil command")
	}
	msg := cmd()
	_, ok := msg.(clipboardCopiedMsg)
	if !ok {
		t.Fatalf("command returned %T, want clipboardCopiedMsg", msg)
	}
}

func TestCopyToClipboardMsgContainsSequence(t *testing.T) {
	content := "observation content"
	cmd := copyToClipboard(content)
	msg := cmd()
	cm, ok := msg.(clipboardCopiedMsg)
	if !ok {
		t.Fatalf("message type = %T", msg)
	}
	wantB64 := base64.StdEncoding.EncodeToString([]byte(content))
	if !strings.Contains(cm.sequence, wantB64) {
		t.Fatalf("clipboardCopiedMsg.sequence does not contain encoded content: %q", cm.sequence)
	}
}

// ─── clipboardClearMsg timer ─────────────────────────────────────────────────

func TestClearFeedbackAfterReturnsCmd(t *testing.T) {
	cmd := clearFeedbackAfter(1 * time.Millisecond)
	if cmd == nil {
		t.Fatal("clearFeedbackAfter should return a non-nil command")
	}
	msg := cmd()
	_, ok := msg.(clipboardClearMsg)
	if !ok {
		t.Fatalf("message type = %T, want clipboardClearMsg", msg)
	}
}

// ─── Update: clipboardCopiedMsg sets CopyFeedback ────────────────────────────

func TestUpdateClipboardCopiedMsgSetsFeedback(t *testing.T) {
	m := New(nil, "")
	m.CopyFeedback = ""

	updatedModel, cmd := m.Update(clipboardCopiedMsg{sequence: "\x1b]52;c;aGVsbG8=\x07"})
	updated := updatedModel.(Model)

	if updated.CopyFeedback != "✓ Copied!" {
		t.Fatalf("CopyFeedback = %q, want %q", updated.CopyFeedback, "✓ Copied!")
	}
	if cmd == nil {
		t.Fatal("clipboardCopiedMsg should return a clear-feedback command")
	}
}

// ─── Update: clipboardClearMsg clears CopyFeedback ───────────────────────────

func TestUpdateClipboardClearMsgClearsFeedback(t *testing.T) {
	m := New(nil, "")
	m.CopyFeedback = "✓ Copied!"

	updatedModel, cmd := m.Update(clipboardClearMsg{})
	updated := updatedModel.(Model)

	if updated.CopyFeedback != "" {
		t.Fatalf("CopyFeedback = %q, want empty string after clear", updated.CopyFeedback)
	}
	if cmd != nil {
		t.Fatal("clipboardClearMsg should not return a command")
	}
}

// ─── 'c' key on ScreenRecent copies selected observation ─────────────────────

func TestRecentScreenCKeyCopiesToClipboard(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenRecent
	m.RecentObservations = []store.Observation{
		{ID: 1, Content: "first observation content"},
		{ID: 2, Content: "second observation content"},
	}
	m.Cursor = 1

	updatedModel, cmd := m.handleRecentKeys("c")
	updated := updatedModel.(Model)
	_ = updated

	if cmd == nil {
		t.Fatal("'c' on recent screen should return a clipboard command")
	}
	msg := cmd()
	cm, ok := msg.(clipboardCopiedMsg)
	if !ok {
		t.Fatalf("command returned %T, want clipboardCopiedMsg", msg)
	}
	wantB64 := base64.StdEncoding.EncodeToString([]byte("second observation content"))
	if !strings.Contains(cm.sequence, wantB64) {
		t.Fatalf("sequence does not contain expected content encoding")
	}
}

func TestRecentScreenCKeyWithNoObservationsIsNoop(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenRecent
	m.RecentObservations = nil

	_, cmd := m.handleRecentKeys("c")
	if cmd != nil {
		t.Fatal("'c' with no observations should not return command")
	}
}

// ─── 'c' key on ScreenSearchResults copies selected result ───────────────────

func TestSearchResultsScreenCKeyCopiesToClipboard(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenSearchResults
	m.SearchResults = []store.SearchResult{
		{Observation: store.Observation{ID: 1, Content: "search result one"}},
		{Observation: store.Observation{ID: 2, Content: "search result two"}},
	}
	m.Cursor = 0

	_, cmd := m.handleSearchResultsKeys("c")
	if cmd == nil {
		t.Fatal("'c' on search results screen should return a clipboard command")
	}
	msg := cmd()
	cm, ok := msg.(clipboardCopiedMsg)
	if !ok {
		t.Fatalf("command returned %T, want clipboardCopiedMsg", msg)
	}
	wantB64 := base64.StdEncoding.EncodeToString([]byte("search result one"))
	if !strings.Contains(cm.sequence, wantB64) {
		t.Fatalf("sequence does not contain expected content encoding")
	}
}

func TestSearchResultsScreenCKeyWithNoResultsIsNoop(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenSearchResults
	m.SearchResults = nil

	_, cmd := m.handleSearchResultsKeys("c")
	if cmd != nil {
		t.Fatal("'c' with no search results should not return command")
	}
}

// ─── 'c' key on ScreenObservationDetail copies full content ──────────────────

func TestObservationDetailScreenCKeyCopiesToClipboard(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenObservationDetail
	m.SelectedObservation = &store.Observation{
		ID:      42,
		Content: "full observation content for copy",
	}

	_, cmd := m.handleObservationDetailKeys("c")
	if cmd == nil {
		t.Fatal("'c' on observation detail screen should return a clipboard command")
	}
	msg := cmd()
	cm, ok := msg.(clipboardCopiedMsg)
	if !ok {
		t.Fatalf("command returned %T, want clipboardCopiedMsg", msg)
	}
	wantB64 := base64.StdEncoding.EncodeToString([]byte("full observation content for copy"))
	if !strings.Contains(cm.sequence, wantB64) {
		t.Fatalf("sequence does not contain expected content encoding")
	}
}

func TestObservationDetailScreenCKeyWithNoObservationIsNoop(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenObservationDetail
	m.SelectedObservation = nil

	_, cmd := m.handleObservationDetailKeys("c")
	if cmd != nil {
		t.Fatal("'c' with nil observation should not return command")
	}
}

// ─── 'c' key on ScreenSessionDetail copies selected observation ───────────────

func TestSessionDetailScreenCKeyCopiesToClipboard(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenSessionDetail
	m.SessionObservations = []store.Observation{
		{ID: 10, Content: "session obs one"},
		{ID: 11, Content: "session obs two"},
	}
	m.Cursor = 1

	_, cmd := m.handleSessionDetailKeys("c")
	if cmd == nil {
		t.Fatal("'c' on session detail screen should return a clipboard command")
	}
	msg := cmd()
	cm, ok := msg.(clipboardCopiedMsg)
	if !ok {
		t.Fatalf("command returned %T, want clipboardCopiedMsg", msg)
	}
	wantB64 := base64.StdEncoding.EncodeToString([]byte("session obs two"))
	if !strings.Contains(cm.sequence, wantB64) {
		t.Fatalf("sequence does not contain expected content encoding")
	}
}

func TestSessionDetailScreenCKeyWithNoObservationsIsNoop(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenSessionDetail
	m.SessionObservations = nil

	_, cmd := m.handleSessionDetailKeys("c")
	if cmd != nil {
		t.Fatal("'c' with no session observations should not return command")
	}
}

// ─── View: CopyFeedback appears in rendered output ───────────────────────────

func TestViewShowsCopyFeedback(t *testing.T) {
	m := New(nil, "")
	m.Width = 80
	m.Height = 24
	m.Screen = ScreenObservationDetail
	m.SelectedObservation = &store.Observation{
		ID:      1,
		Type:    "decision",
		Title:   "Test",
		Content: "content",
	}
	m.CopyFeedback = "✓ Copied!"

	view := m.View()
	if !strings.Contains(view, "✓ Copied!") {
		t.Fatal("view should display CopyFeedback when set")
	}
}

func TestViewDoesNotShowCopyFeedbackWhenEmpty(t *testing.T) {
	m := New(nil, "")
	m.Width = 80
	m.Height = 24
	m.Screen = ScreenObservationDetail
	m.SelectedObservation = &store.Observation{
		ID:      1,
		Type:    "decision",
		Title:   "Test",
		Content: "content",
	}
	m.CopyFeedback = ""

	view := m.View()
	if strings.Contains(view, "✓ Copied!") {
		t.Fatal("view should not show copy feedback when CopyFeedback is empty")
	}
}

// ─── View: help text includes 'c copy' on relevant screens ───────────────────

func TestViewRecentHelpTextIncludesCopy(t *testing.T) {
	m := New(nil, "")
	m.Width = 80
	m.Height = 24
	m.Screen = ScreenRecent
	m.RecentObservations = []store.Observation{{ID: 1, Type: "decision", Title: "t", Content: "c"}}

	view := m.viewRecent()
	if !strings.Contains(view, "c copy") {
		t.Fatalf("recent screen help text should include 'c copy', got: %q", view[strings.LastIndex(view, "\n")-100:])
	}
}

func TestViewSearchResultsHelpTextIncludesCopy(t *testing.T) {
	m := New(nil, "")
	m.Width = 80
	m.Height = 24
	m.Screen = ScreenSearchResults
	m.SearchResults = []store.SearchResult{{Observation: store.Observation{ID: 1}}}

	view := m.viewSearchResults()
	if !strings.Contains(view, "c copy") {
		t.Fatalf("search results help text should include 'c copy'")
	}
}

func TestViewObservationDetailHelpTextIncludesCopy(t *testing.T) {
	m := New(nil, "")
	m.Width = 80
	m.Height = 24
	m.Screen = ScreenObservationDetail
	m.SelectedObservation = &store.Observation{
		ID: 1, Type: "decision", Title: "t", Content: "content",
	}

	view := m.viewObservationDetail()
	if !strings.Contains(view, "c copy") {
		t.Fatalf("observation detail help text should include 'c copy'")
	}
}

func TestViewSessionDetailHelpTextIncludesCopy(t *testing.T) {
	m := New(nil, "")
	m.Width = 80
	m.Height = 24
	m.Screen = ScreenSessionDetail
	m.Sessions = []store.SessionSummary{{ID: "s1", Project: "engram"}}
	m.SelectedSessionIdx = 0
	m.SessionObservations = []store.Observation{{ID: 1, Type: "decision", Title: "t", Content: "c"}}

	view := m.viewSessionDetail()
	if !strings.Contains(view, "c copy") {
		t.Fatalf("session detail help text should include 'c copy'")
	}
}
