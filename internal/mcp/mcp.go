// Package mcp implements the Model Context Protocol server for Engram.
//
// This exposes memory tools via MCP stdio transport so ANY agent
// (OpenCode, Claude Code, Cursor, Windsurf, etc.) can use Engram's
// persistent memory just by adding it as an MCP server.
//
// Tool profiles allow agents to load only the tools they need:
//
//	engram mcp                    → all 18 tools (default)
//	engram mcp --tools=agent      → 14 tools agents actually use (per skill files)
//	engram mcp --tools=admin      → 4 tools for TUI/CLI (delete, stats, timeline, merge)
//	engram mcp --tools=agent,admin → combine profiles
//	engram mcp --tools=mem_save,mem_search → individual tool names
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/diagnostic"
	projectpkg "github.com/Gentleman-Programming/engram/internal/project"
	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// MCPConfig holds configuration for the MCP server.
// JW6: DefaultProject removed — it was populated but never read (dead code).
// Project is always auto-detected from cwd at call time via resolveWriteProject/resolveReadProject.
type MCPConfig struct {
	// BM25Floor overrides the default BM25 score floor used by FindCandidates
	// during conflict candidate detection (REQ-001). The floor is the minimum
	// acceptable BM25 rank (negative; closer to 0 = better match). Candidates
	// whose score falls below this threshold are excluded.
	//
	// nil means "use the store default" (-2.0). An explicit pointer value
	// (including 0.0) is forwarded directly. Using a pointer avoids the
	// zero-value ambiguity where 0.0 would otherwise be indistinguishable
	// from "not set".
	BM25Floor *float64

	// Limit overrides the maximum number of conflict candidates returned per
	// mem_save call (REQ-001). nil means "use the store default" (3).
	// An explicit pointer value (including 0) is forwarded directly.
	Limit *int
}

var suggestTopicKey = store.SuggestTopicKey

var loadMCPStats = func(s *store.Store) (*store.Stats, error) {
	return s.Stats()
}

func currentWorkingDirectory() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

func ensureImplicitSessionWithCWD(s *store.Store, sessionID, project string) error {
	return s.CreateSession(sessionID, project, currentWorkingDirectory())
}

// ─── Tool Profiles ───────────────────────────────────────────────────────────
//
// "agent" — tools AI agents use during coding sessions:
//   mem_save, mem_search, mem_context, mem_session_summary,
//   mem_session_start, mem_session_end, mem_get_observation,
//   mem_suggest_topic_key, mem_capture_passive, mem_save_prompt
//
// "admin" — tools for manual curation, TUI, and dashboards:
//   mem_update, mem_delete, mem_stats, mem_timeline, mem_merge_projects
//
// "all" (default) — every tool registered.

// ProfileAgent contains the tool names that AI agents need.
// Sourced from actual skill files and memory protocol instructions
// across all 4 supported agents (Claude Code, OpenCode, Gemini CLI, Codex).
var ProfileAgent = map[string]bool{
	"mem_save":              true, // proactive save — referenced 17 times across protocols
	"mem_search":            true, // search past memories — referenced 6 times
	"mem_context":           true, // recent context from previous sessions — referenced 10 times
	"mem_session_summary":   true, // end-of-session summary — referenced 16 times
	"mem_session_start":     true, // register session start
	"mem_session_end":       true, // mark session completed
	"mem_get_observation":   true, // full observation content after search — referenced 4 times
	"mem_suggest_topic_key": true, // stable topic key for upserts — referenced 3 times
	"mem_capture_passive":   true, // extract learnings from text — referenced in Gemini/Codex protocol
	"mem_save_prompt":       true, // save user prompts
	"mem_update":            true, // update observation by ID — skills say "use mem_update when you have an exact ID to correct"
	"mem_current_project":   true, // detect current project — recommended first call for agents (REQ-313)
	"mem_judge":             true, // record verdict on a pending memory conflict (REQ-003, Phase D)
	"mem_compare":           true, // persist an agent-judged semantic verdict via JudgeBySemantic (REQ-011, Phase G)
	"mem_doctor":            true, // read-only operational diagnostics for agents
}

// ProfileAdmin contains tools for TUI, dashboards, and manual curation
// that are NOT referenced in any agent skill or memory protocol.
var ProfileAdmin = map[string]bool{
	"mem_delete":         true, // only in OpenCode's ENGRAM_TOOLS filter, not in any agent instructions
	"mem_stats":          true, // only in OpenCode's ENGRAM_TOOLS filter, not in any agent instructions
	"mem_timeline":       true, // only in OpenCode's ENGRAM_TOOLS filter, not in any agent instructions
	"mem_merge_projects": true, // destructive curation tool — not for agent use
}

// Profiles maps profile names to their tool sets.
var Profiles = map[string]map[string]bool{
	"agent": ProfileAgent,
	"admin": ProfileAdmin,
}

