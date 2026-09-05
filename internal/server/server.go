// Package server provides the HTTP API for Engram.
//
// This is how external clients (OpenCode plugin, Claude Code hooks,
// any agent) communicate with the memory engine. Simple JSON REST API.
package server

import (
	"crypto/hmac"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/diagnostic"
	projectpkg "github.com/Gentleman-Programming/engram/v2/internal/project"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

var loadServerStats = func(s *store.Store) (*store.Stats, error) {
	return s.Stats()
}

// SyncStatusProvider returns the current sync status. This is implemented
// by autosync.Manager and injected from cmd/engram/main.go.
type SyncStatusProvider interface {
	Status(project string) SyncStatus
}

// SyncStatus mirrors autosync.Status to avoid a direct import cycle.
type SyncStatus struct {
	Enabled              bool       `json:"enabled"`
	Phase                string     `json:"phase"`
	LastError            string     `json:"last_error,omitempty"`
	ConsecutiveFailures  int        `json:"consecutive_failures"`
	BackoffUntil         *time.Time `json:"backoff_until,omitempty"`
	LastSyncAt           *time.Time `json:"last_sync_at,omitempty"`
	ReasonCode           string     `json:"reason_code,omitempty"`
	ReasonMessage        string     `json:"reason_message,omitempty"`
	UpgradeStage         string     `json:"upgrade_stage,omitempty"`
	UpgradeReasonCode    string     `json:"upgrade_reason_code,omitempty"`
	UpgradeReasonMessage string     `json:"upgrade_reason_message,omitempty"`
	// Phase 2: deferred relation retry counts surfaced from sync_apply_deferred.
	DeferredCount int `json:"deferred_count"`
	DeadCount     int `json:"dead_count"`
}

// SemanticRunnerFactory is a function that resolves a store.SemanticRunner by name.
// It is injected from cmd/engram/main.go so that internal/server does not depend on
// internal/llm directly (preserving the import-cycle boundary).
type SemanticRunnerFactory func(name string) (store.SemanticRunner, error)

// SemanticPromptBuilder constructs the LLM prompt for a given pair of observation
// snippets. Injected from cmd/engram/main.go alongside SemanticRunnerFactory.
type SemanticPromptBuilder func(a, b store.ObservationSnippet) string

type Server struct {
	store      *store.Store
	mux        *http.ServeMux
	port       int
	listen     func(network, address string) (net.Listener, error)
	serve      func(net.Listener, http.Handler) error
	onWrite    func() // called after successful local writes (for autosync notification)
	syncStatus SyncStatusProvider

	// runnerFactory resolves a SemanticRunner by CLI name (read from ENGRAM_AGENT_CLI).
	// When nil, semantic=true requests fail with 500.
	runnerFactory SemanticRunnerFactory
	// promptBuilder constructs LLM prompts for semantic scan pairs.
	// When nil and semantic=true, a no-op builder is used (returns empty string).
	promptBuilder SemanticPromptBuilder
}

func New(s *store.Store, port int) *Server {
	srv := &Server{store: s, port: port, listen: net.Listen, serve: http.Serve}
	srv.mux = http.NewServeMux()
	srv.routes()
	return srv
}

// SetOnWrite configures a callback invoked after every successful local write.
// This is used to notify autosync.Manager via NotifyDirty().
func (s *Server) SetOnWrite(fn func()) {
	s.onWrite = fn
}

// SetSyncStatus configures the sync status provider for the /sync/status endpoint.
func (s *Server) SetSyncStatus(provider SyncStatusProvider) {
	s.syncStatus = provider
}

// SetRunnerFactory configures the semantic runner factory used by POST /conflicts/scan
// when semantic=true. When not set, semantic=true requests fail with 500.
func (s *Server) SetRunnerFactory(fn SemanticRunnerFactory) {
	s.runnerFactory = fn
}

// SetPromptBuilder configures the LLM prompt builder for semantic scan pairs.
// When not set, an empty-string builder is used (valid for tests, not production).
func (s *Server) SetPromptBuilder(fn SemanticPromptBuilder) {
	s.promptBuilder = fn
}

// notifyWrite calls the onWrite callback if configured (best-effort, non-blocking).
func (s *Server) notifyWrite() {
	if s.onWrite != nil {
		s.onWrite()
	}
}

// requireAuth wraps h with optional Bearer-token authentication.
//
// When the ENGRAM_HTTP_TOKEN environment variable is set, every request to the
// wrapped handler must supply a matching "Authorization: Bearer <token>" header.
// Comparison is constant-time to prevent timing attacks. When the env var is
// unset the handler is called directly — zero-config is preserved.
//
// The token is read from the environment on every request so that the server
// does not need to restart when the variable changes.
func requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := os.Getenv("ENGRAM_HTTP_TOKEN")
		if token == "" {
			// No token configured → open access (zero-config default).
			h(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="engram"`)
			jsonError(w, http.StatusUnauthorized, "authorization required")
			return
		}

		provided := authHeader[len(prefix):]
		// Use constant-time comparison via hmac.Equal to prevent timing attacks.
		if !hmac.Equal([]byte(provided), []byte(token)) {
			// Extra defense: also absorb timing via subtle.ConstantTimeCompare (same algo).
			_ = subtle.ConstantTimeCompare([]byte(provided), []byte(token))
			w.Header().Set("WWW-Authenticate", `Bearer realm="engram"`)
			jsonError(w, http.StatusUnauthorized, "invalid token")
			return
		}

		h(w, r)
	}
}

// writeOwnershipError reports an ownership failure the client can act on, and
// names the concrete repair. The repair is a CLI path that reaches the store
// directly, so it stays available in a zero-config install where the HTTP
// rescue endpoint is not served. Returns false when err is not an ownership
// failure and the caller should keep its own handling.
func writeOwnershipError(w http.ResponseWriter, sessionID string, err error) bool {
	code := ""
	switch {
	case errors.Is(err, store.ErrProjectOwnershipAmbiguous):
		code = "project_ownership_ambiguous"
	case errors.Is(err, store.ErrProjectRequired):
		code = "project_ownership_required"
	default:
		return false
	}
	remedy := store.RescueOwnershipCommand + " --project <name>"
	if strings.TrimSpace(sessionID) != "" {
		remedy += " --session " + sessionID
	}
	jsonErrorWithFields(w, http.StatusConflict, err.Error(), map[string]any{
		"code":   code,
		"remedy": remedy,
	})
	return true
}

// requireConfiguredAuth rejects requests when the server token is not configured.
// It is reserved for endpoints that must never use the zero-config auth default.
// It is not the only way to repair ownership: store.RescueOwnershipCommand does
// the same work locally without any server token.
func requireConfiguredAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("ENGRAM_HTTP_TOKEN") == "" {
			jsonError(w, http.StatusServiceUnavailable, "server authorization is not configured")
			return
		}
		requireAuth(h)(w, r)
	}
}

func (s *Server) Start() error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	listenFn := s.listen
	if listenFn == nil {
		listenFn = net.Listen
	}
	serveFn := s.serve
	if serveFn == nil {
		serveFn = http.Serve
	}

	ln, err := listenFn("tcp", addr)
	if err != nil {
		return fmt.Errorf("engram server: listen %s: %w", addr, err)
	}
	log.Printf("[engram] HTTP server listening on %s", addr)
	return serveFn(ln, s.mux)
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)

	// Sessions
	s.mux.HandleFunc("POST /sessions", s.handleCreateSession)
	s.mux.HandleFunc("POST /sessions/{id}/end", s.handleEndSession)
	s.mux.HandleFunc("GET /sessions/recent", s.handleRecentSessions)
	s.mux.HandleFunc("GET /sessions/{id}", s.handleGetSession)
	s.mux.HandleFunc("DELETE /sessions/{id}", requireAuth(s.handleDeleteSession))

	// Observations
	s.mux.HandleFunc("POST /observations", s.handleAddObservation)
	s.mux.HandleFunc("GET /observations", s.handleListObservations)
	s.mux.HandleFunc("POST /observations/passive", s.handlePassiveCapture)
	s.mux.HandleFunc("GET /observations/recent", s.handleRecentObservations)
	s.mux.HandleFunc("PATCH /observations/{id}", s.handleUpdateObservation)
	s.mux.HandleFunc("DELETE /observations/{id}", requireAuth(s.handleDeleteObservation))

	// Search
	s.mux.HandleFunc("GET /search", s.handleSearch)

	// Timeline
	s.mux.HandleFunc("GET /timeline", s.handleTimeline)
	s.mux.HandleFunc("GET /observations/{id}", s.handleGetObservation)

	// Lifecycle review
	s.mux.HandleFunc("GET /review", s.handleReviewList)
	s.mux.HandleFunc("POST /review/mark_reviewed", s.handleReviewMarkReviewed)

	// Prompts
	s.mux.HandleFunc("POST /prompts", s.handleAddPrompt)
	s.mux.HandleFunc("GET /prompts/recent", s.handleRecentPrompts)
	s.mux.HandleFunc("GET /prompts/search", s.handleSearchPrompts)
	s.mux.HandleFunc("DELETE /prompts/{id}", requireAuth(s.handleDeletePrompt))

	// Context
	s.mux.HandleFunc("GET /context", s.handleContext)
	s.mux.HandleFunc("GET /context/compaction", s.handleCompactionContext)

	// Export / Import — sensitive: full data read and bulk mutation.
	s.mux.HandleFunc("GET /export", requireAuth(s.handleExport))
	s.mux.HandleFunc("POST /import", requireAuth(s.handleImport))

	// Stats / diagnostics
	s.mux.HandleFunc("GET /stats", s.handleStats)
	s.mux.HandleFunc("GET /doctor", s.handleDoctor)

	// Project detection / ownership rescue
	s.mux.HandleFunc("GET /project/current", s.handleCurrentProject)
	s.mux.HandleFunc("POST /projects/rescue-ownership", requireConfiguredAuth(s.handleRescueProjectOwnership))
	// Deprecated compatibility alias for older clients.
	s.mux.HandleFunc("POST /projects/migrate", requireConfiguredAuth(s.handleRescueProjectOwnership))

	// Sync status (degraded-state visibility for autosync)
	s.mux.HandleFunc("GET /sync/status", s.handleSyncStatus)

	// Conflicts — CRITICAL ORDER: literals before wildcard (Go 1.22 mux panics on overlap)
	s.mux.HandleFunc("GET /conflicts", s.handleListConflicts)
	s.mux.HandleFunc("GET /conflicts/stats", s.handleConflictsStats)
	s.mux.HandleFunc("GET /conflicts/deferred", s.handleListDeferred)
	s.mux.HandleFunc("POST /conflicts/scan", s.handleScanConflicts)
	s.mux.HandleFunc("POST /conflicts/judge", s.handleJudgeConflict)
	s.mux.HandleFunc("POST /conflicts/compare", s.handleCompareMemories)
	s.mux.HandleFunc("POST /conflicts/deferred/replay", s.handleReplayDeferred)
	s.mux.HandleFunc("GET /conflicts/{relation_id}", s.handleGetConflict)
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "engram",
		"version": "0.1.0",
	})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID            string `json:"id"`
		Project       string `json:"project"`
		Directory     string `json:"directory"`
		OwnershipMode string `json:"ownership_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if body.ID == "" || body.Project == "" {
		jsonError(w, http.StatusBadRequest, "id and project are required")
		return
	}

	mode := body.OwnershipMode
	if mode == "" {
		mode = store.SessionOwnershipShared
	}
	if err := s.store.CreateSessionWithOwnershipMode(body.ID, body.Project, projectpkg.RuntimeWorktreeDirectory(body.Directory), mode); err != nil {
		if errors.Is(err, store.ErrInvalidSessionOwnershipMode) {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.notifyWrite()
	jsonResponse(w, http.StatusCreated, map[string]string{"id": body.ID, "status": "created"})
}

func (s *Server) handleEndSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var body struct {
		Summary string `json:"summary"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	if err := s.store.EndSession(id, body.Summary); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.notifyWrite()
	jsonResponse(w, http.StatusOK, map[string]string{"id": id, "status": "completed"})
}

func (s *Server) handleRecentSessions(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	limit := queryInt(r, "limit", 5)

	sessions, err := s.store.RecentSessions(resolved.Project, limit)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, sessions)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.store.GetSession(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, http.StatusNotFound, "session not found")
			return
		}
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, session)
}

func (s *Server) handleAddObservation(w http.ResponseWriter, r *http.Request) {
	var body store.AddObservationParams
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	// Validate the title before the session lookup so a bad session or project
	// cannot mask the documented title-validation 400 (#459). A whitespace-only
	// title survives a raw `== ""` check, so it needs the shared predicate.
	if err := store.ValidateObservationTitle(body.Title); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.SessionID == "" || body.Content == "" {
		jsonError(w, http.StatusBadRequest, "session_id and content are required")
		return
	}
	if !s.validateSessionProject(w, body.SessionID, body.Project) {
		return
	}

	id, err := s.store.AddObservation(body)
	if err != nil {
		// A titleless or contentless observation is a client mistake, not a
		// server failure.
		switch {
		case errors.Is(err, store.ErrObservationTitleRequired),
			errors.Is(err, store.ErrObservationContentRequired):
			jsonError(w, http.StatusBadRequest, err.Error())
		case writeOwnershipError(w, body.SessionID, err):
		default:
			jsonError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	s.notifyWrite()
	jsonResponse(w, http.StatusCreated, map[string]any{"id": id, "status": "saved"})
}

func (s *Server) handlePassiveCapture(w http.ResponseWriter, r *http.Request) {
	var body store.PassiveCaptureParams
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if body.SessionID == "" {
		jsonError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if !s.validateSessionProject(w, body.SessionID, body.Project) {
		return
	}

	result, err := s.store.PassiveCapture(body)
	if err != nil {
		if writeOwnershipError(w, body.SessionID, err) {
			return
		}
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.notifyWrite()
	jsonResponse(w, http.StatusOK, result)
}

func (s *Server) handleListObservations(w http.ResponseWriter, r *http.Request) {
	sort := r.URL.Query().Get("sort")
	if sort != "" && sort != "created_at:desc" {
		jsonError(w, http.StatusBadRequest, "unsupported sort: use created_at:desc")
		return
	}

	s.handleRecentObservations(w, r)
}

func (s *Server) handleRecentObservations(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	scope := r.URL.Query().Get("scope")
	limit := queryInt(r, "limit", 20)

	obs, err := s.store.RecentObservations(resolved.Project, scope, limit)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, obs)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	query := params.Get("q")
	if query == "" {
		jsonError(w, http.StatusBadRequest, "q parameter is required")
		return
	}

	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}

	matchMode := params.Get("match_mode")
	if matchMode != "" && matchMode != "all" && matchMode != "any" {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid match_mode %q: must be \"all\" or \"any\"", matchMode))
		return
	}

	results, err := s.store.Search(query, store.SearchOptions{
		Type:      params.Get("type"),
		Project:   resolved.Project,
		Scope:     params.Get("scope"),
		Limit:     queryInt(r, "limit", 10),
		MatchMode: matchMode,
	})
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if results == nil {
		results = []store.SearchResult{}
	}

	jsonResponse(w, http.StatusOK, results)
}

func (s *Server) handleGetObservation(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid observation id")
		return
	}

	obs, err := s.store.GetObservation(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "observation not found")
		return
	}

	jsonResponse(w, http.StatusOK, obs)
}

func (s *Server) handleUpdateObservation(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid observation id")
		return
	}

	var body store.UpdateObservationParams
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	if body.Type == nil && body.Title == nil && body.Content == nil && body.Project == nil && body.Scope == nil && body.TopicKey == nil {
		jsonError(w, http.StatusBadRequest, "at least one field is required")
		return
	}

	obs, err := s.store.UpdateObservation(id, body)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrObservationTitleRequired),
			errors.Is(err, store.ErrObservationContentRequired):
			jsonError(w, http.StatusBadRequest, err.Error())
		default:
			jsonError(w, http.StatusNotFound, err.Error())
		}
		return
	}

	s.notifyWrite()
	jsonResponse(w, http.StatusOK, obs)
}

func (s *Server) handleDeleteObservation(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid observation id")
		return
	}

	hard := queryBool(r, "hard", false)
	if err := s.store.DeleteObservation(id, hard); err != nil {
		switch {
		case errors.Is(err, store.ErrObservationNotFound):
			jsonError(w, http.StatusNotFound, err.Error())
		default:
			jsonError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	s.notifyWrite()
	jsonResponse(w, http.StatusOK, map[string]any{
		"id":          id,
		"status":      "deleted",
		"hard_delete": hard,
	})
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("observation_id")
	if idStr == "" {
		jsonError(w, http.StatusBadRequest, "observation_id parameter is required")
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid observation_id")
		return
	}

	before := queryInt(r, "before", 5)
	after := queryInt(r, "after", 5)
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	focus, err := s.store.GetObservation(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "observation not found")
		return
	}
	if resolved.Project != "" && (focus.Project == nil || !strings.EqualFold(strings.TrimSpace(*focus.Project), resolved.Project)) {
		jsonError(w, http.StatusNotFound, "observation not found in resolved project")
		return
	}

	result, err := s.store.Timeline(id, before, after)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

func (s *Server) handleReviewList(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	limit := queryInt(r, "limit", 10)

	observations, err := s.store.ObservationsNeedingReview(resolved.Project, limit)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	structured := make([]map[string]any, 0, len(observations))
	for _, obs := range observations {
		structured = append(structured, reviewObservationPayload(obs))
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"observations": structured,
		"count":        len(structured),
	})
}

func (s *Server) handleReviewMarkReviewed(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ObservationID int64 `json:"observation_id"`
		ID            int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	id := body.ObservationID
	if id == 0 {
		id = body.ID
	}
	if id == 0 {
		jsonError(w, http.StatusBadRequest, "observation_id is required")
		return
	}
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	markReviewed := s.store.MarkReviewed
	if resolved.Project != "" {
		markReviewed = func(observationID int64) error {
			return s.store.MarkReviewedForProject(observationID, resolved.Project)
		}
	}
	if err := markReviewed(id); err != nil {
		if errors.Is(err, store.ErrObservationNotFound) {
			if resolved.Project != "" {
				jsonError(w, http.StatusNotFound, "observation not found in resolved project")
				return
			}
			jsonError(w, http.StatusNotFound, err.Error())
			return
		}
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	obs, err := s.store.GetObservation(id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "marked reviewed but failed to reload observation: "+err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, reviewObservationPayload(*obs))
}

func reviewObservationPayload(obs store.Observation) map[string]any {
	payload := map[string]any{
		"id":      obs.ID,
		"sync_id": obs.SyncID,
		"title":   obs.Title,
		"type":    obs.Type,
		"state":   obs.State(),
	}
	if obs.Project != nil {
		payload["project"] = *obs.Project
	}
	if obs.ReviewAfter != nil {
		payload["review_after"] = *obs.ReviewAfter
	}
	return payload
}

// ─── Prompts ─────────────────────────────────────────────────────────────────

func (s *Server) handleAddPrompt(w http.ResponseWriter, r *http.Request) {
	var body store.AddPromptParams
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if body.SessionID == "" || body.Content == "" {
		jsonError(w, http.StatusBadRequest, "session_id and content are required")
		return
	}
	if !s.validateSessionProject(w, body.SessionID, body.Project) {
		return
	}

	// When the client omits the project, derive it from the session so the
	// prompt is attributed correctly without the hook detecting it eagerly on
	// every message.
	if strings.TrimSpace(body.Project) == "" {
		if sess, err := s.store.GetSession(body.SessionID); err == nil {
			body.Project = sess.Project
		}
	}

	id, err := s.store.AddPrompt(body)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrPromptContentRequired):
			jsonError(w, http.StatusBadRequest, err.Error())
		case writeOwnershipError(w, body.SessionID, err):
		default:
			jsonError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	s.notifyWrite()
	jsonResponse(w, http.StatusCreated, map[string]any{"id": id, "status": "saved"})
}

func (s *Server) handleRecentPrompts(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	limit := queryInt(r, "limit", 20)

	prompts, err := s.store.RecentPrompts(resolved.Project, limit)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, prompts)
}

func (s *Server) handleSearchPrompts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		jsonError(w, http.StatusBadRequest, "q parameter is required")
		return
	}
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}

	prompts, err := s.store.SearchPrompts(
		query,
		resolved.Project,
		queryInt(r, "limit", 10),
	)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, prompts)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, http.StatusBadRequest, "session id is required")
		return
	}

	if err := s.store.DeleteSession(id); err != nil {
		switch {
		case errors.Is(err, store.ErrSessionDeleteBlocked):
			jsonError(w, http.StatusConflict, err.Error())
		case errors.Is(err, store.ErrSessionHasObservations):
			jsonError(w, http.StatusConflict, err.Error())
		case errors.Is(err, store.ErrSessionNotFound):
			jsonError(w, http.StatusNotFound, err.Error())
		default:
			jsonError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// local-only delete path: do not notify autosync.
	jsonResponse(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
}

func (s *Server) handleDeletePrompt(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid prompt id")
		return
	}

	if err := s.store.DeletePrompt(id); err != nil {
		if errors.Is(err, store.ErrPromptNotFound) {
			jsonError(w, http.StatusNotFound, err.Error())
			return
		}
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.notifyWrite()
	jsonResponse(w, http.StatusOK, map[string]any{"id": id, "status": "deleted"})
}

// ─── Export / Import ─────────────────────────────────────────────────────────

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}

	var data *store.ExportData
	if resolved.Project != "" {
		data, err = s.store.ExportProject(resolved.Project)
	} else {
		data, err = s.store.Export()
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=engram-export.json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(data)
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	// Limit body to 50MB
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "failed to read body: "+err.Error())
		return
	}

	var data store.ExportData
	if err := json.Unmarshal(body, &data); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	result, err := s.store.Import(&data)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.notifyWrite()
	jsonResponse(w, http.StatusOK, result)
}

// ─── Context ─────────────────────────────────────────────────────────────────

func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	scope := r.URL.Query().Get("scope")

	context, err := s.store.FormatContext(resolved.Project, scope)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"context": context})
}

func (s *Server) handleCompactionContext(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		jsonError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	context, err := s.store.FormatCompactionContext(sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, http.StatusNotFound, "session not found")
			return
		}
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"context": context})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	var stats *store.Stats
	if resolved.Project != "" {
		stats, err = s.store.StatsProject(resolved.Project)
	} else {
		stats, err = loadServerStats(s.store)
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, stats)
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	projectName := resolved.Project

	check := strings.TrimSpace(r.URL.Query().Get("check"))
	runner := diagnostic.NewRunner()
	scope := diagnostic.Scope{Store: s.store, Project: projectName, Now: time.Now()}
	var report diagnostic.Report
	if check != "" {
		report, err = runner.RunOne(r.Context(), scope, check)
	} else {
		report, err = runner.RunAll(r.Context(), scope)
	}
	if err != nil {
		report = diagnostic.ErrorReport(projectName, err)
	}

	jsonResponse(w, http.StatusOK, report)
}

// ─── Project Detection ───────────────────────────────────────────────────────

func (s *Server) handleCurrentProject(w http.ResponseWriter, r *http.Request) {
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	if cwd == "" {
		var cwdErr error
		cwd, cwdErr = os.Getwd()
		if cwdErr != nil {
			jsonError(w, http.StatusInternalServerError, "failed to detect cwd: "+cwdErr.Error())
			return
		}
	}
	res, err := projectpkg.Resolve(projectpkg.ResolutionOptions{
		Mode:      projectpkg.ResolutionCurrent,
		Directory: cwd,
	})
	if err != nil && !errors.Is(err, projectpkg.ErrAmbiguousProject) {
		s.writeProjectResolutionError(w, res, err)
		return
	}
	payload := map[string]any{
		"project":            res.Project,
		"project_source":     res.Source,
		"project_path":       res.Path,
		"cwd":                cwd,
		"available_projects": res.AvailableProjects,
	}
	if res.Warning != "" {
		payload["warning"] = res.Warning
	}
	if res.Error != nil {
		payload["error_hint"] = res.Error.Error()
	}

	jsonResponse(w, http.StatusOK, payload)
}

// resolveRequestProject applies the shared resolution modes to HTTP operations.
// Current-project reads require known explicit/process overrides, while cwd
// detection remains usable before a project has any stored memories.
func (s *Server) resolveRequestProject(r *http.Request, mode projectpkg.ResolutionMode, requireKnownOverrides bool) (projectpkg.DetectionResult, error) {
	query := r.URL.Query()
	projectValues, projectProvided := query["project"]
	if projectProvided {
		if len(projectValues) != 1 || strings.TrimSpace(projectValues[0]) == "" {
			return projectpkg.DetectionResult{}, fmt.Errorf("%w: project parameter must be specified exactly once and must not be blank", projectpkg.ErrInvalidProjectName)
		}
	}
	if raw, allProvided := query["all_projects"]; allProvided {
		if len(raw) != 1 {
			return projectpkg.DetectionResult{}, fmt.Errorf("%w: all_projects must be a boolean", projectpkg.ErrInvalidProjectName)
		}
		all, err := strconv.ParseBool(raw[0])
		if err != nil {
			return projectpkg.DetectionResult{}, fmt.Errorf("%w: all_projects must be a boolean", projectpkg.ErrInvalidProjectName)
		}
		if all {
			if projectProvided {
				return projectpkg.DetectionResult{}, fmt.Errorf("%w: project and all_projects cannot be used together", projectpkg.ErrInvalidProjectName)
			}
			mode = projectpkg.ResolutionAll
		}
	}
	return projectpkg.Resolve(projectpkg.ResolutionOptions{
		Mode:                 mode,
		Explicit:             query.Get("project"),
		Directory:            query.Get("cwd"),
		ProjectExists:        s.store.ProjectExists,
		RequireKnownExplicit: requireKnownOverrides,
		RequireKnownProcess:  requireKnownOverrides,
	})
}

func (s *Server) writeProjectResolutionError(w http.ResponseWriter, res projectpkg.DetectionResult, err error) {
	var unknown *projectpkg.UnknownProjectError
	if errors.As(err, &unknown) {
		available, listErr := s.store.ListProjectNames()
		if listErr != nil {
			jsonErrorWithFields(w, http.StatusInternalServerError, "project resolution failed: "+listErr.Error(), map[string]any{
				"code": "project_resolution_failed",
			})
			return
		}
		jsonErrorWithFields(w, http.StatusNotFound, fmt.Sprintf("project %q not found", unknown.Name), map[string]any{
			"code":               "unknown_project",
			"available_projects": available,
		})
		return
	}
	code := "project_resolution_failed"
	status := http.StatusInternalServerError
	if errors.Is(err, projectpkg.ErrInvalidProjectName) {
		code = "invalid_project"
		status = http.StatusBadRequest
	}
	if errors.Is(err, projectpkg.ErrAmbiguousProject) {
		code = "ambiguous_project"
		status = http.StatusConflict
	}
	fields := map[string]any{"code": code}
	if errors.Is(err, projectpkg.ErrAmbiguousProject) {
		fields["available_projects"] = res.AvailableProjects
		fields["project_source"] = res.Source
		fields["project_path"] = res.Path
	}
	jsonErrorWithFields(w, status, "project resolution failed: "+err.Error(), fields)
}

// ─── Sync Status ─────────────────────────────────────────────────────────────

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	if resolved.Source == projectpkg.SourceAllProjects {
		jsonErrorWithFields(w, http.StatusBadRequest, "all_projects is not supported for sync status", map[string]any{
			"code": "unsupported_project_scope",
		})
		return
	}
	if s.syncStatus == nil {
		jsonResponse(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "background sync is not configured",
		})
		return
	}

	status := s.syncStatus.Status(resolved.Project)
	jsonResponse(w, http.StatusOK, map[string]any{
		"enabled":              status.Enabled,
		"phase":                status.Phase,
		"last_error":           status.LastError,
		"consecutive_failures": status.ConsecutiveFailures,
		"backoff_until":        status.BackoffUntil,
		"last_sync_at":         status.LastSyncAt,
		"reason_code":          status.ReasonCode,
		"reason_message":       status.ReasonMessage,
		"deferred_count":       status.DeferredCount,
		"dead_count":           status.DeadCount,
		"upgrade": map[string]any{
			"stage":          status.UpgradeStage,
			"reason_code":    status.UpgradeReasonCode,
			"reason_message": status.UpgradeReasonMessage,
		},
	})
}

// ─── Project Ownership Rescue ─────────────────────────────────────────────────

func (s *Server) handleRescueProjectOwnership(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10) // 8 KB max
	var body struct {
		TargetProject  string   `json:"target_project"`
		Confirmed      bool     `json:"confirmed"`
		ObservationIDs []int64  `json:"observation_ids"`
		SessionIDs     []string `json:"session_ids"`
		PromptIDs      []int64  `json:"prompt_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	target, _ := store.NormalizeProject(body.TargetProject)
	if target == "" {
		jsonError(w, http.StatusBadRequest, "target_project is required")
		return
	}
	if !body.Confirmed {
		jsonError(w, http.StatusBadRequest, "confirmed must be true")
		return
	}
	result, err := s.store.RescueNullProjectOwnership(store.ProjectRescueParams{
		TargetProject:  target,
		ObservationIDs: body.ObservationIDs,
		SessionIDs:     body.SessionIDs,
		PromptIDs:      body.PromptIDs,
	})
	if err != nil {
		if errors.Is(err, store.ErrProjectRequired) || errors.Is(err, store.ErrProjectRescueInvalidRequest) {
			jsonError(w, http.StatusBadRequest, "invalid ownership rescue request: provide a target project and at least one positive record ID or non-blank session ID")
			return
		}
		log.Printf("[engram] project ownership rescue failed: %v", err)
		jsonError(w, http.StatusInternalServerError, "project ownership rescue is temporarily unavailable")
		return
	}
	if result.Rescued() > 0 || result.Journaled {
		s.notifyWrite()
	}
	// "rescued" and "partially_rescued" are distinguishable outcomes: the
	// counters alone cannot tell a clean move apart from one that left records
	// behind, so the status and the blocked list say it outright.
	status := "rescued"
	if !result.Complete {
		status = "partially_rescued"
	}
	blocked := result.Blocked
	if blocked == nil {
		blocked = []store.ProjectRescueBlocked{}
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"status":                status,
		"complete":              result.Complete,
		"blocked":               blocked,
		"target_project":        target,
		"rescued_observations":  result.RescuedObservations,
		"rescued_sessions":      result.RescuedSessions,
		"rescued_prompts":       result.RescuedPrompts,
		"conflicting_records":   result.ConflictingRecords,
		"skipped_records":       result.SkippedRecords,
		"journaled_local":       result.Journaled,
		"reconciliation_status": "local journal pending autosync",
	})
}

// ─── Conflicts ───────────────────────────────────────────────────────────────

const (
	conflictsDefaultLimit = 50
	conflictsMaxLimit     = 500
)

// clampConflictsLimit silently clamps limit to [1, conflictsMaxLimit].
// If the provided value is ≤ 0, it returns defaultVal (typically 50).
func clampConflictsLimit(v, defaultVal int) int {
	if v <= 0 {
		return defaultVal
	}
	if v > conflictsMaxLimit {
		return conflictsMaxLimit
	}
	return v
}

// handleListConflicts serves GET /conflicts
// Query params: project, status, since (RFC3339), limit (default 50, max 500), offset.
func (s *Server) handleListConflicts(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	project := resolved.Project
	status := r.URL.Query().Get("status")
	limit := clampConflictsLimit(queryInt(r, "limit", conflictsDefaultLimit), conflictsDefaultLimit)
	offset := queryInt(r, "offset", 0)

	opts := store.ListRelationsOptions{
		Project:            project,
		Status:             status,
		Limit:              limit,
		Offset:             offset,
		ExcludeNotConflict: true,
	}

	sinceStr := r.URL.Query().Get("since")
	if sinceStr != "" {
		t, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "invalid since parameter: must be RFC3339")
			return
		}
		opts.SinceTime = t
	}

	relations, err := s.store.ListRelations(opts)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Count without limit/offset for the total field.
	countOpts := store.ListRelationsOptions{
		Project:            project,
		Status:             status,
		SinceTime:          opts.SinceTime,
		ExcludeNotConflict: true,
	}
	total, err := s.store.CountRelations(countOpts)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"total":     total,
		"limit":     limit,
		"offset":    offset,
		"relations": relations,
	})
}

