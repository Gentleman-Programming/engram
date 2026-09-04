// Package tasks derives engram-projects task lifecycle state from the
// literal Jira status strings mirrored by mem_task_upsert (RFC
// rfc-engram-projects.md §5.3).
package tasks

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed jira_state_map.json
var jiraStateMapJSON []byte

// jiraStateMap maps a literal Jira status.name to the corresponding
// engram-projects task state. Loaded once from jira_state_map.json so the
// mapping table lives as data, not code, and can be extended without
// touching Go source.
var jiraStateMap = func() map[string]string {
	var m map[string]string
	if err := json.Unmarshal(jiraStateMapJSON, &m); err != nil {
		// The embedded file is part of the build; a decode failure here is a
		// programming error, not a runtime condition callers can recover from.
		panic("internal/tasks: invalid jira_state_map.json: " + err.Error())
	}
	return m
}()

// Valid task states, in the order the engram-projects schema CHECK
// constraint accepts them.
const (
	StateOpen       = "open"
	StateAnalysis   = "analysis"
	StateInProgress = "in_progress"
	StateReview     = "review"
	StateVerified   = "verified"
	StateDone       = "done"
	StateBlocked    = "blocked"
	StateCancelled  = "cancelled"
)

// ActiveStates lists every state considered "active" by mem_task_list's
// default filter (every state except done and cancelled).
var ActiveStates = []string{
	StateOpen, StateAnalysis, StateInProgress, StateReview, StateVerified, StateBlocked,
}

// ClosedStates lists the states that require closed_at to be set.
var ClosedStates = map[string]bool{
	StateDone:      true,
	StateCancelled: true,
}

// DeriveState resolves the task state for an incoming jira_status. It first
// looks up the literal status string in jira_state_map.json; when the status
// is unrecognized, it falls back to jiraStatusCategory (new -> open,
// indeterminate -> in_progress, done -> done). ok is false when neither the
// literal status nor the category could be mapped.
func DeriveState(jiraStatus, jiraStatusCategory string) (state string, ok bool) {
	if state, found := jiraStateMap[jiraStatus]; found {
		return state, true
	}
	switch strings.TrimSpace(jiraStatusCategory) {
	case "new":
		return StateOpen, true
	case "indeterminate":
		return StateInProgress, true
	case "done":
		return StateDone, true
	default:
		return "", false
	}
}
