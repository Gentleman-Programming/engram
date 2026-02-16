// Package mcp implements the Model Context Protocol server for Engram.
//
// This exposes memory tools via MCP stdio transport so ANY agent
// (OpenCode, Claude Code, Cursor, Windsurf, etc.) can use Engram's
// persistent memory just by adding it as an MCP server.
package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/alanbuscaglia/engram/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func NewServer(s *store.Store) *server.MCPServer {
	srv := server.NewMCPServer(
		"engram",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	registerTools(srv, s)
	return srv
}

func registerTools(srv *server.MCPServer, s *store.Store) {
	// ─── mem_search ──────────────────────────────────────────────────
	srv.AddTool(
		mcp.NewTool("mem_search",
			mcp.WithDescription("Search your persistent memory across all sessions. Use this to find past decisions, bugs fixed, patterns used, files changed, or any context from previous coding sessions."),
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
			mcp.WithNumber("limit",
				mcp.Description("Max results (default: 10, max: 20)"),
			),
		),
		handleSearch(s),
	)

	// ─── mem_save ────────────────────────────────────────────────────
	srv.AddTool(
		mcp.NewTool("mem_save",
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
				mcp.Description("Session ID to associate with (default: manual-save)"),
			),
			mcp.WithString("project",
				mcp.Description("Project name"),
			),
		),
		handleSave(s),
	)

	// ─── mem_save_prompt ────────────────────────────────────────────
	srv.AddTool(
		mcp.NewTool("mem_save_prompt",
			mcp.WithDescription("Save a user prompt to persistent memory. Use this to record what the user asked — their intent, questions, and requests — so future sessions have context about the user's goals."),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("The user's prompt text"),
			),
			mcp.WithString("session_id",
				mcp.Description("Session ID to associate with (default: manual-save)"),
			),
			mcp.WithString("project",
				mcp.Description("Project name"),
			),
		),
		handleSavePrompt(s),
	)

	// ─── mem_context ─────────────────────────────────────────────────
	srv.AddTool(
		mcp.NewTool("mem_context",
			mcp.WithDescription("Get recent memory context from previous sessions. Shows recent sessions and observations to understand what was done before."),
			mcp.WithString("project",
				mcp.Description("Filter by project (omit for all projects)"),
			),
			mcp.WithNumber("limit",
				mcp.Description("Number of observations to retrieve (default: 20)"),
			),
		),
		handleContext(s),
	)

	// ─── mem_stats ───────────────────────────────────────────────────
	srv.AddTool(
		mcp.NewTool("mem_stats",
			mcp.WithDescription("Show memory system statistics — total sessions, observations, and projects tracked."),
		),
		handleStats(s),
	)

	// ─── mem_timeline ───────────────────────────────────────────────
	srv.AddTool(
		mcp.NewTool("mem_timeline",
			mcp.WithDescription("Show chronological context around a specific observation. Use after mem_search to drill into the timeline of events surrounding a search result. This is the progressive disclosure pattern: search first, then timeline to understand context."),
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
		),
		handleTimeline(s),
	)

	// ─── mem_get_observation ────────────────────────────────────────
	srv.AddTool(
		mcp.NewTool("mem_get_observation",
			mcp.WithDescription("Get the full content of a specific observation by ID. Use when you need the complete, untruncated content of an observation found via mem_search or mem_timeline."),
			mcp.WithNumber("id",
				mcp.Required(),
				mcp.Description("The observation ID to retrieve"),
			),
		),
		handleGetObservation(s),
	)

	// ─── mem_session_summary ────────────────────────────────────────
	srv.AddTool(
		mcp.NewTool("mem_session_summary",
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
				mcp.Description("Session ID (default: manual-save)"),
			),
			mcp.WithString("project",
				mcp.Required(),
				mcp.Description("Project name"),
			),
		),
		handleSessionSummary(s),
	)

	// ─── mem_session_start ───────────────────────────────────────────
	srv.AddTool(
		mcp.NewTool("mem_session_start",
			mcp.WithDescription("Register the start of a new coding session. Call this at the beginning of a session to track activity."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("Unique session identifier"),
			),
			mcp.WithString("project",
				mcp.Required(),
				mcp.Description("Project name"),
			),
			mcp.WithString("directory",
				mcp.Description("Working directory"),
			),
		),
		handleSessionStart(s),
	)

	// ─── mem_session_end ─────────────────────────────────────────────
	srv.AddTool(
		mcp.NewTool("mem_session_end",
			mcp.WithDescription("Mark a coding session as completed with an optional summary."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("Session identifier to close"),
			),
			mcp.WithString("summary",
				mcp.Description("Summary of what was accomplished"),
			),
		),
		handleSessionEnd(s),
	)
}

// ─── Tool Handlers ───────────────────────────────────────────────────────────