// handleConflictsStats serves GET /conflicts/stats
// Query params: project (optional — empty means global).
func (s *Server) handleConflictsStats(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.resolveRequestProject(r, projectpkg.ResolutionCurrent, true)
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	project := resolved.Project

	stats, err := s.store.GetRelationStats(project)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"project":            stats.Project,
		"by_relation":        stats.ByRelation,
		"by_judgment_status": stats.ByJudgmentStatus,
		"deferred":           stats.DeferredCount,
		"dead":               stats.DeadCount,
	})
}

// handleListDeferred serves GET /conflicts/deferred
// Query params: status, limit (default 50, max 500), offset.
func (s *Server) handleListDeferred(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit := clampConflictsLimit(queryInt(r, "limit", conflictsDefaultLimit), conflictsDefaultLimit)
	offset := queryInt(r, "offset", 0)

	opts := store.ListDeferredOptions{
		Status: status,
		Limit:  limit,
		Offset: offset,
	}

	rows, err := s.store.ListDeferred(opts)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Phase 3: count total via a second unbounded ListDeferred call.
	// Phase 4 hook: add CountDeferred(status) store method for O(1) count.
	totalOpts := store.ListDeferredOptions{Status: status}
	totalRows, err := s.store.ListDeferred(totalOpts)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"total": len(totalRows),
		"limit": limit,
		"rows":  rows,
	})
}

