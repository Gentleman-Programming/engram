package mcp

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionActivity_RecordAndNudge(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	a := NewSessionActivity(10 * time.Minute)
	a.now = func() time.Time { return now }

	sid := "test-session"

	// Record some tool calls
	for i := 0; i < 6; i++ {
		a.RecordToolCall(sid)
	}

	// Session just started, no nudge expected even with > 5 tool calls
	nudge := a.NudgeIfNeeded(sid)
	if nudge != "" {
		t.Fatalf("expected no nudge for new session, got: %q", nudge)
	}

	// Advance time past threshold
	now = now.Add(15 * time.Minute)

	nudge = a.NudgeIfNeeded(sid)
	if nudge == "" {
		t.Fatal("expected nudge after threshold passed")
	}
	if !strings.Contains(nudge, "15 minutes") {
		t.Fatalf("expected nudge to mention 15 minutes, got: %q", nudge)
	}
	if !strings.Contains(nudge, "No mem_save calls for this project") {
		t.Fatalf("expected nudge text about mem_save, got: %q", nudge)
	}
}

func TestSessionActivity_RecordSave_ResetsNudge(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	a := NewSessionActivity(10 * time.Minute)
	a.now = func() time.Time { return now }

	sid := "test-session"

	// Record tool calls and advance past threshold
	for i := 0; i < 6; i++ {
		a.RecordToolCall(sid)
	}
	now = now.Add(15 * time.Minute)

	// Verify nudge fires
	nudge := a.NudgeIfNeeded(sid)
	if nudge == "" {
		t.Fatal("expected nudge before save")
	}

	// Now save, which should reset the timer
	a.RecordSave(sid)

	// Nudge should be gone since save just happened
	nudge = a.NudgeIfNeeded(sid)
	if nudge != "" {
		t.Fatalf("expected no nudge right after save, got: %q", nudge)
	}

	// Advance time again past threshold
	now = now.Add(12 * time.Minute)
	nudge = a.NudgeIfNeeded(sid)
	if nudge == "" {
		t.Fatal("expected nudge again after threshold passed since last save")
	}
	if !strings.Contains(nudge, "12 minutes") {
		t.Fatalf("expected 12 minutes in nudge, got: %q", nudge)
	}
}

func TestSessionActivity_ProjectFreshness(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	a := NewSessionActivity(10 * time.Minute)
	a.now = func() time.Time { return now }

	for _, project := range []string{"project-a", "project-b"} {
		for i := 0; i < 6; i++ {
			a.RecordToolCall(defaultSessionID(project))
		}
	}
	now = now.Add(15 * time.Minute)

	a.RecordProjectSave("project-a")
	if got := a.NudgeIfNeededForProject(defaultSessionID("project-a"), "project-a"); got != "" {
		t.Fatalf("expected fresh project save to suppress nudge, got %q", got)
	}
	if got := a.NudgeIfNeededForProject(defaultSessionID("project-b"), "project-b"); got == "" {
		t.Fatal("expected project freshness to remain isolated")
	}

	now = now.Add(10 * time.Minute)
	if got := a.NudgeIfNeededForProject(defaultSessionID("project-a"), "project-a"); got == "" {
		t.Fatal("expected nudge after project freshness expires")
	}
	if _, ok := a.projectLastSaveAt["project-a"]; ok {
		t.Fatal("expected expired project freshness to be removed")
	}
}

func TestSessionActivity_ProjectFreshnessNormalizesProjectNames(t *testing.T) {
	const (
		canonicalProject = "project-a"
		explicitSession  = "explicit-session"
	)

	tests := []struct {
		name        string
		record      func(*SessionActivity)
		wantSession bool
	}{
		{
			name: "session and project save",
			record: func(a *SessionActivity) {
				a.RecordSaveForProject(explicitSession, "  project-a\t")
			},
			wantSession: true,
		},
		{
			name: "project-only save",
			record: func(a *SessionActivity) {
				a.RecordProjectSave("\nproject-a  ")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
			a := NewSessionActivity(10 * time.Minute)
			a.now = func() time.Time { return now }
			defaultSession := defaultSessionID(canonicalProject)
			for i := 0; i < 6; i++ {
				a.RecordToolCall(defaultSession)
			}
			now = now.Add(15 * time.Minute)

			tt.record(a)

			if len(a.projectLastSaveAt) != 1 {
				t.Fatalf("expected one canonical freshness entry, got %v", a.projectLastSaveAt)
			}
			if _, ok := a.projectLastSaveAt[canonicalProject]; !ok {
				t.Fatalf("expected canonical project key, got %v", a.projectLastSaveAt)
			}
			if got := a.NudgeIfNeededForProject(defaultSession, "\tproject-a\n"); got != "" {
				t.Fatalf("expected normalized project lookup to suppress nudge, got %q", got)
			}

			score := a.ActivityScore(explicitSession)
			if tt.wantSession && !strings.Contains(score, "1 save") {
				t.Fatalf("expected explicit session save to be recorded, got %q", score)
			}
			if !tt.wantSession && score != "" {
				t.Fatalf("expected project-only save not to create session activity, got %q", score)
			}
		})
	}
}

