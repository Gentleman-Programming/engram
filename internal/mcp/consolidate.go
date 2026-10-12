package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

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
	p := store.ConsolidateParams{}
	for key := range args {
		switch key {
		case "ids_to_delete", "new_title", "new_content", "expected_project", "type", "topic_key", "confirm", "fingerprint":
		default:
			return p, fmt.Errorf("unsupported argument %q; source project/session/scope cannot be overridden", key)
		}
	}
	required := make(map[string]string, 3)
	for _, key := range []string{"new_title", "new_content", "expected_project"} {
		v, ok := args[key].(string)
		if !ok || strings.TrimSpace(v) == "" {
			return p, fmt.Errorf("%s must be a non-empty string", key)
		}
		required[key] = v
	}
	p.Overrides.Title = required["new_title"]
	p.Overrides.Content = required["new_content"]
	owner := required["expected_project"]
	p.ExpectedProject = &owner
	ids, ok := args["ids_to_delete"].([]any)
	if !ok || len(ids) < 2 {
		return p, errors.New("ids_to_delete must be an array of at least two positive integer IDs")
	}
	for _, raw := range ids {
		n, ok := raw.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n > consolidateMaxJSONID || n != math.Trunc(n) {
			return p, errors.New("ids_to_delete must contain positive exact JSON integers")
		}
		p.IDs = append(p.IDs, int64(n))
	}
	optional := map[string]*string{"type": &p.Overrides.Type, "topic_key": &p.Overrides.TopicKey, "fingerprint": &p.Fingerprint}
	for key, target := range optional {
		if raw, exists := args[key]; exists {
			v, ok := raw.(string)
			if !ok {
				return p, fmt.Errorf("%s must be a string", key)
			}
			*target = v
		}
	}
	if raw, exists := args["confirm"]; exists {
		v, ok := raw.(bool)
		if !ok {
			return p, errors.New("confirm must be a boolean")
		}
		p.Confirm = v
	}
	if p.Confirm && strings.TrimSpace(p.Fingerprint) == "" {
		return p, errors.New("fingerprint is required when confirm is true")
	}
	return p, nil
}

func consolidationPlanJSON(plan *store.ConsolidatePlan) map[string]any {
	if plan == nil {
		return nil
	}
	s := plan.Survivor
	return map[string]any{"source_ids": plan.IDs, "sources": plan.Sources, "project": plan.Project, "fingerprint": plan.Fingerprint, "survivor": map[string]any{"session_id": s.SessionID, "title": s.Title, "content": s.Content, "type": s.Type, "scope": s.Scope, "project": s.Project, "topic_key": s.TopicKey}}
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