// handleScanConflicts serves POST /conflicts/scan
// Body: {"project":"X","since":"...","limit":int,"cursor":int,"apply":bool,"max_insert":int,
//
//	"semantic":bool,"concurrency":int,"timeout_per_call_seconds":int,"max_semantic":int}
func (s *Server) handleScanConflicts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project     string `json:"project"`
		AllProjects bool   `json:"all_projects"`
		Since       string `json:"since"`
		Apply       bool   `json:"apply"`
		MaxInsert   int    `json:"max_insert"`
		Limit       *int   `json:"limit"`
		Cursor      *int64 `json:"cursor"`

		// Phase 4 semantic fields — all optional, defaults match CLI.
		// Pointer types so absent fields are nil (use default) vs. explicit 0 (invalid).
		Semantic              bool `json:"semantic"`
		Concurrency           *int `json:"concurrency"`
		TimeoutPerCallSeconds *int `json:"timeout_per_call_seconds"`
		MaxSemantic           *int `json:"max_semantic"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if body.AllProjects && strings.TrimSpace(body.Project) != "" {
		jsonError(w, http.StatusBadRequest, "project and all_projects cannot be used together")
		return
	}
	mode := projectpkg.ResolutionCurrent
	if body.AllProjects {
		mode = projectpkg.ResolutionAll
	}
	resolved, err := projectpkg.Resolve(projectpkg.ResolutionOptions{
		Mode:                 mode,
		Explicit:             body.Project,
		Directory:            r.URL.Query().Get("cwd"),
		ProjectExists:        s.store.ProjectExists,
		RequireKnownExplicit: true,
		RequireKnownProcess:  true,
	})
	if err != nil {
		s.writeProjectResolutionError(w, resolved, err)
		return
	}
	if body.Limit != nil && (*body.Limit < 1 || *body.Limit > store.DefaultScanLimit) {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", store.DefaultScanLimit))
		return
	}
	if body.Cursor != nil && *body.Cursor < 0 {
		jsonError(w, http.StatusBadRequest, "cursor must be non-negative")
		return
	}
	// Resolve semantic params: validate explicit values, apply defaults for absent fields.
	concurrency := 5
	timeoutPerCallSeconds := 60
	maxSemantic := 100

	if body.Semantic {
		if body.Concurrency != nil {
			concurrency = *body.Concurrency
		}
		if body.TimeoutPerCallSeconds != nil {
			timeoutPerCallSeconds = *body.TimeoutPerCallSeconds
		}
		if body.MaxSemantic != nil {
			maxSemantic = *body.MaxSemantic
		}

		// Validate concurrency range [1, 20].
		if concurrency < 1 || concurrency > 20 {
			jsonError(w, http.StatusBadRequest,
				fmt.Sprintf("concurrency must be between 1 and 20; got %d", concurrency))
			return
		}

		// Validate timeout range [1, 600].
		if timeoutPerCallSeconds < 1 || timeoutPerCallSeconds > 600 {
			jsonError(w, http.StatusBadRequest,
				fmt.Sprintf("timeout_per_call_seconds must be between 1 and 600; got %d", timeoutPerCallSeconds))
			return
		}
	}

	opts := store.ScanOptions{Project: resolved.Project, Apply: body.Apply, MaxInsert: body.MaxInsert}
	if body.Limit != nil {
		opts.Limit = *body.Limit
	}
	if body.Cursor != nil {
		opts.Cursor = *body.Cursor
	}
	if body.Since != "" {
		t, err := time.Parse(time.RFC3339, body.Since)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "invalid since: must be RFC3339")
			return
		}
		opts.Since = t
	}

	// Wire semantic options when requested.
	if body.Semantic {
		if s.runnerFactory == nil {
			jsonError(w, http.StatusInternalServerError,
				"ENGRAM_AGENT_CLI not set: server runnerFactory is not configured")
			return
		}

		name := os.Getenv("ENGRAM_AGENT_CLI")
		runner, err := s.runnerFactory(name)
		if err != nil {
			jsonError(w, http.StatusInternalServerError,
				"failed to resolve agent runner: "+err.Error())
			return
		}

		promptBuilder := s.promptBuilder
		if promptBuilder == nil {
			// Fallback: empty prompt (adequate for tests without LLM).
			promptBuilder = func(a, b store.ObservationSnippet) string { return "" }
		}

		opts.Semantic = true
		opts.Concurrency = concurrency
		opts.TimeoutPerCall = time.Duration(timeoutPerCallSeconds) * time.Second
		opts.MaxSemantic = maxSemantic
		opts.Runner = runner
		opts.BuildPrompt = promptBuilder
	}

	var result store.ScanResult
	if body.AllProjects {
		result, err = s.store.ScanAllProjects(opts)
	} else {
		result, err = s.store.ScanProject(opts)
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := map[string]any{
		"project":          result.Project,
		"inspected":        result.Inspected,
		"ranked_queries":   result.RankedQueries,
		"candidates_found": result.CandidatesFound,
		"already_related":  result.AlreadyRelated,
		"inserted":         result.RelationsInserted,
		"capped":           result.Capped,
		"dry_run":          result.DryRun,
		// Semantic counters — always present; zero when semantic=false.
		"semantic_judged":  result.SemanticJudged,
		"semantic_skipped": result.SemanticSkipped,
		"semantic_errors":  result.SemanticErrors,
	}
	if result.NextCursor != nil {
		resp["next_cursor"] = *result.NextCursor
	}
	if result.Capped {
		resp["warning"] = "cap reached: this page has no continuation; rerun from the same cursor with a higher applicable cap"
	}

	jsonResponse(w, http.StatusOK, resp)
}

// handleJudgeConflict serves POST /conflicts/judge.
// Body: {"judgment_id":"rel-...","relation":"related|compatible|scoped|conflicts_with|supersedes|not_conflict", ...}
func (s *Server) handleJudgeConflict(w http.ResponseWriter, r *http.Request) {
	var body struct {
		JudgmentID string   `json:"judgment_id"`
		Relation   string   `json:"relation"`
		Reason     string   `json:"reason"`
		Evidence   string   `json:"evidence"`
		Confidence *float64 `json:"confidence"`
		SessionID  string   `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if strings.TrimSpace(body.JudgmentID) == "" {
		jsonError(w, http.StatusBadRequest, "judgment_id is required")
		return
	}
	if strings.TrimSpace(body.Relation) == "" {
		jsonError(w, http.StatusBadRequest, "relation is required")
		return
	}

	var reason *string
	if body.Reason != "" {
		reason = &body.Reason
	}
	var evidence *string
	if body.Evidence != "" {
		evidence = &body.Evidence
	}
	if body.Confidence != nil && (*body.Confidence < 0 || *body.Confidence > 1) {
		jsonError(w, http.StatusBadRequest, "confidence must be between 0.0 and 1.0")
		return
	}

	relation, err := s.store.JudgeRelation(store.JudgeRelationParams{
		JudgmentID:    body.JudgmentID,
		Relation:      body.Relation,
		Reason:        reason,
		Evidence:      evidence,
		Confidence:    body.Confidence,
		MarkedByActor: "agent",
		MarkedByKind:  "agent",
		SessionID:     body.SessionID,
	})
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.notifyWrite()
	jsonResponse(w, http.StatusOK, map[string]any{"relation": relation})
}

