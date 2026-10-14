package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/agentops"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// decodeWorkflow accepts exactly one JSON object; no trailing write instructions.
func decodeWorkflow(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	var args map[string]any
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&args); err != nil || args == nil {
		jsonErrorWithFields(w, 400, "body must be a JSON object", map[string]any{"code": "invalid_arguments"})
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		jsonErrorWithFields(w, 400, "body must contain exactly one JSON object", map[string]any{"code": "invalid_arguments"})
		return nil, false
	}
	return args, true
}

func (s *Server) handleConsolidateObservations(w http.ResponseWriter, r *http.Request) {
	args, ok := decodeWorkflow(w, r)
	if !ok {
		return
	}
	params, err := agentops.ParseConsolidate(args)
	if err != nil {
		jsonErrorWithFields(w, 400, err.Error(), map[string]any{"code": "invalid_arguments"})
		return
	}
	result, err := s.store.ConsolidateObservations(params)
	if err != nil {
		status, code := 400, "consolidation_refused"
		fields := map[string]any{}
		switch {
		case errors.Is(err, store.ErrConsolidateFingerprintMismatch):
			status, code = 409, "fingerprint_mismatch"
			if result != nil {
				fields["plan"] = agentops.ConsolidationPlanJSON(result.Plan)
			}
		case errors.Is(err, store.ErrObservationProjectMismatch):
			status, code = 403, "project_mismatch"
		case errors.Is(err, store.ErrExpectedProjectRequired):
			code = "invalid_arguments"
		}
		fields["code"] = code
		jsonErrorWithFields(w, status, err.Error(), fields)
		return
	}
	body := map[string]any{"status": "preview", "plan": agentops.ConsolidationPlanJSON(result.Plan)}
	status := http.StatusOK
	if result.Survivor != nil {
		body["status"] = "consolidated"
		body["survivor"] = result.Survivor
		body["soft_deleted_ids"] = result.Plan.IDs
		status = http.StatusCreated
		s.notifyWrite()
	}
	jsonResponse(w, status, body)
}

func (s *Server) handleJudgeMany(w http.ResponseWriter, r *http.Request) {
	args, ok := decodeWorkflow(w, r)
	if !ok {
		return
	}
	items, session, err := agentops.JudgmentBatch(args)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	outcomes := make([]map[string]any, 0, len(items))
	for index, raw := range items {
		out := map[string]any{"index": index, "judgment_id": nil, "status": "error"}
		item, valid := raw.(map[string]any)
		if valid {
			if id, ok := item["judgment_id"].(string); ok {
				out["judgment_id"] = id
			}
		}
		message := agentops.JudgmentItemError(raw)
		if message == "" {
			// A shared session replaces per-item provenance exactly as in MCP.
			item["session_id"] = session
			params, err := agentops.JudgeParams(item)
			if err != nil {
				message = err.Error()
			} else {
				relation, err := s.store.JudgeRelation(params)
				if err != nil {
					message = err.Error()
				} else {
					out["status"] = "success"
					out["relation"] = relation
					out["judgment_status"] = relation.JudgmentStatus
					s.notifyWrite()
				}
			}
		}
		if message != "" {
			out["error"] = message
		}
		outcomes = append(outcomes, out)
	}
	jsonResponse(w, 200, map[string]any{"outcomes": outcomes})
}

func (s *Server) handleFinalizeSession(w http.ResponseWriter, r *http.Request) {
	args, ok := decodeWorkflow(w, r)
	if !ok {
		return
	}
	for key := range args {
		if key != "project" && key != "summary" {
			jsonErrorWithFields(w, 400, "unsupported argument "+key, map[string]any{"code": "invalid_arguments"})
			return
		}
	}
	project, projectOK := args["project"].(string)
	summary, summaryOK := args["summary"].(string)
	if !projectOK || !summaryOK || strings.TrimSpace(project) == "" || strings.TrimSpace(summary) == "" {
		jsonErrorWithFields(w, 400, "project and summary must be non-empty strings", map[string]any{"code": "invalid_arguments"})
		return
	}
	sessionID := r.PathValue("id")
	result, err := s.store.FinalizeSession(store.FinalizeSessionParams{SessionID: sessionID, Project: project, Summary: summary})
	if err != nil {
		status, code := 400, "session_finalize_failed"
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			status, code = 404, "unknown_session"
		case errors.Is(err, store.ErrSessionOwnershipMismatch):
			status, code = 403, "session_project_mismatch"
		case errors.Is(err, store.ErrSessionAlreadyEnded):
			status, code = 409, "session_already_ended"
		case errors.Is(err, store.ErrObservationContentRequired), errors.Is(err, store.ErrProjectRequired):
			code = "invalid_arguments"
		}
		jsonErrorWithFields(w, status, err.Error(), map[string]any{"code": code, "session_id": sessionID})
		return
	}
	state := "finalized"
	if result.AlreadyFinalized {
		state = "already_finalized"
	} else {
		s.notifyWrite()
	}
	jsonResponse(w, 200, map[string]any{"session_id": sessionID, "state": state, "summary_id": result.SummaryID, "summary_sync_id": result.SummarySyncID, "already_finalized": result.AlreadyFinalized, "judgment_required": false})
}
