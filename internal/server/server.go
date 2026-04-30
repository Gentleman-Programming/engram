// Package server provides the HTTP API for Engram.
//
// This is how external clients (OpenCode plugin, Claude Code hooks,
// any agent) communicate with the memory engine. Simple JSON REST API.
package server

import (
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

	"github.com/Gentleman-Programming/engram/internal/store"
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
	s.mux.HandleFunc("DELETE /sessions/{id}", s.handleDeleteSession)

	// Observations
	s.mux.HandleFunc("POST /observations", s.handleAddObservation)
	s.mux.HandleFunc("POST /observations/passive", s.handlePassiveCapture)
	s.mux.HandleFunc("GET /observations/recent", s.handleRecentObservations)
	s.mux.HandleFunc("PATCH /observations/{id}", s.handleUpdateObservation)
	s.mux.HandleFunc("DELETE /observations/{id}", s.handleDeleteObservation)

	// Search
	s.mux.HandleFunc("GET /search", s.handleSearch)

	// Timeline
	s.mux.HandleFunc("GET /timeline", s.handleTimeline)
	s.mux.HandleFunc("GET /observations/{id}", s.handleGetObservation)

	// Prompts
	s.mux.HandleFunc("POST /prompts", s.handleAddPrompt)
	s.mux.HandleFunc("GET /prompts/recent", s.handleRecentPrompts)
	s.mux.HandleFunc("GET /prompts/search", s.handleSearchPrompts)
	s.mux.HandleFunc("DELETE /prompts/{id}", s.handleDeletePrompt)

	// Context
	s.mux.HandleFunc("GET /context", s.handleContext)

	// Export / Import
	s.mux.HandleFunc("GET /export", s.handleExport)
	s.mux.HandleFunc("POST /import", s.handleImport)

	// Stats
	s.mux.HandleFunc("GET /stats", s.handleStats)

	// Project migration
	s.mux.HandleFunc("POST /projects/migrate", s.handleMigrateProject)

	// Sync status (degraded-state visibility for autosync)
	s.mux.HandleFunc("GET /sync/status", s.handleSyncStatus)

	// Conflicts — CRITICAL ORDER: literals before wildcard (Go 1.22 mux panics on overlap)
	s.mux.HandleFunc("GET /conflicts", s.handleListConflicts)
	s.mux.HandleFunc("GET /conflicts/stats", s.handleConflictsStats)
	s.mux.HandleFunc("GET /conflicts/deferred", s.handleListDeferred)
	s.mux.HandleFunc("POST /conflicts/scan", s.handleScanConflicts)
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
		ID        string `json:"id"`
		Project   string `json:"project"`
		Directory string `json:"directory"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if body.ID == "" || body.Project == "" {
		jsonError(w, http.StatusBadRequest, "id and project are required")
		return
	}

	if err := s.store.CreateSession(body.ID, body.Project, body.Directory); err != nil {
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
	project := r.URL.Query().Get("project")
	limit := queryInt(r, "limit", 5)

	sessions, err := s.store.RecentSessions(project, limit)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, sessions)
}

func (s *Server) handleAddObservation(w http.ResponseWriter, r *http.Request) {
	var body store.AddObservationParams
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if body.SessionID == "" || body.Title == "" || body.Content == "" {
		jsonError(w, http.StatusBadRequest, "session_id, title, and content are required")
		return
	}

	id, err := s.store.AddObservation(body)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
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

	result, err := s.store.PassiveCapture(body)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.notifyWrite()
	jsonResponse(w, http.StatusOK, result)
}

func (s *Server) handleRecentObservations(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	scope := r.URL.Query().Get("scope")
	limit := queryInt(r, "limit", 20)

	obs, err := s.store.RecentObservations(project, scope, limit)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, obs)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		jsonError(w, http.StatusBadRequest, "q parameter is required")
		return
	}

	results, err := s.store.Search(query, store.SearchOptions{
		Type:    r.URL.Query().Get("type"),
		Project: r.URL.Query().Get("project"),
		Scope:   r.URL.Query().Get("scope"),
		Limit:   queryInt(r, "limit", 10),
	})
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
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
		jsonError(w, http.StatusNotFound, err.Error())
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

	result, err := s.store.Timeline(id, before, after)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, result)
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

	id, err := s.store.AddPrompt(body)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.notifyWrite()
	jsonResponse(w, http.StatusCreated, map[string]any{"id": id, "status": "saved"})
}

func (s *Server) handleRecentPrompts(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	limit := queryInt(r, "limit", 20)

	prompts, err := s.store.RecentPrompts(project, limit)
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

	prompts, err := s.store.SearchPrompts(
		query,
		r.URL.Query().Get("project"),
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
	query := r.URL.Query()
	project := query.Get("project")
	if _, provided := query["project"]; provided && strings.TrimSpace(project) == "" {
		jsonError(w, http.StatusBadRequest, "project parameter must not be blank")
		return
	}

	var (
		data *store.ExportData
		err  error
	)
	if strings.TrimSpace(project) != "" {
		data, err = s.store.ExportProject(project)
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
	project := r.URL.Query().Get("project")
	scope := r.URL.Query().Get("scope")

	context, err := s.store.FormatContext(project, scope)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"context": context})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := loadServerStats(s.store)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, stats)
}

// ─── Sync Status ─────────────────────────────────────────────────────────────

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if s.syncStatus == nil {
		jsonResponse(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "background sync is not configured",
		})
		return
	}

	status := s.syncStatus.Status(r.URL.Query().Get("project"))
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

// ─── Project Migration ───────────────────────────────────────────────────────

func (s *Server) handleMigrateProject(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10) // 1 KB max
	var body struct {
		OldProject string `json:"old_project"`
		NewProject string `json:"new_project"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.OldProject == "" || body.NewProject == "" {
		jsonError(w, http.StatusBadRequest, "old_project and new_project are required")
		return
	}
	if body.OldProject == body.NewProject {
		jsonResponse(w, http.StatusOK, map[string]any{"status": "skipped", "reason": "names are identical"})
		return
	}

	result, err := s.store.MigrateProject(body.OldProject, body.NewProject)
	if err != nil {
		log.Printf("[engram] project migration failed: %v", err)
		jsonError(w, http.StatusInternalServerError, "migration failed")
		return
	}

	if !result.Migrated {
		jsonResponse(w, http.StatusOK, map[string]any{"status": "skipped", "reason": "no records found"})
		return
	}

	log.Printf("[engram] migrated project %q → %q (obs: %d, sessions: %d, prompts: %d)",
		body.OldProject, body.NewProject,
		result.ObservationsUpdated, result.SessionsUpdated, result.PromptsUpdated)

	jsonResponse(w, http.StatusOK, map[string]any{
		"status":       "migrated",
		"old_project":  body.OldProject,
		"new_project":  body.NewProject,
		"observations": result.ObservationsUpdated,
		"sessions":     result.SessionsUpdated,
		"prompts":      result.PromptsUpdated,
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
	project := r.URL.Query().Get("project")
	status := r.URL.Query().Get("status")
	limit := clampConflictsLimit(queryInt(r, "limit", conflictsDefaultLimit), conflictsDefaultLimit)
	offset := queryInt(r, "offset", 0)

	opts := store.ListRelationsOptions{
		Project: project,
		Status:  status,
		Limit:   limit,
		Offset:  offset,
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
		Project:   project,
		Status:    status,
		SinceTime: opts.SinceTime,
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
	project := r.URL.Query().Get("project")

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
// Body: {"project":"X","since":"...","apply":bool,"max_insert":int,
//        "semantic":bool,"concurrency":int,"timeout_per_call_seconds":int,"max_semantic":int}
func (s *Server) handleScanConflicts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project   string `json:"project"`
		Since     string `json:"since"`
		Apply     bool   `json:"apply"`
		MaxInsert int    `json:"max_insert"`

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
	if body.Project == "" {
		jsonError(w, http.StatusBadRequest, "project is required")
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

	opts := store.ScanOptions{
		Project:   body.Project,
		Apply:     body.Apply,
		MaxInsert: body.MaxInsert,
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

	result, err := s.store.ScanProject(opts)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := map[string]any{
		"project":          result.Project,
		"inspected":        result.Inspected,
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
	if result.Capped {
		resp["warning"] = "cap reached: not all candidates were inserted"
	}

	jsonResponse(w, http.StatusOK, resp)
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

func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, map[string]string{"error": msg})
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