// ResolveTools takes a comma-separated string of profile names and/or
// individual tool names and returns the set of tool names to register.
// An empty input means "all" — every tool is registered.
func ResolveTools(input string) map[string]bool {
	input = strings.TrimSpace(input)
	if input == "" || input == "all" {
		return nil // nil means register everything
	}

	result := make(map[string]bool)
	for _, token := range strings.Split(input, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if token == "all" {
			return nil
		}
		if profile, ok := Profiles[token]; ok {
			for tool := range profile {
				result[tool] = true
			}
		} else {
			// Treat as individual tool name
			result[token] = true
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// NewServer creates an MCP server with ALL tools registered (backwards compatible).
func NewServer(s *store.Store) *server.MCPServer {
	return NewServerWithConfig(s, MCPConfig{}, nil)
}

// serverInstructions tells MCP clients when to use Engram's tools.
// 6 core tools are eager (always in context). The rest are deferred
// and require ToolSearch to load.
const serverInstructions = `Engram provides persistent memory that survives across sessions and compactions.

CORE TOOLS (always available — use without ToolSearch):
  mem_save — save decisions, bugs, discoveries, conventions PROACTIVELY (do not wait to be asked)
  mem_search — find past work, decisions, or context from previous sessions
  mem_context — get recent session history (call at session start or after compaction)
  mem_session_summary — save end-of-session summary (MANDATORY before saying "done")
  mem_get_observation — get full untruncated content of a search result by ID
  mem_save_prompt — save user prompt for context
  mem_current_project — detect current project from cwd (recommended first call)

DEFERRED TOOLS (use ToolSearch when needed):
  mem_update, mem_suggest_topic_key, mem_session_start, mem_session_end,
  mem_stats, mem_delete, mem_timeline, mem_capture_passive, mem_merge_projects

PROACTIVE SAVE RULE: Call mem_save immediately after ANY decision, bug fix, discovery, or convention — not just when asked.

## CONFLICT SURFACING — when mem_save returns candidates

After every mem_save call, check the response envelope for judgment_required.

IF judgment_required IS TRUE:
  Iterate candidates[] and call mem_judge once per candidate using the
  judgment_id from that candidate entry. Do NOT use the top-level judgment_id
  for multiple candidates — each candidate has its own.

  HEURISTIC — when to ask the user vs. resolve autonomously:

  ASK the user (conversationally, as part of your next reply) when:
    - confidence is below 0.7, OR
    - the relation you would choose is supersedes or conflicts_with AND the
      observation type is architecture, policy, or decision

  RESOLVE silently (call mem_judge without asking) when:
    - confidence >= 0.7 AND the relation is not supersedes/conflicts_with, OR
    - the relation is related, compatible, scoped, or not_conflict

  HOW TO ASK (conversationally — never via blocking CLI or dashboard prompt):
    Raise it naturally in your next reply to the user. Example phrasing:
    "I noticed memory #abc123 might conflict with what we just saved.
     Want me to mark the new one as superseding it, or are they about
     different scopes? I can also mark them as compatible if both still apply."

  AFTER RESOLUTION (both paths):
    Call mem_judge with the chosen relation, a reason, and if the user gave
    explicit direction, include their words as the evidence field. This persists
    the verdict and closes the pending conflict row.`

// NewServerWithTools creates an MCP server registering only the tools in
// the allowlist. If allowlist is nil, all tools are registered.
func NewServerWithTools(s *store.Store, allowlist map[string]bool) *server.MCPServer {
	return NewServerWithConfig(s, MCPConfig{}, allowlist)
}

// NewServerWithConfig creates an MCP server with full configuration including
// default project detection and optional tool allowlist.
func NewServerWithConfig(s *store.Store, cfg MCPConfig, allowlist map[string]bool) *server.MCPServer {
	return newServerWithActivity(s, cfg, allowlist, NewSessionActivity(10*time.Minute))
}

func newServerWithActivity(s *store.Store, cfg MCPConfig, allowlist map[string]bool, activity *SessionActivity) *server.MCPServer {
	srv := server.NewMCPServer(
		"engram",
		"0.1.0",
		server.WithToolCapabilities(true),
		server.WithInstructions(serverInstructions),
	)

	registerTools(srv, s, cfg, allowlist, activity)
	return srv
}

// shouldRegister returns true if the tool should be registered given the
// allowlist. If allowlist is nil, everything is allowed.
func shouldRegister(name string, allowlist map[string]bool) bool {
	if allowlist == nil {
		return true
	}
	return allowlist[name]
}

func registerTools(srv *server.MCPServer, s *store.Store, cfg MCPConfig, allowlist map[string]bool, activity *SessionActivity) {
	writeQueue := newWriteQueue(defaultMCPWriteQueueSize)

	// ─── mem_search (profile: agent, core — always in context) ─────────
	if shouldRegister("mem_search", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_search",
				mcp.WithDescription("Search your persistent memory across all sessions. Use this to find past decisions, bugs fixed, patterns used, files changed, or any context from previous coding sessions."),
				mcp.WithTitleAnnotation("Search Memory"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("query",
					mcp.Required(),
					mcp.Description("Search query — natural language or keywords"),
				),
				mcp.WithString("type",
					mcp.Description("Filter by type: tool_use, file_change, command, file_read, search, manual, decision, architecture, bugfix, pattern"),
				),
				mcp.WithString("project",
					mcp.Description("Filter by project name"),
				),
				mcp.WithString("scope",
					mcp.Description("Filter by scope: project (default) or personal"),
				),
				mcp.WithNumber("limit",
					mcp.Description("Max results (default: 10, max: 20)"),
				),
			),
			handleSearch(s, cfg, activity),
		)
	}

	// ─── mem_save (profile: agent, core — always in context) ───────────
	if shouldRegister("mem_save", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_save",
				mcp.WithTitleAnnotation("Save Memory"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithDescription(`Save an important observation to persistent memory. Call this PROACTIVELY after completing significant work — don't wait to be asked.

WHEN to save (call this after each of these):
- Architectural decisions or tradeoffs
- Bug fixes (what was wrong, why, how you fixed it)
- New patterns or conventions established
- Configuration changes or environment setup
- Important discoveries or gotchas
- File structure changes

FORMAT for content — use this structured format:
  **What**: [concise description of what was done]
  **Why**: [the reasoning, user request, or problem that drove it]
  **Where**: [files/paths affected, e.g. src/auth/middleware.ts, internal/store/store.go]
  **Learned**: [any gotchas, edge cases, or decisions made — omit if none]

TITLE should be short and searchable, like: "JWT auth middleware", "FTS5 query sanitization", "Fixed N+1 in user list"

Examples:
  title: "Switched from sessions to JWT"
  type: "decision"
  content: "**What**: Replaced express-session with jsonwebtoken for auth\n**Why**: Session storage doesn't scale across multiple instances\n**Where**: src/middleware/auth.ts, src/routes/login.ts\n**Learned**: Must set httpOnly and secure flags on the cookie, refresh tokens need separate rotation logic"

  title: "Fixed FTS5 syntax error on special chars"
  type: "bugfix"
  content: "**What**: Wrapped each search term in quotes before passing to FTS5 MATCH\n**Why**: Users typing queries like 'fix auth bug' would crash because FTS5 interprets special chars as operators\n**Where**: internal/store/store.go — sanitizeFTS() function\n**Learned**: FTS5 MATCH syntax is NOT the same as LIKE — always sanitize user input"`),
				mcp.WithString("title",
					mcp.Required(),
					mcp.Description("Short, searchable title (e.g. 'JWT auth middleware', 'Fixed N+1 query')"),
				),
				mcp.WithString("content",
					mcp.Required(),
					mcp.Description("Structured content using **What**, **Why**, **Where**, **Learned** format"),
				),
				mcp.WithString("type",
					mcp.Description("Category: decision, architecture, bugfix, pattern, config, discovery, learning (default: manual)"),
				),
				mcp.WithString("session_id",
					mcp.Description("Session ID to associate with (default: manual-save-{project})"),
				),
				mcp.WithString("scope",
					mcp.Description("Scope for this observation: project (default) or personal"),
				),
				mcp.WithString("topic_key",
					mcp.Description("Optional topic identifier for upserts (e.g. architecture/auth-model). Reuses and updates the latest observation in same project+scope."),
				),
			),
			queuedWriteHandler(writeQueue, handleSave(s, cfg, activity)),
		)
	}

	// ─── mem_update (profile: agent, deferred) ──────────────────────────
	if shouldRegister("mem_update", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_update",
				mcp.WithDescription("Update an existing observation by ID. Only provided fields are changed."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Update Memory"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithNumber("id",
					mcp.Required(),
					mcp.Description("Observation ID to update"),
				),
				mcp.WithString("title",
					mcp.Description("New title"),
				),
				mcp.WithString("content",
					mcp.Description("New content"),
				),
				mcp.WithString("type",
					mcp.Description("New type/category"),
				),
				mcp.WithString("scope",
					mcp.Description("New scope: project or personal"),
				),
				mcp.WithString("topic_key",
					mcp.Description("New topic key (normalized internally)"),
				),
			),
			queuedWriteHandler(writeQueue, handleUpdate(s)),
		)
	}

	// ─── mem_suggest_topic_key (profile: agent, deferred) ───────────────
	if shouldRegister("mem_suggest_topic_key", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_suggest_topic_key",
				mcp.WithDescription("Suggest a stable topic_key for memory upserts. Use this before mem_save when you want evolving topics (like architecture decisions) to update a single observation over time."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Suggest Topic Key"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("type",
					mcp.Description("Observation type/category, e.g. architecture, decision, bugfix"),
				),
				mcp.WithString("title",
					mcp.Description("Observation title (preferred input for stable keys)"),
				),
				mcp.WithString("content",
					mcp.Description("Observation content used as fallback if title is empty"),
				),
			),
			handleSuggestTopicKey(),
		)
	}

	// ─── mem_delete (profile: admin, deferred) ──────────────────────────
	if shouldRegister("mem_delete", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_delete",
				mcp.WithDescription("Delete an observation by ID. Soft-delete by default; set hard_delete=true for permanent deletion."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Delete Memory"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(true),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithNumber("id",
					mcp.Required(),
					mcp.Description("Observation ID to delete"),
				),
				mcp.WithBoolean("hard_delete",
					mcp.Description("If true, permanently deletes the observation"),
				),
			),
			queuedWriteHandler(writeQueue, handleDelete(s)),
		)
	}

	// ─── mem_save_prompt (profile: agent, eager) ────────────────────────
	if shouldRegister("mem_save_prompt", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_save_prompt",
				mcp.WithDescription("Save a user prompt to persistent memory. Use this to record what the user asked — their intent, questions, and requests — so future sessions have context about the user's goals."),
				mcp.WithTitleAnnotation("Save User Prompt"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("content",
					mcp.Required(),
					mcp.Description("The user's prompt text"),
				),
				mcp.WithString("session_id",
					mcp.Description("Session ID to associate with (default: manual-save-{project})"),
				),
			),
			queuedWriteHandler(writeQueue, handleSavePrompt(s, cfg)),
		)
	}

	// ─── mem_context (profile: agent, core — always in context) ────────
	if shouldRegister("mem_context", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_context",
				mcp.WithDescription("Get recent memory context from previous sessions. Shows recent sessions and observations to understand what was done before."),
				mcp.WithTitleAnnotation("Get Memory Context"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("project",
					mcp.Description("Filter by project (omit for all projects)"),
				),
				mcp.WithString("scope",
					mcp.Description("Filter observations by scope: project (default) or personal"),
				),
				// JW7: limit param removed — schema advertised it but handleContext never read it.
			),
			handleContext(s, cfg, activity),
		)
	}

	// ─── mem_stats (profile: admin, deferred) ───────────────────────────
	if shouldRegister("mem_stats", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_stats",
				mcp.WithDescription("Show memory system statistics — total sessions, observations, and projects tracked."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Memory Stats"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("project",
					mcp.Description("Project to echo in envelope context (omit for auto-detect; stats themselves are global aggregates)"),
				),
			),
			handleStats(s),
		)
	}

	// ─── mem_timeline (profile: admin, deferred) ────────────────────────
	if shouldRegister("mem_timeline", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_timeline",
				mcp.WithDescription("Show chronological context around a specific observation. Use after mem_search to drill into the timeline of events surrounding a search result. This is the progressive disclosure pattern: search first, then timeline to understand context."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Memory Timeline"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithNumber("observation_id",
					mcp.Required(),
					mcp.Description("The observation ID to center the timeline on (from mem_search results)"),
				),
				mcp.WithNumber("before",
					mcp.Description("Number of observations to show before the focus (default: 5)"),
				),
				mcp.WithNumber("after",
					mcp.Description("Number of observations to show after the focus (default: 5)"),
				),
				mcp.WithString("project",
					mcp.Description("Filter by project name (omit for auto-detect)"),
				),
			),
			handleTimeline(s),
		)
	}

	// ─── mem_get_observation (profile: agent, eager) ────────────────────
	if shouldRegister("mem_get_observation", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_get_observation",
				mcp.WithDescription("Get the full content of a specific observation by ID. Use when you need the complete, untruncated content of an observation found via mem_search or mem_timeline."),
				mcp.WithTitleAnnotation("Get Observation"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithNumber("id",
					mcp.Required(),
					mcp.Description("The observation ID to retrieve"),
				),
			),
			handleGetObservation(s),
		)
	}

	// ─── mem_session_summary (profile: agent, core — always in context) ─
	if shouldRegister("mem_session_summary", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_session_summary",
				mcp.WithTitleAnnotation("Save Session Summary"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithDescription(`Save a comprehensive end-of-session summary. Call this when a session is ending or when significant work is complete. This creates a structured summary that future sessions will use to understand what happened.

FORMAT — use this exact structure in the content field:

## Goal
[One sentence: what were we building/working on in this session]

## Instructions
[User preferences, constraints, or context discovered during this session. Things a future agent needs to know about HOW the user wants things done. Skip if nothing notable.]

## Discoveries
- [Technical finding, gotcha, or learning 1]
- [Technical finding 2]
- [Important API behavior, config quirk, etc.]

## Accomplished
- ✅ [Completed task 1 — with key implementation details]
- ✅ [Completed task 2 — mention files changed]
- 🔲 [Identified but not yet done — for next session]

## Relevant Files
- path/to/file.ts — [what it does or what changed]
- path/to/other.go — [role in the architecture]

GUIDELINES:
- Be CONCISE but don't lose important details (file paths, error messages, decisions)
- Focus on WHAT and WHY, not HOW (the code itself is in the repo)
- Include things that would save a future agent time
- The Discoveries section is the most valuable — capture gotchas and non-obvious learnings
- Relevant Files should only include files that were significantly changed or are important for context`),
				mcp.WithString("content",
					mcp.Required(),
					mcp.Description("Full session summary using the Goal/Instructions/Discoveries/Accomplished/Files format"),
				),
				mcp.WithString("session_id",
					mcp.Description("Session ID (default: manual-save-{project})"),
				),
				// project field intentionally omitted — auto-detect only (REQ-308 write-tool contract)
			),
			queuedWriteHandler(writeQueue, handleSessionSummary(s, cfg, activity)),
		)
	}

	// ─── mem_session_start (profile: agent, deferred) ───────────────────
	if shouldRegister("mem_session_start", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_session_start",
				mcp.WithDescription("Register the start of a new coding session. Call this at the beginning of a session to track activity."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Start Session"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("id",
					mcp.Required(),
					mcp.Description("Unique session identifier"),
				),
				mcp.WithString("directory",
					mcp.Description("Working directory"),
				),
			),
			queuedWriteHandler(writeQueue, handleSessionStart(s, cfg, activity)),
		)
	}

	// ─── mem_session_end (profile: agent, deferred) ─────────────────────
	if shouldRegister("mem_session_end", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_session_end",
				mcp.WithDescription("Mark a coding session as completed with an optional summary."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("End Session"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("id",
					mcp.Required(),
					mcp.Description("Session identifier to close"),
				),
				mcp.WithString("summary",
					mcp.Description("Summary of what was accomplished"),
				),
			),
			queuedWriteHandler(writeQueue, handleSessionEnd(s, cfg, activity)),
		)
	}

	// ─── mem_capture_passive (profile: agent, deferred) ─────────────────
	if shouldRegister("mem_capture_passive", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_capture_passive",
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Capture Learnings"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithDescription(`Extract and save structured learnings from text output. Use this at the end of a task to capture knowledge automatically.

The tool looks for sections like "## Key Learnings:" or "## Aprendizajes Clave:" and extracts numbered or bulleted items. Each item is saved as a separate observation.

Duplicates are automatically detected and skipped — safe to call multiple times with the same content.`),
				mcp.WithString("content",
					mcp.Required(),
					mcp.Description("The text output containing a '## Key Learnings:' section with numbered or bulleted items"),
				),
				mcp.WithString("session_id",
					mcp.Description("Session ID (default: manual-save-{project})"),
				),
				mcp.WithString("source",
					mcp.Description("Source identifier (e.g. 'subagent-stop', 'session-end')"),
				),
			),
			queuedWriteHandler(writeQueue, handleCapturePassive(s, cfg, activity)),
		)
	}

	// ─── mem_merge_projects (profile: admin, deferred) ──────────────────
	if shouldRegister("mem_merge_projects", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_merge_projects",
				mcp.WithDescription("Merge memories from multiple project name variants into one canonical name. Use when you discover project name drift (e.g. 'Engram' and 'engram' should be the same project). DESTRUCTIVE — moves all records from source names to the canonical name."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Merge Projects"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(true),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("from",
					mcp.Required(),
					mcp.Description("Comma-separated list of project names to merge FROM (e.g. 'Engram,engram-memory,ENGRAM')"),
				),
				mcp.WithString("to",
					mcp.Required(),
					mcp.Description("The canonical project name to merge INTO (e.g. 'engram')"),
				),
			),
			queuedWriteHandler(writeQueue, handleMergeProjects(s)),
		)
	}

	// ─── mem_current_project (profile: agent) ────────────────────────────
	if shouldRegister("mem_current_project", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_current_project",
				mcp.WithDescription("Detect the current project from the working directory. Returns project name, source (how it was detected), path, and available alternatives. NEVER errors — use this for discovery before writing. Recommended as the first call when starting a new session."),
				mcp.WithTitleAnnotation("Detect Current Project"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
			),
			handleCurrentProject(s),
		)
	}

	// ─── mem_doctor (profile: agent, deferred) ──────────────────────────
	if shouldRegister("mem_doctor", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_doctor",
				mcp.WithDescription("Run read-only operational diagnostics. Returns the same structured envelope as `engram doctor --json`."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Run Engram Doctor"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("project", mcp.Description("Project to diagnose (omit for auto-detect)")),
				mcp.WithString("check", mcp.Description("Optional diagnostic check code to run")),
			),
			handleDoctor(s),
		)
	}

	// ─── mem_judge (profile: agent, eager) — REQ-003, Design §6 ─────────
	if shouldRegister("mem_judge", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_judge",
				mcp.WithDescription(`Record a verdict on a pending memory conflict surfaced by mem_save.

WHEN TO CALL: After mem_save returns judgment_required=true, iterate candidates[] and call mem_judge once per entry using that entry's judgment_id.

PARAMS:
  judgment_id (required) — from candidates[].judgment_id in the mem_save response
  relation    (required) — one of: related, compatible, scoped, conflicts_with, supersedes, not_conflict
  reason      (optional) — free-text explanation of the verdict
  evidence    (optional) — JSON or text supporting the verdict
  confidence  (optional) — float 0..1; default 1.0

WHEN TO ASK USER:
  - confidence < 0.7: surface to user before judging
  - relation in {supersedes, conflicts_with} AND type in {architecture, policy, decision}: ask user
  - otherwise: resolve silently

SUCCESS: Returns the updated relation row with judgment_status="judged".
ERROR: Returns IsError=true if judgment_id is unknown or relation verb is invalid. Row is NOT mutated on error.

Re-judging an already-judged ID overwrites the verdict (deliberate revision).`),
				mcp.WithTitleAnnotation("Judge Memory Conflict"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("judgment_id",
					mcp.Required(),
					mcp.Description("The judgment_id from candidates[] in the mem_save response (format: rel-<hex>)"),
				),
				mcp.WithString("relation",
					mcp.Required(),
					mcp.Description("Verdict: related | compatible | scoped | conflicts_with | supersedes | not_conflict"),
				),
				mcp.WithString("reason",
					mcp.Description("Free-text explanation of the verdict"),
				),
				mcp.WithString("evidence",
					mcp.Description("Supporting evidence (JSON or free text)"),
				),
				mcp.WithNumber("confidence",
					mcp.Description("Confidence score 0.0..1.0 (default: 1.0)"),
				),
				mcp.WithString("session_id",
					mcp.Description("Session ID for provenance (default: auto)"),
				),
			),
			queuedWriteHandler(writeQueue, handleJudge(s, activity)),
		)
	}

	// ─── mem_compare (profile: agent, eager) — REQ-011, Design §9 ────────
	if shouldRegister("mem_compare", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_compare",
				mcp.WithDescription(`Persist a semantic verdict you have already judged externally (with your LLM) into Engram.

WHEN TO CALL: After you have evaluated two memories and reached a verdict, call mem_compare to PERSIST that verdict into the relation store. You do the judgment; mem_compare records it.

PARAMS:
  memory_id_a  (required) — integer id of the first observation (from mem_search or mem_get_observation)
  memory_id_b  (required) — integer id of the second observation
  relation     (required) — one of: related, compatible, scoped, conflicts_with, supersedes, not_conflict
  confidence   (required) — float 0..1; your self-reported confidence in the verdict
  reasoning    (required) — explanation of the verdict, max 200 chars
  model        (optional) — your model identifier, stored for provenance (e.g. "claude-haiku-4-5")

BEHAVIOR:
  - Persists the verdict via JudgeBySemantic with system provenance (marked_by_actor="engram").
  - not_conflict: no row is inserted; tool returns success with empty sync_id (the verdict is recorded but not stored — it means "we evaluated these and they do not conflict").
  - Idempotent: calling again for the same pair updates the existing row.
  - Cross-project pairs are rejected.

SUCCESS: Returns { "sync_id": "rel-..." } on persist, { "sync_id": "" } on not_conflict.
ERROR: Returns IsError=true if IDs are unknown, relation is invalid, or cross-project pair.`),
				mcp.WithTitleAnnotation("Compare Memory Pair (Persist Semantic Verdict)"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithNumber("memory_id_a",
					mcp.Required(),
					mcp.Description("Integer id of the first observation (from mem_search #id)"),
				),
				mcp.WithNumber("memory_id_b",
					mcp.Required(),
					mcp.Description("Integer id of the second observation (from mem_search #id)"),
				),
				mcp.WithString("relation",
					mcp.Required(),
					mcp.Description("Verdict: related | compatible | scoped | conflicts_with | supersedes | not_conflict"),
				),
				mcp.WithNumber("confidence",
					mcp.Required(),
					mcp.Description("Confidence score 0.0..1.0"),
				),
				mcp.WithString("reasoning",
					mcp.Required(),
					mcp.Description("Brief explanation of the verdict (max 200 chars)"),
				),
				mcp.WithString("model",
					mcp.Description("Your model identifier for provenance (e.g. \"claude-haiku-4-5\")"),
				),
			),
			handleCompare(s, activity),
		)
	}
}

// ─── Tool Handlers ───────────────────────────────────────────────────────────

// handleCurrentProject implements mem_current_project. It NEVER returns an error
// even on ambiguous cwd — it always returns a success result with whatever
// detection info is available (REQ-313).
func handleCurrentProject(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cwd, _ := os.Getwd()
		res := projectpkg.DetectProjectFull(cwd)

		envelope := map[string]any{
			"project":            res.Project,
			"project_source":     res.Source,
			"project_path":       res.Path,
			"cwd":                cwd,
			"available_projects": res.AvailableProjects,
		}
		if res.Warning != "" {
			envelope["warning"] = res.Warning
		}
		if res.Error != nil {
			// REQ-313: not an error response — just surface the info.
			envelope["error_hint"] = res.Error.Error()
		}
		out, _ := jsonMarshal(envelope)
		return mcp.NewToolResultText(string(out)), nil
	}
}

func handleSearch(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, _ := req.GetArguments()["query"].(string)
		typ, _ := req.GetArguments()["type"].(string)
		projectOverride, _ := req.GetArguments()["project"].(string)
		scope, _ := req.GetArguments()["scope"].(string)
		limit := intArg(req, "limit", 10)

		// Resolve project: validate override or auto-detect (REQ-310, REQ-311)
		detRes, err := resolveReadProject(s, projectOverride)
		if err != nil {
			var upe *unknownProjectError
			if errors.As(err, &upe) {
				return errorWithMeta("unknown_project",
					fmt.Sprintf("Project %q not found in store", upe.Name),
					upe.AvailableProjects,
				), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("Project resolution failed: %s", err)), nil
		}
		project := detRes.Project
		project, _ = store.NormalizeProject(project)
		detRes.Project = project // JR2-1: keep envelope in sync with normalized query project

		sessionID := defaultSessionID(project)
		activity.RecordToolCall(sessionID)

		results, err := s.Search(query, store.SearchOptions{
			Type:    typ,
			Project: project,
			Scope:   scope,
			Limit:   limit,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Search error: %s. Try simpler keywords.", err)), nil
		}

		if len(results) == 0 {
			// JW4: use respondWithProject even for empty results.
			return respondWithProject(detRes, fmt.Sprintf("No memories found for: %q", query), nil), nil
		}

		// Batch-load relations for all results (REQ-002). Avoids N+1.
		syncIDs := make([]string, 0, len(results))
		for _, r := range results {
			if r.SyncID != "" {
				syncIDs = append(syncIDs, r.SyncID)
			}
		}
		relationsMap := map[string]store.ObservationRelations{}
		if len(syncIDs) > 0 {
			if rm, relErr := s.GetRelationsForObservations(syncIDs); relErr == nil {
				relationsMap = rm
			}
			// Errors from relation loading are swallowed — search must not fail.
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Found %d memories:\n\n", len(results))
		anyTruncated := false
		for i, r := range results {
			projectDisplay := ""
			if r.Project != nil {
				projectDisplay = fmt.Sprintf(" | project: %s", *r.Project)
			}
			preview := truncate(r.Content, 300)
			if len(r.Content) > 300 {
				anyTruncated = true
				preview += " [preview]"
			}
			fmt.Fprintf(&b, "[%d] #%d (%s) — %s\n    %s\n    %s%s | scope: %s\n",
				i+1, r.ID, r.Type, r.Title,
				preview,
				r.CreatedAt, projectDisplay, r.Scope)

			// Append relation annotations. Skip orphaned (filtered by store).
			//
			// Annotation format contract (REQ-012, Design §7):
			//   supersedes: #<id> (<title>)            judged supersedes
			//   superseded_by: #<id> (<title>)         judged superseded_by
			//   conflicts: #<id> (<title>)             judged conflicts_with
			//   conflict: contested by #<id> (pending) pending (UNCHANGED from Phase 1)
			//
			// <id> is the observation's integer primary key. <title> is the related
			// observation's title; "(deleted)" when the observation is missing or soft-deleted.
			// Prefixes (supersedes:, superseded_by:, conflicts:) are stable across Phase 3.
			if rels, ok := relationsMap[r.SyncID]; ok {
				for _, rel := range rels.AsSource {
					switch {
					case rel.Relation == store.RelationSupersedes && rel.JudgmentStatus == store.JudgmentStatusJudged:
						title := rel.TargetTitle
						if rel.TargetMissing || title == "" {
							title = "deleted"
						}
						fmt.Fprintf(&b, "    supersedes: #%d (%s)\n", rel.TargetIntID, title)
					case rel.Relation == store.RelationConflictsWith && rel.JudgmentStatus == store.JudgmentStatusJudged:
						title := rel.TargetTitle
						if rel.TargetMissing || title == "" {
							title = "deleted"
						}
						fmt.Fprintf(&b, "    conflicts: #%d (%s)\n", rel.TargetIntID, title)
					case rel.JudgmentStatus == store.JudgmentStatusPending:
						// UNCHANGED from Phase 1 — byte-for-byte preserved.
						fmt.Fprintf(&b, "    conflict: contested by #%s (pending)\n", rel.TargetID)
					}
				}
				for _, rel := range rels.AsTarget {
					switch {
					case rel.Relation == store.RelationSupersedes && rel.JudgmentStatus == store.JudgmentStatusJudged:
						title := rel.SourceTitle
						if rel.SourceMissing || title == "" {
							title = "deleted"
						}
						fmt.Fprintf(&b, "    superseded_by: #%d (%s)\n", rel.SourceIntID, title)
					case rel.JudgmentStatus == store.JudgmentStatusPending:
						// UNCHANGED from Phase 1 — byte-for-byte preserved.
						fmt.Fprintf(&b, "    conflict: contested by #%s (pending)\n", rel.SourceID)
					}
				}
			}
			b.WriteString("\n")
		}
		if anyTruncated {
			fmt.Fprintf(&b, "---\nResults above are previews (300 chars). To read the full content of a specific memory, call mem_get_observation(id: <ID>).\n")
		}

		if nudge := activity.NudgeIfNeeded(sessionID); nudge != "" {
			b.WriteString(nudge)
		}

		// JW4: use respondWithProject for the success path (REQ-314).
		return respondWithProject(detRes, b.String(), nil), nil
	}
}

func handleSave(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		title, _ := req.GetArguments()["title"].(string)
		content, _ := req.GetArguments()["content"].(string)
		typ, _ := req.GetArguments()["type"].(string)
		sessionID, _ := req.GetArguments()["session_id"].(string)
		scope, _ := req.GetArguments()["scope"].(string)
		topicKey, _ := req.GetArguments()["topic_key"].(string)
		// project field intentionally not read — auto-detect only (REQ-308)

		// Auto-detect project from cwd; fail fast on ambiguous (REQ-308, REQ-309)
		detRes, err := resolveWriteProject()
		if err != nil {
			// JW1: use AvailableProjects from detection result (repos in cwd),
			// NOT stats.Projects (all store projects).
			return errorWithMeta("ambiguous_project",
				fmt.Sprintf("Cannot determine project: %s", err),
				detRes.AvailableProjects,
			), nil
		}
		project := detRes.Project

		// Normalize project name and capture warning
		normalized, normWarning := store.NormalizeProject(project)
		project = normalized

		if typ == "" {
			typ = "manual"
		}
		if sessionID == "" {
			sessionID = defaultSessionID(project)
		}
		suggestedTopicKey := suggestTopicKey(typ, title, content)

		// Check for similar existing projects (only when this project has no existing observations)
		var similarWarning string
		if project != "" {
			existingNames, _ := s.ListProjectNames()
			isNew := true
			for _, e := range existingNames {
				if e == project {
					isNew = false
					break
				}
			}
			if isNew && len(existingNames) > 0 {
				matches := projectpkg.FindSimilar(project, existingNames, 3)
				if len(matches) > 0 {
					bestMatch := matches[0].Name
					obsCount, _ := s.CountObservationsForProject(bestMatch)
					similarWarning = fmt.Sprintf("⚠️ Project %q has no memories. Similar project found: %q (%d memories). Consider using that name instead.", project, bestMatch, obsCount)
				}
			}
		}

		// Ensure the implicit MCP session exists with the current working directory.
		_ = ensureImplicitSessionWithCWD(s, sessionID, project)

		truncated := len(content) > s.MaxObservationLength()

		savedID, err := s.AddObservation(store.AddObservationParams{
			SessionID: sessionID,
			Type:      typ,
			Title:     title,
			Content:   content,
			Project:   project,
			Scope:     scope,
			TopicKey:  topicKey,
		})
		if err != nil {
			return mcp.NewToolResultError("Failed to save: " + err.Error()), nil
		}

		activity.RecordSave(defaultSessionID(project))

		msg := fmt.Sprintf("Memory saved: %q (%s)", title, typ)
		if topicKey == "" && suggestedTopicKey != "" {
			msg += fmt.Sprintf("\nSuggested topic_key: %s", suggestedTopicKey)
		}
		if truncated {
			msg += fmt.Sprintf("\n⚠ WARNING: Content was truncated from %d to %d chars. Consider splitting into smaller observations.", len(content), s.MaxObservationLength())
		}
		if normWarning != "" {
			msg += "\n" + normWarning
		}
		if similarWarning != "" {
			msg += "\n" + similarWarning
		}

		// Post-transaction conflict candidate detection (REQ-001).
		// Errors are logged and swallowed — detection failure never fails the save.
		extra := map[string]any{}
		// Build CandidateOptions, forwarding any MCPConfig overrides.
		// nil fields mean "use store defaults"; explicit pointer values override.
		candOpts := store.CandidateOptions{
			Project:   project,
			Scope:     scope,
			BM25Floor: cfg.BM25Floor, // nil → store default (-2.0); explicit value overrides
		}
		if cfg.Limit != nil {
			candOpts.Limit = *cfg.Limit
		}
		candidates, candErr := s.FindCandidates(savedID, candOpts)
		if candErr != nil {
			// Log only — do not fail the save.
			fmt.Fprintf(os.Stderr, "engram: FindCandidates error (non-fatal): %v\n", candErr)
		}

		// Fetch the saved observation's sync_id for the envelope (REQ-001).
		var savedSyncID string
		if obs, obsErr := s.GetObservation(savedID); obsErr == nil {
			savedSyncID = obs.SyncID
			extra["id"] = savedID
			extra["sync_id"] = savedSyncID
		}

		if len(candidates) > 0 {
			extra["judgment_required"] = true
			extra["judgment_status"] = "pending"
			extra["judgment_id"] = candidates[0].JudgmentID // first candidate's rel sync_id (design convenience)

			candList := make([]map[string]any, 0, len(candidates))
			for _, c := range candidates {
				entry := map[string]any{
					"id":          c.ID,
					"sync_id":     c.SyncID,
					"title":       c.Title,
					"type":        c.Type,
					"score":       c.Score,
					"judgment_id": c.JudgmentID,
				}
				if c.TopicKey != nil {
					entry["topic_key"] = *c.TopicKey
				}
				candList = append(candList, entry)
			}
			extra["candidates"] = candList

			msg += fmt.Sprintf("\nCONFLICT REVIEW PENDING — %d candidate(s); use mem_judge to record verdicts.", len(candidates))
		} else {
			extra["judgment_required"] = false
		}

		// Update detRes to reflect normalized project for envelope accuracy
		detRes.Project = project
		return respondWithProject(detRes, msg, extra), nil
	}
}

func handleSuggestTopicKey() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		typ, _ := req.GetArguments()["type"].(string)
		title, _ := req.GetArguments()["title"].(string)
		content, _ := req.GetArguments()["content"].(string)

		if strings.TrimSpace(title) == "" && strings.TrimSpace(content) == "" {
			return mcp.NewToolResultError("provide title or content to suggest a topic_key"), nil
		}

		topicKey := suggestTopicKey(typ, title, content)
		if topicKey == "" {
			return mcp.NewToolResultError("could not suggest topic_key from input"), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Suggested topic_key: %s", topicKey)), nil
	}
}

func handleUpdate(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := int64(intArg(req, "id", 0))
		if id == 0 {
			return mcp.NewToolResultError("id is required"), nil
		}

		update := store.UpdateObservationParams{}
		if v, ok := req.GetArguments()["title"].(string); ok {
			update.Title = &v
		}
		if v, ok := req.GetArguments()["content"].(string); ok {
			update.Content = &v
		}
		if v, ok := req.GetArguments()["type"].(string); ok {
			update.Type = &v
		}
		// Tolerant parse: project still accepted even though removed from schema (REQ-308).
		if v, ok := req.GetArguments()["project"].(string); ok && v != "" {
			update.Project = &v
		}
		if v, ok := req.GetArguments()["scope"].(string); ok {
			update.Scope = &v
		}
		if v, ok := req.GetArguments()["topic_key"].(string); ok {
			update.TopicKey = &v
		}

		if update.Title == nil && update.Content == nil && update.Type == nil && update.Project == nil && update.Scope == nil && update.TopicKey == nil {
			return mcp.NewToolResultError("provide at least one field to update"), nil
		}

		var contentLen int
		if update.Content != nil {
			contentLen = len(*update.Content)
		}

		obs, err := s.UpdateObservation(id, update)
		if err != nil {
			return mcp.NewToolResultError("Failed to update memory: " + err.Error()), nil
		}

		msg := fmt.Sprintf("Memory updated: #%d %q (%s, scope=%s)", obs.ID, obs.Title, obs.Type, obs.Scope)
		if contentLen > s.MaxObservationLength() {
			msg += fmt.Sprintf("\n⚠ WARNING: Content was truncated from %d to %d chars. Consider splitting into smaller observations.", contentLen, s.MaxObservationLength())
		}

		// Auto-detect for envelope; tolerant — don't fail update on resolution error
		detRes, detErr := resolveWriteProject()
		if detErr != nil {
			// Still return success for the update itself.
			return mcp.NewToolResultText(msg), nil
		}
		return respondWithProject(detRes, msg, nil), nil
	}
}

func handleDelete(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := int64(intArg(req, "id", 0))
		if id == 0 {
			return mcp.NewToolResultError("id is required"), nil
		}

		hardDelete := boolArg(req, "hard_delete", false)
		if err := s.DeleteObservation(id, hardDelete); err != nil {
			return mcp.NewToolResultError("Failed to delete memory: " + err.Error()), nil
		}

		mode := "soft-deleted"
		if hardDelete {
			mode = "permanently deleted"
		}
		return mcp.NewToolResultText(fmt.Sprintf("Memory #%d %s", id, mode)), nil
	}
}

func handleSavePrompt(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, _ := req.GetArguments()["content"].(string)
		sessionID, _ := req.GetArguments()["session_id"].(string)
		// project field intentionally not read — auto-detect only (REQ-308)

		detRes, err := resolveWriteProject()
		if err != nil {
			// JW1: use AvailableProjects from detection result (repos in cwd).
			return errorWithMeta("ambiguous_project",
				fmt.Sprintf("Cannot determine project: %s", err),
				detRes.AvailableProjects,
			), nil
		}
		project, _ := store.NormalizeProject(detRes.Project)

		if sessionID == "" {
			sessionID = defaultSessionID(project)
		}

		// Ensure the implicit MCP session exists with the current working directory.
		_ = ensureImplicitSessionWithCWD(s, sessionID, project)

		_, err = s.AddPrompt(store.AddPromptParams{
			SessionID: sessionID,
			Content:   content,
			Project:   project,
		})
		if err != nil {
			return mcp.NewToolResultError("Failed to save prompt: " + err.Error()), nil
		}

		detRes.Project = project
		return respondWithProject(detRes, fmt.Sprintf("Prompt saved: %q", truncate(content, 80)), nil), nil
	}
}

func handleContext(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		projectOverride, _ := req.GetArguments()["project"].(string)
		scope, _ := req.GetArguments()["scope"].(string)

		// Resolve project: validate override or auto-detect (REQ-310, REQ-311)
		detRes, err := resolveReadProject(s, projectOverride)
		if err != nil {
			var upe *unknownProjectError
			if errors.As(err, &upe) {
				return errorWithMeta("unknown_project",
					fmt.Sprintf("Project %q not found in store", upe.Name),
					upe.AvailableProjects,
				), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("Project resolution failed: %s", err)), nil
		}
		project := detRes.Project
		project, _ = store.NormalizeProject(project)
		detRes.Project = project // JR2-1: keep envelope in sync with normalized query project

		sessionID := defaultSessionID(project)
		activity.RecordToolCall(sessionID)

		contextResult, err := s.FormatContext(project, scope)
		if err != nil {
			return mcp.NewToolResultError("Failed to get context: " + err.Error()), nil
		}

		if contextResult == "" {
			return respondWithProject(detRes, "No previous session memories found.", nil), nil
		}

		stats, _ := s.Stats()
		var projects string
		if len(stats.Projects) > 0 {
			projects = strings.Join(stats.Projects, ", ")
		} else {
			projects = "none"
		}

		result := fmt.Sprintf("%s\n---\nMemory stats: %d sessions, %d observations across projects: %s",
			contextResult, stats.TotalSessions, stats.TotalObservations, projects)

		if nudge := activity.NudgeIfNeeded(sessionID); nudge != "" {
			result += nudge
		}

		return respondWithProject(detRes, result, nil), nil
	}
}

func handleStats(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		projectOverride, _ := req.GetArguments()["project"].(string)

		// Resolve project: validate override or auto-detect (REQ-310, REQ-311, REQ-314)
		detRes, err := resolveReadProject(s, projectOverride)
		if err != nil {
			var upe *unknownProjectError
			if errors.As(err, &upe) {
				return errorWithMeta("unknown_project",
					fmt.Sprintf("Project %q not found in store", upe.Name),
					upe.AvailableProjects,
				), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("Project resolution failed: %s", err)), nil
		}

		stats, err := loadMCPStats(s)
		if err != nil {
			return mcp.NewToolResultError("Failed to get stats: " + err.Error()), nil
		}

		var projects string
		if len(stats.Projects) > 0 {
			projects = strings.Join(stats.Projects, ", ")
		} else {
			projects = "none yet"
		}

		result := fmt.Sprintf("Memory System Stats:\n- Sessions: %d\n- Observations: %d\n- Prompts: %d\n- Projects: %s",
			stats.TotalSessions, stats.TotalObservations, stats.TotalPrompts, projects)

		return respondWithProject(detRes, result, nil), nil
	}
}

func DoctorToolHandler(s *store.Store) server.ToolHandlerFunc {
	return handleDoctor(s)
}

func handleDoctor(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		projectOverride, _ := req.GetArguments()["project"].(string)
		check, _ := req.GetArguments()["check"].(string)
		detRes, err := resolveReadProject(s, projectOverride)
		if err != nil {
			var upe *unknownProjectError
			if errors.As(err, &upe) {
				return errorWithMeta("unknown_project", fmt.Sprintf("Project %q not found in store", upe.Name), upe.AvailableProjects), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("Project resolution failed: %s", err)), nil
		}
		project := detRes.Project
		project, _ = store.NormalizeProject(project)
		runner := diagnostic.NewRunner()
		scope := diagnostic.Scope{Store: s, Project: project, Now: time.Now()}
		var report diagnostic.Report
		if strings.TrimSpace(check) != "" {
			report, err = runner.RunOne(ctx, scope, check)
		} else {
			report, err = runner.RunAll(ctx, scope)
		}
		if err != nil {
			report = diagnostic.ErrorReport(project, err)
		}
		out, marshalErr := jsonMarshal(report)
		if marshalErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Doctor JSON error: %s", marshalErr)), nil
		}
		result := mcp.NewToolResultText(string(out))
		if report.Status == diagnostic.StatusError {
			result.IsError = true
		}
		return result, nil
	}
}

func handleTimeline(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		observationID := int64(intArg(req, "observation_id", 0))
		if observationID == 0 {
			return mcp.NewToolResultError("observation_id is required"), nil
		}
		before := intArg(req, "before", 5)
		after := intArg(req, "after", 5)
		projectOverride, _ := req.GetArguments()["project"].(string)

		// Resolve project: validate override or auto-detect (REQ-310, REQ-311, REQ-314)
		detRes, err := resolveReadProject(s, projectOverride)
		if err != nil {
			var upe *unknownProjectError
			if errors.As(err, &upe) {
				return errorWithMeta("unknown_project",
					fmt.Sprintf("Project %q not found in store", upe.Name),
					upe.AvailableProjects,
				), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("Project resolution failed: %s", err)), nil
		}

		result, err := s.Timeline(observationID, before, after)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Timeline error: %s", err)), nil
		}

		var b strings.Builder

		// Session header
		if result.SessionInfo != nil {
			summary := ""
			if result.SessionInfo.Summary != nil {
				summary = fmt.Sprintf(" — %s", truncate(*result.SessionInfo.Summary, 100))
			}
			fmt.Fprintf(&b, "Session: %s (%s)%s\n", result.SessionInfo.Project, result.SessionInfo.StartedAt, summary)
			fmt.Fprintf(&b, "Total observations in session: %d\n\n", result.TotalInRange)
		}

		// Before entries
		if len(result.Before) > 0 {
			b.WriteString("─── Before ───\n")
			for _, e := range result.Before {
				fmt.Fprintf(&b, "  #%d [%s] %s — %s\n", e.ID, e.Type, e.Title, truncate(e.Content, 150))
			}
			b.WriteString("\n")
		}

		// Focus observation (highlighted)
		fmt.Fprintf(&b, ">>> #%d [%s] %s <<<\n", result.Focus.ID, result.Focus.Type, result.Focus.Title)
		fmt.Fprintf(&b, "    %s\n", truncate(result.Focus.Content, 500))
		fmt.Fprintf(&b, "    %s\n\n", result.Focus.CreatedAt)

		// After entries
		if len(result.After) > 0 {
			b.WriteString("─── After ───\n")
			for _, e := range result.After {
				fmt.Fprintf(&b, "  #%d [%s] %s — %s\n", e.ID, e.Type, e.Title, truncate(e.Content, 150))
			}
		}

		return respondWithProject(detRes, b.String(), nil), nil
	}
}

func handleGetObservation(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := int64(intArg(req, "id", 0))
		if id == 0 {
			return mcp.NewToolResultError("id is required"), nil
		}

		obs, err := s.GetObservation(id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Observation #%d not found", id)), nil
		}

		// Resolve project from cwd (REQ-310, REQ-314). No override possible for
		// get-by-ID — always auto-detect. JW5: use resolveReadProject (read semantics).
		// Tolerant: don't fail the fetch on resolution error; degrade to plain text.
		detRes, detErr := resolveReadProject(s, "")

		obsProject := ""
		if obs.Project != nil {
			obsProject = fmt.Sprintf("\nProject: %s", *obs.Project)
		}
		scope := fmt.Sprintf("\nScope: %s", obs.Scope)
		topic := ""
		if obs.TopicKey != nil {
			topic = fmt.Sprintf("\nTopic: %s", *obs.TopicKey)
		}
		toolName := ""
		if obs.ToolName != nil {
			toolName = fmt.Sprintf("\nTool: %s", *obs.ToolName)
		}
		duplicateMeta := fmt.Sprintf("\nDuplicates: %d", obs.DuplicateCount)
		revisionMeta := fmt.Sprintf("\nRevisions: %d", obs.RevisionCount)

		result := fmt.Sprintf("#%d [%s] %s\n%s\nSession: %s%s%s\nCreated: %s",
			obs.ID, obs.Type, obs.Title,
			obs.Content,
			obs.SessionID, obsProject+scope+topic, toolName+duplicateMeta+revisionMeta,
			obs.CreatedAt,
		)

		if detErr != nil {
			// Degraded path: resolution failed (e.g. ambiguous cwd). Return
			// the observation content without envelope rather than erroring.
			return mcp.NewToolResultText(result), nil
		}
		return respondWithProject(detRes, result, nil), nil
	}
}

func handleSessionSummary(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, _ := req.GetArguments()["content"].(string)
		sessionID, _ := req.GetArguments()["session_id"].(string)
		// project field intentionally not read — auto-detect only (REQ-308 write-tool contract)

		// Auto-detect project from cwd; fail fast on ambiguous (REQ-308, REQ-309)
		detRes, err := resolveWriteProject()
		if err != nil {
			// JW1: use AvailableProjects from detection result (repos in cwd).
			return errorWithMeta("ambiguous_project",
				fmt.Sprintf("Cannot determine project: %s", err),
				detRes.AvailableProjects,
			), nil
		}
		project, _ := store.NormalizeProject(detRes.Project)

		if sessionID == "" {
			sessionID = defaultSessionID(project)
		}

		// Ensure the implicit MCP session exists with the current working directory.
		_ = ensureImplicitSessionWithCWD(s, sessionID, project)

		_, err = s.AddObservation(store.AddObservationParams{
			SessionID: sessionID,
			Type:      "session_summary",
			Title:     fmt.Sprintf("Session summary: %s", project),
			Content:   content,
			Project:   project,
		})
		if err != nil {
			return mcp.NewToolResultError("Failed to save session summary: " + err.Error()), nil
		}

		msg := fmt.Sprintf("Session summary saved for project %q", project)
		if score := activity.ActivityScore(defaultSessionID(project)); score != "" {
			msg += "\n" + score
		}
		detRes.Project = project
		return respondWithProject(detRes, msg, nil), nil
	}
}

func handleSessionStart(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, _ := req.GetArguments()["id"].(string)
		directory, _ := req.GetArguments()["directory"].(string)
		explicitDirectory := strings.TrimSpace(directory)
		// project field intentionally not read — auto-detect only (REQ-308)

		detRes, err := resolveSessionStartProject(explicitDirectory)
		if err != nil {
			// JW1: use AvailableProjects from detection result (repos in cwd).
			return errorWithMeta("ambiguous_project",
				fmt.Sprintf("Cannot determine project: %s", err),
				detRes.AvailableProjects,
			), nil
		}
		project, _ := store.NormalizeProject(detRes.Project)

		activity.RecordToolCall(defaultSessionID(project))
		if explicitDirectory == "" {
			directory = strings.TrimSpace(detRes.Path)
			if directory == "" {
				directory = currentWorkingDirectory()
			}
		}

		if err := s.CreateSession(id, project, directory); err != nil {
			return mcp.NewToolResultError("Failed to start session: " + err.Error()), nil
		}

		detRes.Project = project
		return respondWithProject(detRes, fmt.Sprintf("Session %q started for project %q", id, project), nil), nil
	}
}

func resolveSessionStartProject(explicitDirectory string) (projectpkg.DetectionResult, error) {
	if explicitDirectory == "" {
		return resolveWriteProject()
	}
	res := projectpkg.DetectProjectFull(explicitDirectory)
	if res.Error != nil {
		return res, res.Error
	}
	if res.Source == projectpkg.SourceDirBasename {
		return resolveWriteProject()
	}
	return res, nil
}

func handleSessionEnd(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, _ := req.GetArguments()["id"].(string)
		summary, _ := req.GetArguments()["summary"].(string)
		// project field intentionally not read — auto-detect only (REQ-308)

		detRes, err := resolveWriteProject()
		if err != nil {
			// For session end, still complete the operation even if project resolution fails.
			// Use basename fallback.
			cwd, _ := os.Getwd()
			detRes = projectpkg.DetectionResult{
				Project: projectpkg.DetectProject(cwd),
				Source:  "dir_basename",
				Path:    cwd,
			}
		}
		project, _ := store.NormalizeProject(detRes.Project)

		if err := s.EndSession(id, summary); err != nil {
			return mcp.NewToolResultError("Failed to end session: " + err.Error()), nil
		}

		activity.ClearSession(defaultSessionID(project))

		detRes.Project = project
		return respondWithProject(detRes, fmt.Sprintf("Session %q completed", id), nil), nil
	}
}

func handleCapturePassive(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, _ := req.GetArguments()["content"].(string)
		sessionID, _ := req.GetArguments()["session_id"].(string)
		source, _ := req.GetArguments()["source"].(string)
		// project field intentionally not read — auto-detect only (REQ-308)

		detRes, err := resolveWriteProject()
		if err != nil {
			// JW1: use AvailableProjects from detection result (repos in cwd).
			return errorWithMeta("ambiguous_project",
				fmt.Sprintf("Cannot determine project: %s", err),
				detRes.AvailableProjects,
			), nil
		}
		project, _ := store.NormalizeProject(detRes.Project)

		activity.RecordToolCall(defaultSessionID(project))

		if content == "" {
			return mcp.NewToolResultError("content is required — include text with a '## Key Learnings:' section"), nil
		}

		if sessionID == "" {
			sessionID = defaultSessionID(project)
			_ = ensureImplicitSessionWithCWD(s, sessionID, project)
		}

		if source == "" {
			source = "mcp-passive"
		}

		result, err := s.PassiveCapture(store.PassiveCaptureParams{
			SessionID: sessionID,
			Content:   content,
			Project:   project,
			Source:    source,
		})
		if err != nil {
			return mcp.NewToolResultError("Passive capture failed: " + err.Error()), nil
		}

		detRes.Project = project
		return respondWithProject(detRes, fmt.Sprintf(
			"Passive capture complete: extracted=%d saved=%d duplicates=%d",
			result.Extracted, result.Saved, result.Duplicates,
		), nil), nil
	}
}

// handleJudge implements mem_judge. It validates params, calls JudgeRelation,
// and returns the updated relation row as JSON.
//
// Tool description contract (Design §6.1):
// "Record a verdict on a pending memory conflict surfaced by mem_save.
// When mem_save returns judgment_required=true, call mem_judge once per
// candidate (judgment_id is in candidates[]). Use to mark SUPERSEDES,
// CONFLICTS_WITH, NOT_CONFLICT, RELATED, COMPATIBLE, or SCOPED.
// Ask the user when ambiguous."
func handleJudge(s *store.Store, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		judgmentID, _ := req.GetArguments()["judgment_id"].(string)
		relation, _ := req.GetArguments()["relation"].(string)

		if judgmentID == "" {
			return mcp.NewToolResultError("judgment_id is required"), nil
		}
		if relation == "" {
			return mcp.NewToolResultError("relation is required"), nil
		}

		// Collect optional fields.
		var reason *string
		if v, ok := req.GetArguments()["reason"].(string); ok && v != "" {
			reason = &v
		}
		var evidence *string
		if v, ok := req.GetArguments()["evidence"].(string); ok && v != "" {
			evidence = &v
		}
		var confidence *float64
		if v, ok := req.GetArguments()["confidence"].(float64); ok {
			// Clamp to [0, 1] per design §6.3.
			if v < 0 {
				v = 0
			}
			if v > 1 {
				v = 1
			}
			confidence = &v
		}

		// Session context for provenance.
		sessionID, _ := req.GetArguments()["session_id"].(string)
		// Actor defaults to "agent" kind for MCP tool calls.
		markedByActor := "agent"
		markedByKind := "agent"
		markedByModel := "" // No model ID available at MCP layer without explicit param.

		result, err := s.JudgeRelation(store.JudgeRelationParams{
			JudgmentID:    judgmentID,
			Relation:      relation,
			Reason:        reason,
			Evidence:      evidence,
			Confidence:    confidence,
			MarkedByActor: markedByActor,
			MarkedByKind:  markedByKind,
			MarkedByModel: markedByModel,
			SessionID:     sessionID,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		envelope := map[string]any{
			"relation": result,
		}
		out, _ := jsonMarshal(envelope)
		return mcp.NewToolResultText(string(out)), nil
	}
}

// handleCompare implements mem_compare. The agent has already judged two
// observations externally; this handler persists the verdict via JudgeBySemantic.
//
// Tool description contract (REQ-011, Design §9):
// "Persist a semantic verdict you have already judged externally into Engram.
// Accepts int IDs for both observations, resolves them to sync_ids, then
// calls JudgeBySemantic. Returns the persisted relation's sync_id."
func handleCompare(s *store.Store, _ *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// --- required numeric IDs ---
		rawA, okA := req.GetArguments()["memory_id_a"].(float64)
		rawB, okB := req.GetArguments()["memory_id_b"].(float64)
		if !okA {
			return mcp.NewToolResultError("memory_id_a is required (integer observation id)"), nil
		}
		if !okB {
			return mcp.NewToolResultError("memory_id_b is required (integer observation id)"), nil
		}
		idA := int64(rawA)
		idB := int64(rawB)

		// --- required string fields ---
		relation, _ := req.GetArguments()["relation"].(string)
		if relation == "" {
			return mcp.NewToolResultError("relation is required"), nil
		}
		reasoning, _ := req.GetArguments()["reasoning"].(string)
		if reasoning == "" {
			return mcp.NewToolResultError("reasoning is required"), nil
		}

		// --- required confidence ---
		rawConf, okConf := req.GetArguments()["confidence"].(float64)
		if !okConf {
			return mcp.NewToolResultError("confidence is required (float 0.0..1.0)"), nil
		}
		// Clamp to [0, 1].
		if rawConf < 0 {
			rawConf = 0
		}
		if rawConf > 1 {
			rawConf = 1
		}

		// --- optional model ---
		model, _ := req.GetArguments()["model"].(string)

		// Resolve integer IDs to sync_ids.
		obsA, err := s.GetObservation(idA)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("observation id=%d not found: %s", idA, err)), nil
		}
		obsB, err := s.GetObservation(idB)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("observation id=%d not found: %s", idB, err)), nil
		}

		syncID, err := s.JudgeBySemantic(store.JudgeBySemanticParams{
			SourceID:   obsA.SyncID,
			TargetID:   obsB.SyncID,
			Relation:   relation,
			Confidence: rawConf,
			Reasoning:  reasoning,
			Model:      model,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// syncID is "" when relation == "not_conflict" (JudgeBySemantic no-op).
		envelope := map[string]any{
			"sync_id": syncID,
		}
		out, _ := jsonMarshal(envelope)
		return mcp.NewToolResultText(string(out)), nil
	}
}

func handleMergeProjects(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fromStr, _ := req.GetArguments()["from"].(string)
		to, _ := req.GetArguments()["to"].(string)

		if fromStr == "" || to == "" {
			return mcp.NewToolResultError("both 'from' and 'to' are required"), nil
		}

		var sources []string
		for _, src := range strings.Split(fromStr, ",") {
			src = strings.TrimSpace(src)
			if src != "" {
				sources = append(sources, src)
			}
		}

		if len(sources) == 0 {
			return mcp.NewToolResultError("at least one source project name is required in 'from'"), nil
		}

		result, err := s.MergeProjects(sources, to)
		if err != nil {
			return mcp.NewToolResultError("Merge failed: " + err.Error()), nil
		}

		msg := fmt.Sprintf("Merged %d source(s) into %q:\n", len(result.SourcesMerged), result.Canonical)
		msg += fmt.Sprintf("  Observations moved: %d\n", result.ObservationsUpdated)
		msg += fmt.Sprintf("  Sessions moved:     %d\n", result.SessionsUpdated)
		msg += fmt.Sprintf("  Prompts moved:      %d\n", result.PromptsUpdated)

		return mcp.NewToolResultText(msg), nil
	}
}

// ─── Project Resolution Helpers ──────────────────────────────────────────────

// unknownProjectError is returned when a read tool receives a project override
// that does not exist in the store.
type unknownProjectError struct {
	Name              string
	AvailableProjects []string
}

func (e *unknownProjectError) Error() string {
	return "unknown project: " + e.Name
}

// resolveWriteProject detects the current project from the process working
// directory. Returns ErrAmbiguousProject if cwd is a parent of multiple repos.
func resolveWriteProject() (projectpkg.DetectionResult, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	res := projectpkg.DetectProjectFull(cwd)
	if res.Error != nil {
		return res, res.Error
	}
	return res, nil
}

// resolveReadProject validates an optional project override against the store.
// If override is empty, falls back to auto-detection from cwd.
// JW2: normalizes the override (lowercase+trim) before ProjectExists lookup so
// that e.g. "MyApp" and "  myapp  " both resolve to the stored "myapp".
func resolveReadProject(s *store.Store, override string) (projectpkg.DetectionResult, error) {
	override = strings.TrimSpace(override)
	if override == "" {
		return resolveWriteProject()
	}
	normalized, _ := store.NormalizeProject(override)
	exists, err := s.ProjectExists(normalized)
	if err != nil {
		return projectpkg.DetectionResult{}, err
	}
	if !exists {
		// Collect available projects for the error.
		stats, _ := s.Stats()
		return projectpkg.DetectionResult{}, &unknownProjectError{
			Name:              normalized,
			AvailableProjects: stats.Projects,
		}
	}
	return projectpkg.DetectionResult{
		Project: normalized,
		Source:  projectpkg.SourceExplicitOverride, // JR2-2: use named constant
		Path:    "",
	}, nil
}

// respondWithProject wraps a tool result by prepending the project envelope
// fields (project, project_source, project_path) to the text output.
// extra is an optional map of additional fields to include.
func respondWithProject(res projectpkg.DetectionResult, text string, extra map[string]any) *mcp.CallToolResult {
	envelope := map[string]any{
		"project":        res.Project,
		"project_source": res.Source,
		"project_path":   res.Path,
		"result":         text,
	}
	if res.Warning != "" {
		envelope["warning"] = res.Warning
	}
	for k, v := range extra {
		envelope[k] = v
	}
	out, _ := jsonMarshal(envelope)
	return mcp.NewToolResultText(string(out))
}

// errorWithMeta returns a structured tool error result with error_code,
// message, available_projects, and a hint for resolution.
func errorWithMeta(code, msg string, availableProjects []string) *mcp.CallToolResult {
	envelope := map[string]any{
		"error_code":         code,
		"message":            msg,
		"available_projects": availableProjects,
	}
	switch code {
	case "ambiguous_project":
		envelope["hint"] = "Use mem_current_project to inspect detection results, or cd into one of the listed repositories."
	case "unknown_project":
		envelope["hint"] = "Use one of the available_projects values, or omit project to auto-detect."
	}
	out, _ := jsonMarshal(envelope)
	result := mcp.NewToolResultText(string(out))
	result.IsError = true
	return result
}

// jsonMarshal marshals v to JSON. Named to allow test injection if needed.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// defaultSessionID returns a project-scoped default session ID.
// If project is non-empty: "manual-save-{project}"
// If project is empty: "manual-save"
func defaultSessionID(project string) string {
	if project == "" {
		return "manual-save"
	}
	return "manual-save-" + project
}

func intArg(req mcp.CallToolRequest, key string, defaultVal int) int {
	v, ok := req.GetArguments()[key].(float64)
	if !ok {
		return defaultVal
	}
	return int(v)
}

func boolArg(req mcp.CallToolRequest, key string, defaultVal bool) bool {
	v, ok := req.GetArguments()[key].(bool)
	if !ok {
		return defaultVal
	}
	return v
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
