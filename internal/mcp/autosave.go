// Package mcp — auto-save hook implementations.
//
// Auto-save hooks reduce the burden on agents to explicitly call mem_save or
// mem_session_summary. When enabled, Engram automatically persists a
// consolidation observation on session end, and can optionally scan tool
// results for extractable learnings after every tool call.
//
// # Configuration
//
// Auto-save is controlled by AutoSaveConfig inside MCPConfig:
//
//	cfg := MCPConfig{
//	    AutoSave: AutoSaveConfig{
//	        Enabled:  true,
//	        Triggers: []string{"session_end"},  // default when Enabled=true
//	    },
//	}
//
// Valid trigger values:
//
//	"session_end"    — consolidate and save on mem_session_end (default)
//	"post_tool_use"  — scan every tool result for "## Key Learnings:" sections
//
// # Tagging
//
// Auto-saved observations carry ToolName="auto" so they are distinguishable
// from explicit agent saves:
//
//	obs.ToolName == "auto"
//
// # Deduplication
//
// Session-end saves use a topic_key ("auto-save/session-end/<sessionID>") so
// that calling mem_session_end twice for the same session upserts rather than
// creates a duplicate. Post-tool-use saves delegate to PassiveCapture which
// uses the normalized_hash column for content dedup.
package mcp

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// AutoSaveConfig controls automatic memory persistence on session lifecycle
// events and tool completions.
type AutoSaveConfig struct {
	// Enabled turns auto-save on. Default: false.
	Enabled bool `json:"enabled"`

	// Triggers lists the events that trigger an auto-save.
	// Valid values: "session_end", "post_tool_use".
	// When Enabled is true and Triggers is empty, "session_end" is assumed.
	Triggers []string `json:"triggers,omitempty"`
}

const (
	// autoSaveSource is the ToolName tag written on auto-saved observations.
	autoSaveSource = "auto"

	// Trigger name constants.
	autoSaveTriggerSessionEnd  = "session_end"
	autoSaveTriggerPostToolUse = "post_tool_use"

	// autoSaveTopicKeyPrefix is prepended to the session ID to form the
	// topic_key used for session-end auto-saves.
	autoSaveTopicKeyPrefix = "auto-save/session-end/"

	// autoSaveMaxObservations caps how many session observations are included
	// in a session-end consolidation summary.
	autoSaveMaxObservations = 50
)

// hasTrigger reports whether cfg has the given trigger active.
// When Enabled is true and Triggers is empty, only "session_end" is implied.
func hasTrigger(cfg AutoSaveConfig, trigger string) bool {
	if !cfg.Enabled {
		return false
	}
	if len(cfg.Triggers) == 0 {
		return trigger == autoSaveTriggerSessionEnd
	}
	for _, t := range cfg.Triggers {
		if t == trigger {
			return true
		}
	}
	return false
}

// performSessionEndAutoSave saves a brief consolidation observation when a
// session ends. It collects the session's observations, groups them by type,
// and persists a structured summary tagged with source="auto".
//
// The save is idempotent: it uses a topic_key derived from the session ID so
// that a second call (e.g. if the hook fires twice) upserts the existing row
// rather than creating a duplicate.
//
// Returns nil without saving when the session has no observations.
func performSessionEndAutoSave(s *store.Store, sessionID, project string) error {
	observations, err := s.SessionObservations(sessionID, autoSaveMaxObservations)
	if err != nil {
		return fmt.Errorf("auto-save session-end: list observations: %w", err)
	}
	if len(observations) == 0 {
		return nil
	}

	content := buildAutoSaveContent(sessionID, observations)
	topicKey := autoSaveTopicKeyPrefix + sessionID
	// len(observations) may be capped at autoSaveMaxObservations; the title
	// reflects the fetched count, not necessarily the total session count.
	title := fmt.Sprintf("Auto-save: session %s (%d observations)", sessionID, len(observations))

	_, err = s.AddObservation(store.AddObservationParams{
		SessionID: sessionID,
		Type:      "session_summary",
		Title:     title,
		Content:   content,
		Project:   project,
		Scope:     "project",
		TopicKey:  topicKey,
		ToolName:  autoSaveSource,
	})
	if err != nil {
		return fmt.Errorf("auto-save session-end: save: %w", err)
	}
	return nil
}

