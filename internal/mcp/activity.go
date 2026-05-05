package mcp

import (
	"fmt"
	"sync"
	"time"
)

// SessionActivity tracks tool call activity for save reminders and activity scores.
type SessionActivity struct {
	mu         sync.Mutex
	sessions   map[string]*sessionState
	nudgeAfter time.Duration
	now        func() time.Time // injectable for testing
}

type sessionState struct {
	lastSaveAt    time.Time
	toolCallCount int
	saveCount     int
	startedAt     time.Time
	currentPrompt *promptContext
}

type promptContext struct {
	project string
	content string
}

// NewSessionActivity creates a new activity tracker with the given nudge threshold.
func NewSessionActivity(nudgeAfter time.Duration) *SessionActivity {
	return &SessionActivity{
		sessions:   make(map[string]*sessionState),
		nudgeAfter: nudgeAfter,
		now:        time.Now,
	}
}

func (a *SessionActivity) getOrCreate(sessionID string) *sessionState {
	s, ok := a.sessions[sessionID]
	if !ok {
		s = &sessionState{startedAt: a.now()}
		a.sessions[sessionID] = s
	}
	return s
}

// RecordToolCall increments the tool call counter for a session.
func (a *SessionActivity) RecordToolCall(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.getOrCreate(sessionID)
	s.toolCallCount++
}

// ClearSession removes the session entry, freeing memory.
func (a *SessionActivity) ClearSession(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sessionID)
}

// RecordSave increments the save counter and updates lastSaveAt.
func (a *SessionActivity) RecordSave(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.getOrCreate(sessionID)
	s.saveCount++
	s.lastSaveAt = a.now()
}

// RecordPrompt stores the latest user prompt observed for a session. MCP does
// not currently receive user prompts on every tool call, so callers must feed
// this explicitly when prompt text is available.
func (a *SessionActivity) RecordPrompt(sessionID, project, content string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.getOrCreate(sessionID)
	s.currentPrompt = &promptContext{project: project, content: content}
}

// CurrentPrompt returns the latest prompt for the session when it belongs to the
// same project as the save operation.
func (a *SessionActivity) CurrentPrompt(sessionID, project string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[sessionID]
	if !ok || s.currentPrompt == nil {
		return "", false
	}
	if s.currentPrompt.project != project || s.currentPrompt.content == "" {
		return "", false
	}
	return s.currentPrompt.content, true
}

// NudgeIfNeeded returns a reminder string if too much time has passed since
// the last save in this session. Returns empty string if no nudge needed.
func (a *SessionActivity) NudgeIfNeeded(sessionID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()

	s, ok := a.sessions[sessionID]
	if !ok {
		return ""
	}

	now := a.now()

	// Don't nudge if session is too young
	if now.Sub(s.startedAt) < a.nudgeAfter {
		return ""
	}

	// Don't nudge idle/new sessions (no saves and few tool calls)
	if s.saveCount == 0 && s.toolCallCount <= 5 {
		return ""
	}

	// Check time since last save (or session start if no saves yet)
	ref := s.lastSaveAt
	if ref.IsZero() {
		ref = s.startedAt
	}

	elapsed := now.Sub(ref)
	if elapsed < a.nudgeAfter {
		return ""
	}

	minutes := int(elapsed.Minutes())
	return fmt.Sprintf("\n\n⚠️ No mem_save calls for this project in %d minutes. Did you make any decisions, fix bugs, or discover something worth persisting?", minutes)
}

// ActivityScore returns a formatted activity score string for the session.
func (a *SessionActivity) ActivityScore(sessionID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()

	s, ok := a.sessions[sessionID]
	if !ok {
		return ""
	}

	callLabel := "tool calls"
	if s.toolCallCount == 1 {
		callLabel = "tool call"
	}
	saveLabel := "saves"
	if s.saveCount == 1 {
		saveLabel = "save"
	}
	score := fmt.Sprintf("Session activity: %d %s, %d %s", s.toolCallCount, callLabel, s.saveCount, saveLabel)
	if s.saveCount == 0 && s.toolCallCount > 5 {
		score += " — high activity with no saves, consider persisting important decisions"
	}
	return score
}