func handleSearch(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, _ := req.GetArguments()["query"].(string)
		typ, _ := req.GetArguments()["type"].(string)
		project, _ := req.GetArguments()["project"].(string)
		limit := intArg(req, "limit", 10)

		results, err := s.Search(query, store.SearchOptions{
			Type:    typ,
			Project: project,
			Limit:   limit,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Search error: %s. Try simpler keywords.", err)), nil
		}

		if len(results) == 0 {
			return mcp.NewToolResultText(fmt.Sprintf("No memories found for: %q", query)), nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Found %d memories:\n\n", len(results))
		for i, r := range results {
			project := ""
			if r.Project != nil {
				project = fmt.Sprintf(" | project: %s", *r.Project)
			}
			fmt.Fprintf(&b, "[%d] #%d (%s) — %s\n    %s\n    %s%s\n\n",
				i+1, r.ID, r.Type, r.Title,
				truncate(r.Content, 300),
				r.CreatedAt, project)
		}

		return mcp.NewToolResultText(b.String()), nil
	}
}

func handleSave(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		title, _ := req.GetArguments()["title"].(string)
		content, _ := req.GetArguments()["content"].(string)
		typ, _ := req.GetArguments()["type"].(string)
		sessionID, _ := req.GetArguments()["session_id"].(string)
		project, _ := req.GetArguments()["project"].(string)

		if typ == "" {
			typ = "manual"
		}
		if sessionID == "" {
			sessionID = "manual-save"
		}

		// Ensure the session exists
		s.CreateSession(sessionID, project, "")

		_, err := s.AddObservation(store.AddObservationParams{
			SessionID: sessionID,
			Type:      typ,
			Title:     title,
			Content:   content,
			Project:   project,
		})
		if err != nil {
			return mcp.NewToolResultError("Failed to save: " + err.Error()), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Memory saved: %q (%s)", title, typ)), nil
	}
}

func handleSavePrompt(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, _ := req.GetArguments()["content"].(string)
		sessionID, _ := req.GetArguments()["session_id"].(string)
		project, _ := req.GetArguments()["project"].(string)

		if sessionID == "" {
			sessionID = "manual-save"
		}

		// Ensure the session exists
		s.CreateSession(sessionID, project, "")

		_, err := s.AddPrompt(store.AddPromptParams{
			SessionID: sessionID,
			Content:   content,
			Project:   project,
		})
		if err != nil {
			return mcp.NewToolResultError("Failed to save prompt: " + err.Error()), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Prompt saved: %q", truncate(content, 80))), nil
	}
}

func handleContext(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		project, _ := req.GetArguments()["project"].(string)

		context, err := s.FormatContext(project)
		if err != nil {
			return mcp.NewToolResultError("Failed to get context: " + err.Error()), nil
		}

		if context == "" {
			return mcp.NewToolResultText("No previous session memories found."), nil
		}

		stats, _ := s.Stats()
		var projects string
		if len(stats.Projects) > 0 {
			projects = strings.Join(stats.Projects, ", ")
		} else {
			projects = "none"
		}

		result := fmt.Sprintf("%s\n---\nMemory stats: %d sessions, %d observations across projects: %s",
			context, stats.TotalSessions, stats.TotalObservations, projects)

		return mcp.NewToolResultText(result), nil
	}
}

func handleStats(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		stats, err := s.Stats()
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

		return mcp.NewToolResultText(result), nil
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

		return mcp.NewToolResultText(b.String()), nil
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

		project := ""
		if obs.Project != nil {
			project = fmt.Sprintf("\nProject: %s", *obs.Project)
		}
		toolName := ""
		if obs.ToolName != nil {
			toolName = fmt.Sprintf("\nTool: %s", *obs.ToolName)
		}

		result := fmt.Sprintf("#%d [%s] %s\n%s\nSession: %s%s%s\nCreated: %s",
			obs.ID, obs.Type, obs.Title,
			obs.Content,
			obs.SessionID, project, toolName,
			obs.CreatedAt,
		)

		return mcp.NewToolResultText(result), nil
	}
}

func handleSessionSummary(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, _ := req.GetArguments()["content"].(string)
		sessionID, _ := req.GetArguments()["session_id"].(string)
		project, _ := req.GetArguments()["project"].(string)

		if sessionID == "" {
			sessionID = "manual-save"
		}

		// Ensure the session exists
		s.CreateSession(sessionID, project, "")

		_, err := s.AddObservation(store.AddObservationParams{
			SessionID: sessionID,
			Type:      "session_summary",
			Title:     fmt.Sprintf("Session summary: %s", project),
			Content:   content,
			Project:   project,
		})
		if err != nil {
			return mcp.NewToolResultError("Failed to save session summary: " + err.Error()), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Session summary saved for project %q", project)), nil
	}
}

func handleSessionStart(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, _ := req.GetArguments()["id"].(string)
		project, _ := req.GetArguments()["project"].(string)
		directory, _ := req.GetArguments()["directory"].(string)

		if err := s.CreateSession(id, project, directory); err != nil {
			return mcp.NewToolResultError("Failed to start session: " + err.Error()), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Session %q started for project %q", id, project)), nil
	}
}

func handleSessionEnd(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, _ := req.GetArguments()["id"].(string)
		summary, _ := req.GetArguments()["summary"].(string)

		if err := s.EndSession(id, summary); err != nil {
			return mcp.NewToolResultError("Failed to end session: " + err.Error()), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Session %q completed", id)), nil
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func intArg(req mcp.CallToolRequest, key string, defaultVal int) int {
	v, ok := req.GetArguments()[key].(float64)
	if !ok {
		return defaultVal
	}
	return int(v)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