func TestSessionActivity_EmptyProjectFreshnessDoesNotSuppressNudge(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	a := NewSessionActivity(10 * time.Minute)
	a.now = func() time.Time { return now }
	sessionID := defaultSessionID("")
	for i := 0; i < 6; i++ {
		a.RecordToolCall(sessionID)
	}
	now = now.Add(15 * time.Minute)

	a.RecordProjectSave("")
	if len(a.projectLastSaveAt) != 0 {
		t.Fatalf("expected no freshness entry for an empty project, got %v", a.projectLastSaveAt)
	}
	if got := a.NudgeIfNeededForProject(sessionID, ""); got == "" {
		t.Fatal("expected empty project save not to suppress nudge")
	}
}

func TestSessionActivity_ActivityScore(t *testing.T) {
	a := NewSessionActivity(10 * time.Minute)
	sid := "test-session"

	// No session yet
	score := a.ActivityScore(sid)
	if score != "" {
		t.Fatalf("expected empty score for unknown session, got: %q", score)
	}

	// Record some activity
	for i := 0; i < 8; i++ {
		a.RecordToolCall(sid)
	}

	score = a.ActivityScore(sid)
	if !strings.Contains(score, "8 tool calls") {
		t.Fatalf("expected 8 tool calls in score, got: %q", score)
	}
	if !strings.Contains(score, "0 saves") {
		t.Fatalf("expected 0 saves in score, got: %q", score)
	}
	if !strings.Contains(score, "high activity with no saves") {
		t.Fatalf("expected high activity warning, got: %q", score)
	}

	// After a save, warning should disappear
	a.RecordSave(sid)
	score = a.ActivityScore(sid)
	if !strings.Contains(score, "1 save") {
		t.Fatalf("expected '1 save' in score, got: %q", score)
	}
	if strings.Contains(score, "1 saves") {
		t.Fatalf("expected singular 'save' not 'saves', got: %q", score)
	}
	if strings.Contains(score, "high activity") {
		t.Fatalf("expected no high activity warning after save, got: %q", score)
	}
}

func TestSessionActivity_NoNudgeForIdleSessions(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	a := NewSessionActivity(10 * time.Minute)
	a.now = func() time.Time { return now }

	sid := "idle-session"

	// Only 3 tool calls, no saves
	for i := 0; i < 3; i++ {
		a.RecordToolCall(sid)
	}

	// Advance past threshold
	now = now.Add(20 * time.Minute)

	// Should NOT nudge because toolCallCount <= 5 and saveCount == 0
	nudge := a.NudgeIfNeeded(sid)
	if nudge != "" {
		t.Fatalf("expected no nudge for idle session with few tool calls, got: %q", nudge)
	}
}

func TestSessionActivity_ClearSession(t *testing.T) {
	a := NewSessionActivity(10 * time.Minute)
	sid := "clear-test"

	a.RecordToolCall(sid)
	a.RecordSave(sid)

	// Verify session exists
	score := a.ActivityScore(sid)
	if score == "" {
		t.Fatal("expected activity score before clear")
	}

	// Clear and verify it's gone
	a.ClearSession(sid)
	score = a.ActivityScore(sid)
	if score != "" {
		t.Fatalf("expected empty score after clear, got: %q", score)
	}

	// Clearing a non-existent session should not panic
	a.ClearSession("non-existent")
}

func TestSessionActivity_Pluralization(t *testing.T) {
	a := NewSessionActivity(10 * time.Minute)
	sid := "plural-test"

	a.RecordToolCall(sid)
	a.RecordSave(sid)

	score := a.ActivityScore(sid)
	if !strings.Contains(score, "1 tool call,") {
		t.Fatalf("expected singular 'tool call', got: %q", score)
	}
	if !strings.Contains(score, "1 save") {
		t.Fatalf("expected singular 'save', got: %q", score)
	}

	a.RecordToolCall(sid)
	a.RecordSave(sid)

	score = a.ActivityScore(sid)
	if !strings.Contains(score, "2 tool calls,") {
		t.Fatalf("expected plural 'tool calls', got: %q", score)
	}
	if !strings.Contains(score, "2 saves") {
		t.Fatalf("expected plural 'saves', got: %q", score)
	}
}

func TestSessionActivity_ConcurrentAccess(t *testing.T) {
	a := NewSessionActivity(10 * time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			a.RecordToolCall("concurrent-session")
		}()
		go func() {
			defer wg.Done()
			a.RecordSave("concurrent-session")
		}()
		go func() {
			defer wg.Done()
			_ = a.NudgeIfNeeded("concurrent-session")
			_ = a.ActivityScore("concurrent-session")
		}()
	}
	wg.Wait()

	// Verify state is consistent
	a.mu.Lock()
	s := a.sessions["concurrent-session"]
	a.mu.Unlock()

	if s.toolCallCount != 100 {
		t.Fatalf("expected 100 tool calls, got %d", s.toolCallCount)
	}
	if s.saveCount != 100 {
		t.Fatalf("expected 100 saves, got %d", s.saveCount)
	}
}