// buildAutoSaveContent constructs the content for a session-end auto-save
// observation. It groups observations by type and lists their titles.
func buildAutoSaveContent(sessionID string, observations []store.Observation) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Auto-consolidated session summary\n\n")
	fmt.Fprintf(&b, "Session: %s\n", sessionID)
	fmt.Fprintf(&b, "Observations captured: %d\n\n", len(observations))

	// Group by type and collect unique type names for deterministic output.
	byType := make(map[string][]store.Observation)
	var types []string
	seen := make(map[string]bool)
	for _, obs := range observations {
		// Skip other auto-saves to avoid circular references.
		if obs.ToolName != nil && *obs.ToolName == autoSaveSource {
			continue
		}
		if !seen[obs.Type] {
			seen[obs.Type] = true
			types = append(types, obs.Type)
		}
		byType[obs.Type] = append(byType[obs.Type], obs)
	}

	// Sort types for deterministic output.
	sort.Strings(types)

	for _, typ := range types {
		obsList := byType[typ]
		fmt.Fprintf(&b, "### %s (%d)\n", typ, len(obsList))
		for _, obs := range obsList {
			fmt.Fprintf(&b, "- %s\n", obs.Title)
		}
		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String())
}

// wrapWithPostToolUseCapture returns a ToolHandlerFunc that invokes h and,
// after a successful (non-error) result, scans the response text for
// "## Key Learnings:" sections. Any extracted items are persisted via
// PassiveCapture (which deduplicates via normalized_hash).
//
// When the post_tool_use trigger is not enabled, h is returned unchanged.
// Auto-save failures are logged to stderr but never propagate to the caller —
// tool results are always returned intact.
func wrapWithPostToolUseCapture(h server.ToolHandlerFunc, s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	if !hasTrigger(cfg.AutoSave, autoSaveTriggerPostToolUse) {
		return h
	}

	return func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		result, err := h(ctx, req)
		if err != nil || result == nil || result.IsError {
			// Don't scan error results — they're unlikely to contain learnings
			// and we don't want to mask the original error.
			return result, err
		}

		// Collect text content from the result.
		var text strings.Builder
		for _, content := range result.Content {
			if tc, ok := mcppkg.AsTextContent(content); ok {
				text.WriteString(tc.Text)
				text.WriteString("\n")
			}
		}
		raw := text.String()
		if strings.TrimSpace(raw) == "" {
			return result, nil
		}

		// Only proceed when a Key Learnings section is present — avoids
		// unnecessary store queries for the common case.
		if len(store.ExtractLearnings(raw)) == 0 {
			return result, nil
		}

		// Resolve project for the capture. Tolerant: failures are non-fatal.
		detRes, detErr := resolveWriteProjectWithProcessOverride(cfg.DefaultProject)
		if detErr != nil {
			fmt.Fprintf(os.Stderr, "engram: auto-save post-tool-use: project resolution failed: %v\n", detErr)
			return result, nil
		}
		project, _ := store.NormalizeProject(detRes.Project)
		sessionID := resolveFallbackSessionID(s, project)

		captureResult, captureErr := s.PassiveCapture(store.PassiveCaptureParams{
			SessionID: sessionID,
			Content:   raw,
			Project:   project,
			Source:    autoSaveSource,
		})
		if captureErr != nil {
			fmt.Fprintf(os.Stderr, "engram: auto-save post-tool-use: passive capture failed: %v\n", captureErr)
		} else if captureResult != nil && captureResult.Saved > 0 {
			fmt.Fprintf(os.Stderr, "engram: auto-save: captured %d learnings from tool result\n", captureResult.Saved)
		}

		return result, nil
	}
}
