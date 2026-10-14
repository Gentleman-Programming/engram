package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// RuntimeRouteDescriptor describes a server handler contract, not a Pi input schema.
type RuntimeRouteDescriptor struct {
	Method         string `json:"method"`
	Path           string `json:"path"`
	ServerContract string `json:"server_contract"`
	ConfiguredAuth bool   `json:"configured_auth,omitempty"`
}

// RuntimeOperationDescriptor keeps adapter-local behavior separate from Go handlers.
type RuntimeOperationDescriptor struct {
	Kind     string                   `json:"kind"`
	Bindings []RuntimeRouteDescriptor `json:"bindings,omitempty"`
}

// RuntimeContract is the stable projection generated for the Pi HTTP adapter.
// It does not certify TypeBox/MCP schema equivalence or general adapter parity.
type RuntimeContract struct {
	ContractID       string                                `json:"contract_id"`
	Revision         int                                   `json:"revision"`
	Status           string                                `json:"status"`
	Digest           string                                `json:"digest,omitempty"`
	Operations       map[string]RuntimeOperationDescriptor `json:"operations"`
	SupportingRoutes map[string]RuntimeRouteDescriptor     `json:"supporting_routes"`
}

type runtimeRoute struct {
	id      string
	pattern string
	handler http.HandlerFunc
	auth    bool
}

// This registry supplies both actual mux bindings and advertised route descriptors.
// Preserve the existing wrappers: declaring a route must not weaken its auth policy.
func (s *Server) runtimeRoutes() []runtimeRoute {
	return []runtimeRoute{
		{"session-create", "POST /sessions", s.handleCreateSession, false},
		{"session-end", "POST /sessions/{id}/end", s.handleEndSession, false},
		{"runtime-session-resolve", "POST /runtime-sessions/resolve", s.handleRuntimeSession, false},
		{"runtime-session-end", "POST /runtime-sessions/end", s.handleRuntimeSession, false},
		{"observation-add", "POST /observations", s.handleAddObservation, false},
		{"observation-passive", "POST /observations/passive", s.handlePassiveCapture, false},
		{"observation-save-result", "GET /observations/save-result", s.handleGetObservationSaveResult, false},
		{"observation-pin", "PUT /observations/{id}/pin", s.handlePinObservation, false},
		{"observation-unpin", "DELETE /observations/{id}/pin", s.handleUnpinObservation, false},
		{"observation-update", "PATCH /observations/{id}", s.handleUpdateObservation, false},
		{"observation-delete", "DELETE /observations/{id}", s.handleDeleteObservation, true},
		{"search", "GET /search", s.handleSearch, false},
		{"timeline", "GET /timeline", s.handleTimeline, false},
		{"observation-get", "GET /observations/{id}", s.handleGetObservation, false},
		{"review-list", "GET /review", s.handleReviewList, false},
		{"review-mark", "POST /review/mark_reviewed", s.handleReviewMarkReviewed, false},
		{"prompt-add", "POST /prompts", s.handleAddPrompt, false},
		{"prompt-capture-decision", "POST /prompts/capture-decision", s.handlePromptCaptureDecision, false},
		{"context", "GET /context", s.handleContext, false},
		{"compaction-context", "GET /context/compaction", s.handleCompactionContext, false},
		{"stats", "GET /stats", s.handleStats, false},
		{"projects", "GET /projects", s.handleListProjects, false},
		{"doctor", "GET /doctor", s.handleDoctor, false},
		{"caller-binding", "POST /doctor/caller-binding", s.handleCallerBinding, false},
		{"current-project", "GET /project/current", s.handleCurrentProject, false},
		{"conflict-judge", "POST /conflicts/judge", s.handleJudgeConflict, false},
		{"conflict-compare", "POST /conflicts/compare", s.handleCompareMemories, false},
	}
}

func (s *Server) registerRuntimeRoutes() {
	for _, route := range s.runtimeRoutes() {
		handler := route.handler
		if route.auth {
			handler = requireAuth(handler)
		}
		s.mux.HandleFunc(route.pattern, handler)
	}
	s.mux.HandleFunc("GET /runtime-contract", s.handleRuntimeContract)
}

// RuntimeContractProjection derives stable metadata without opening a store,
// starting a server, or including the dynamic database/server instance identity.
func RuntimeContractProjection() (RuntimeContract, error) {
	routes := make(map[string]RuntimeRouteDescriptor)
	for _, route := range (&Server{}).runtimeRoutes() {
		method, path, ok := strings.Cut(route.pattern, " ")
		if !ok {
			return RuntimeContract{}, fmt.Errorf("invalid runtime route %q", route.id)
		}
		if _, exists := routes[route.id]; exists {
			return RuntimeContract{}, fmt.Errorf("duplicate runtime route %q", route.id)
		}
		routes[route.id] = RuntimeRouteDescriptor{method, path, route.id + "/v1", route.auth}
	}
	// Aliases and composite tools name their real server bindings. Their client
	// formatting/fallback algorithms are deliberately not certified by this map.
	bindings := map[string][]string{
		"mem_search": {"search"}, "mem_context": {"context"},
		"mem_stats": {"stats"}, "mem_timeline": {"timeline"},
		"mem_get_observation": {"observation-get"},
		"mem_save":            {"observation-add", "observation-save-result"},
		"mem_session_summary": {"observation-add"},
		"mem_update":          {"observation-update"}, "mem_delete": {"observation-delete"},
		"mem_save_prompt": {"prompt-add"}, "mem_session_start": {"session-create"},
		"mem_session_end": {"session-end"}, "mem_current_project": {"current-project"},
		"mem_doctor":          {"doctor", "caller-binding"},
		"mem_capture_passive": {"observation-passive"},
		"mem_review":          {"review-list", "review-mark"},
		"mem_judge":           {"conflict-judge"}, "mem_compare": {"conflict-compare"},
		"mem_list_projects": {"projects"}, "mem_pin": {"observation-pin"},
		"mem_unpin": {"observation-unpin"},
	}
	contract := RuntimeContract{
		ContractID: "engram.pi-http-runtime/v1", Revision: 1, Status: "pi-http-server-contract",
		Operations:       make(map[string]RuntimeOperationDescriptor),
		SupportingRoutes: make(map[string]RuntimeRouteDescriptor),
	}
	for name, ids := range bindings {
		op := RuntimeOperationDescriptor{Kind: "http"}
		for _, id := range ids {
			route, ok := routes[id]
			if !ok {
				return RuntimeContract{}, fmt.Errorf("operation %q references missing route %q", name, id)
			}
			op.Bindings = append(op.Bindings, route)
		}
		contract.Operations[name] = op
	}
	contract.Operations["mem_suggest_topic_key"] = RuntimeOperationDescriptor{Kind: "client-local"}
	for _, id := range []string{"runtime-session-resolve", "runtime-session-end", "prompt-capture-decision", "compaction-context"} {
		contract.SupportingRoutes[id] = routes[id]
	}
	unsigned, err := json.Marshal(contract)
	if err != nil {
		return RuntimeContract{}, fmt.Errorf("encode runtime contract: %w", err)
	}
	sum := sha256.Sum256(unsigned)
	contract.Digest = hex.EncodeToString(sum[:])
	return contract, nil
}

func (s *Server) handleRuntimeContract(w http.ResponseWriter, r *http.Request) {
	contract, err := RuntimeContractProjection()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "runtime contract unavailable")
		return
	}
	jsonResponse(w, http.StatusOK, struct {
		RuntimeContract
		InstanceID string `json:"instance_id"`
	}{contract, s.instanceID})
}
