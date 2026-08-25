// Package server provides the HTTP API for Engram.
//
// This is how external clients (OpenCode plugin, Claude Code hooks,
// any agent) communicate with the memory engine. Simple JSON REST API.
package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
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
}

type Server struct {
	store      *store.Store
	mux        *http.ServeMux
	port       int
	listen     func(network, address string) (net.Listener, error)
	serve      func(net.Listener, http.Handler) error
	onWrite    func() // called after successful local writes (for autosync notification)
	syncStatus SyncStatusProvider
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

	if body.Type == nil && body.Title == nil && body.Content == nil && body.Find == nil && body.Replace == nil && body.Project == nil && body.Scope == nil && body.TopicKey == nil {
		jsonError(w, http.StatusBadRequest, "at least one field is required")
		return
	}

	obs, err := s.store.UpdateObservation(id, body)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrFindReplacePairRequired), errors.Is(err, store.ErrFindReplaceWithContent):
			jsonError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, sql.ErrNoRows), errors.Is(err, store.ErrObservationNotFound):
			jsonError(w, http.StatusNotFound, err.Error())
		default:
			jsonError(w, http.StatusInternalServerError, err.Error())
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
