package mcp

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerSessionFinalize(srv *server.MCPServer, s *store.Store, cfg MCPConfig, activity *SessionActivity, queue *writeQueue) {
	srv.AddTool(mcp.NewTool("mem_session_finalize",
		mcp.WithTitleAnnotation("Finalize Session"),
		mcp.WithDescription("Atomically save a caller-authored structured summary and end an existing exact session. Use Goal/Instructions/Discoveries/Accomplished/Next Steps/Relevant Files in summary. Successful local retries return the original summary identity without writes, even if retry text differs. Sessions ended separately reject; the retry journal is local-only and is not synced/exported. Preserves the legacy free-text session summary."),
		mcp.WithDeferLoading(true),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Exact existing session ID; never implicitly created.")),
		mcp.WithString("summary", mcp.Required(), mcp.Description("Caller-authored structured summary; nonempty.")),
		mcp.WithString("project", mcp.Description("Optional explicit project, validated using summary project resolution and exact session ownership.")),
		mcp.WithString("project_choice_reason", mcp.Description("user_selected_after_ambiguous_project only after explicit user choice.")),
		mcp.WithString("recovery_token", mcp.Description("Short-lived ambiguous-project recovery token bound to this session/context/choice.")),
	), queuedWriteHandler(queue, handleSessionFinalize(s, cfg, activity)))
}

func handleSessionFinalize(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		for _, key := range []string{"session_id", "summary", "project", "project_choice_reason", "recovery_token"} {
			if value, present := args[key]; present {
				if _, ok := value.(string); !ok {
					return errorWithMeta("invalid_arguments", key+" must be a string", nil), nil
				}
			}
		}
		sessionID, _ := args["session_id"].(string)
		summary, _ := args["summary"].(string)
		if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(summary) == "" {
			return errorWithMeta("invalid_arguments", "session_id and summary are required and must be nonempty", nil), nil
		}
		// Check the exact identity first: shared resolution trims IDs and can create
		// an unknown session after ambiguity recovery, neither is allowed here.
		if _, err := s.GetSession(sessionID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errorWithMeta("unknown_session", "session_id must identify an existing exact session", nil), nil
			}
			return errorWithMeta("session_finalize_failed", err.Error(), nil), nil
		}
		choice, _ := args["project"].(string)
		_, explicit := args["project"]
		reason, _ := args["project_choice_reason"].(string)
		token, _ := args["recovery_token"].(string)
		validateToken := func(res projectpkg.DetectionResult, project string) (bool, bool) {
			if strings.TrimSpace(token) == "" {
				return false, false
			}
			return true, activity.ValidateAmbiguousProjectRecoveryToken(sessionID, token, strings.TrimSpace(project), res.AvailableProjects, res.Path)
		}
		resolved, err := resolveSaveWriteProjectForSession(s, choice, explicit, reason, sessionID, validateToken, false)
		if err != nil {
			return writeProjectErrorResult(activity, sessionID, resolved, err), nil
		}
		result, err := s.FinalizeSession(store.FinalizeSessionParams{SessionID: sessionID, Project: resolved.Project, Summary: summary})
		if err != nil {
			code := "session_finalize_failed"
			switch {
			case errors.Is(err, store.ErrSessionAlreadyEnded):
				code = "session_already_ended"
			case errors.Is(err, store.ErrSessionNotFound):
				code = "unknown_session"
			case errors.Is(err, store.ErrSessionOwnershipMismatch):
				code = "session_project_mismatch"
			case errors.Is(err, store.ErrObservationContentRequired):
				code = "invalid_arguments"
			}
			response := errorWithMeta(code, err.Error(), nil)
			addErrorMetadata(response, map[string]any{"session_id": sessionID})
			return response, nil
		}
		state := "finalized"
		if result.AlreadyFinalized {
			state = "already_finalized"
		} else {
			activity.RecordProjectSave(resolved.Project)
		}
		return respondWithProject(resolved, "Session finalization completed", map[string]any{
			"session_id": sessionID, "state": state, "summary_id": result.SummaryID, "summary_sync_id": result.SummarySyncID,
			"already_finalized": result.AlreadyFinalized, "judgment_required": false,
		}), nil
	}
}