// handleCompareMemories serves POST /conflicts/compare.
// Body: {"memory_id_a":1,"memory_id_b":2,"relation":"related", "confidence":0.9, "reasoning":"..."}
func (s *Server) handleCompareMemories(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MemoryIDA  int64    `json:"memory_id_a"`
		MemoryIDB  int64    `json:"memory_id_b"`
		Relation   string   `json:"relation"`
		Confidence *float64 `json:"confidence"`
		Reasoning  string   `json:"reasoning"`
		Model      string   `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if body.MemoryIDA == 0 {
		jsonError(w, http.StatusBadRequest, "memory_id_a is required")
		return
	}
	if body.MemoryIDB == 0 {
		jsonError(w, http.StatusBadRequest, "memory_id_b is required")
		return
	}
	if strings.TrimSpace(body.Relation) == "" {
		jsonError(w, http.StatusBadRequest, "relation is required")
		return
	}
	if strings.TrimSpace(body.Reasoning) == "" {
		jsonError(w, http.StatusBadRequest, "reasoning is required")
		return
	}
	if body.Confidence == nil {
		jsonError(w, http.StatusBadRequest, "confidence is required")
		return
	}
	confidence := *body.Confidence
	if confidence < 0 || confidence > 1 {
		jsonError(w, http.StatusBadRequest, "confidence must be between 0.0 and 1.0")
		return
	}

	obsA, err := s.store.GetObservation(body.MemoryIDA)
	if err != nil {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("observation id=%d not found: %s", body.MemoryIDA, err))
		return
	}
	obsB, err := s.store.GetObservation(body.MemoryIDB)
	if err != nil {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("observation id=%d not found: %s", body.MemoryIDB, err))
		return
	}

	syncID, err := s.store.JudgeBySemantic(store.JudgeBySemanticParams{
		SourceID:   obsA.SyncID,
		TargetID:   obsB.SyncID,
		Relation:   body.Relation,
		Confidence: confidence,
		Reasoning:  body.Reasoning,
		Model:      body.Model,
	})
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	if syncID != "" {
		s.notifyWrite()
	}
	jsonResponse(w, http.StatusOK, map[string]any{"sync_id": syncID})
}

// handleReplayDeferred serves POST /conflicts/deferred/replay
func (s *Server) handleReplayDeferred(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.ReplayDeferred()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"retried":   result.Retried,
		"succeeded": result.Succeeded,
		"failed":    result.Failed,
		"dead":      result.Dead,
	})
}

// handleGetConflict serves GET /conflicts/{relation_id}
// Returns full relation detail with source/target observation snippets.
// Returns 404 when relation_id does not exist.
func (s *Server) handleGetConflict(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("relation_id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid relation_id: must be an integer")
		return
	}

	item, err := s.store.GetRelationByIntID(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	// Build source/target snippets (first 200 chars of title as snippet proxy).
	// Full observation content snippets are not stored in RelationListItem; for Phase 3
	// the title serves as the accessible snippet from the JOIN.
	jsonResponse(w, http.StatusOK, map[string]any{
		"relation_id":     item.ID,
		"sync_id":         item.SyncID,
		"relation":        item.Relation,
		"judgment_status": item.JudgmentStatus,
		"source_id":       item.SourceID,
		"source_snippet":  item.SourceTitle,
		"target_id":       item.TargetID,
		"target_snippet":  item.TargetTitle,
		"created_at":      item.CreatedAt,
		"updated_at":      item.UpdatedAt,
	})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (s *Server) validateSessionProject(w http.ResponseWriter, sessionID, projectName string) bool {
	if strings.TrimSpace(projectName) == "" {
		return true
	}
	projectName, _ = store.NormalizeProject(projectName)
	session, err := s.store.GetSession(sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, http.StatusNotFound, "session not found")
			return false
		}
		jsonError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	sessionProject, _ := store.NormalizeProject(session.Project)
	if sessionProject != "" && sessionProject != projectName {
		jsonErrorWithFields(w, http.StatusBadRequest, "session project does not match requested project", map[string]any{
			"code":            "session_project_mismatch",
			"session_project": sessionProject,
			"project":         projectName,
		})
		return false
	}
	return true
}

func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, map[string]string{"error": msg})
}

func jsonErrorWithFields(w http.ResponseWriter, status int, msg string, fields map[string]any) {
	payload := map[string]any{"error": msg}
	for key, value := range fields {
		payload[key] = value
	}
	jsonResponse(w, status, payload)
}

func queryInt(r *http.Request, key string, defaultVal int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}

func queryBool(r *http.Request, key string, defaultVal bool) bool {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultVal
	}
	return b
}
