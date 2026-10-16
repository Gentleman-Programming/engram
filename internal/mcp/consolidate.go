package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Gentleman-Programming/engram/v3/internal/agentops"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// MCP JSON numbers must round-trip exactly before becoming observation IDs.
const consolidateMaxJSONID = 9007199254740991

func registerConsolidate(srv *server.MCPServer, s *store.Store, allowlist map[string]bool, queue *writeQueue) {
	if !shouldRegister("mem_consolidate", allowlist) {
		return
	}
	srv.AddTool(mcp.NewTool("mem_consolidate",
		mcp.WithDescription("Atomically consolidate at least two live observations owned by expected_project. Preview by default without writes; inspect the returned plan, then repeat the exact arguments with confirm=true and its opaque fingerprint. Drift refuses without writes and returns a new preview. Never automatically confirm a replacement plan or retry an uncertain write. Session/scope are inherited; this guarantees local atomicity, not remote visibility."),
		mcp.WithDeferLoading(true), mcp.WithTitleAnnotation("Consolidate Memories"),
		mcp.WithReadOnlyHintAnnotation(false), mcp.WithDestructiveHintAnnotation(true), mcp.WithIdempotentHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithArray("ids_to_delete", mcp.Required(), mcp.Description("At least two distinct positive integer observation IDs, each at most 9007199254740991"), mcp.Items(map[string]any{"type": "integer"})),
		mcp.WithString("new_title", mcp.Required(), mcp.Description("Non-empty consolidated survivor title")),
		mcp.WithString("new_content", mcp.Required(), mcp.Description("Non-empty summary; store appends the provenance footer")),
		mcp.WithString("expected_project", mcp.Required(), mcp.Description("Explicit expected canonical owner of every source, not a target-project override")),
		mcp.WithString("type", mcp.Description("Optional survivor type; defaults to newest source")),
		mcp.WithString("topic_key", mcp.Description("Optional explicit survivor topic key")),
		mcp.WithBoolean("confirm", mcp.Description("Default false: preview only; true requires the preview fingerprint")),
		mcp.WithString("fingerprint", mcp.Description("Opaque preview fingerprint, required when confirm=true")),
	), queuedWriteHandler(queue, handleConsolidate(s)))
}

func parseConsolidateArguments(args map[string]any) (store.ConsolidateParams, error) {
	return agentops.ParseConsolidate(args)
}

func consolidationPlanJSON(plan *store.ConsolidatePlan) map[string]any {
	return agentops.ConsolidationPlanJSON(plan)
}

func consolidationToolError(code string, err error, plan *store.ConsolidatePlan) *mcp.CallToolResult {
	body := map[string]any{"error": map[string]string{"code": code, "message": err.Error()}}
	if plan != nil {
		body["plan"] = consolidationPlanJSON(plan)
	}
	raw, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		return mcp.NewToolResultError("Failed to encode consolidation refusal: " + marshalErr.Error())
	}
	return mcp.NewToolResultError(string(raw))
}

func handleConsolidate(s *store.Store) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		params, err := parseConsolidateArguments(req.GetArguments())
		if err != nil {
			return consolidationToolError("invalid_arguments", err, nil), nil
		}
		result, err := s.ConsolidateObservations(params)
		if err != nil {
			code := "consolidation_refused"
			var plan *store.ConsolidatePlan
			switch {
			case errors.Is(err, store.ErrConsolidateFingerprintMismatch):
				code = "fingerprint_mismatch"
				if result != nil {
					plan = result.Plan
				}
			case errors.Is(err, store.ErrObservationProjectMismatch):
				code = "project_mismatch"
			case errors.Is(err, store.ErrExpectedProjectRequired):
				code = "invalid_arguments"
			}
			return consolidationToolError(code, err, plan), nil
		}
		body := map[string]any{"status": "preview", "plan": consolidationPlanJSON(result.Plan)}
		if result.Survivor != nil {
			body["status"] = "consolidated"
			body["survivor"] = result.Survivor
			body["soft_deleted_ids"] = result.Plan.IDs
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return mcp.NewToolResultError("Failed to encode consolidation result; do not retry an uncertain write: " + err.Error()), nil
		}
		return mcp.NewToolResultText(string(raw)), nil
	}
}
