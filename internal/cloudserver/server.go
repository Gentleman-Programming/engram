// Package cloudserver implements the HTTP API for the engram cloud sync server.
package cloudserver

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Gentleman-Programming/engram/internal/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/types"
)

const version = "0.1.0"

// New creates the HTTP handler with all routes and middleware wired.
func New(store *cloudstore.Store) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	// Health (no auth)
	r.Get("/api/v1/health", handleHealth(store))

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(AuthMiddleware(store))
		r.Use(ProtocolVersionMiddleware)

		// Sync
		r.With(RateLimitMiddleware(store, "push", 60)).
			Post("/api/v1/sync/push", handlePush(store))
		r.With(RateLimitMiddleware(store, "pull", 120)).
			Get("/api/v1/sync/pull", handlePull(store))

		// Auth management
		r.Post("/api/v1/auth/rotate-key", handleRotateKey(store))

		// Projects
		r.Get("/api/v1/projects", handleListProjects(store))

		// CRUD (cloud-only, rate limited)
		r.Route("/api/v1", func(cr chi.Router) {
			cr.Use(RateLimitMiddleware(store, "crud", 600))

			cr.Post("/observations", handleCreateObservation(store))
			cr.Get("/observations/{id}", handleGetObservation(store))
			cr.Get("/search", handleSearch(store))
			cr.Get("/context", handleContext(store))
			cr.Get("/stats", handleStats(store))
		})

		// Batch — uses the full router so sub-requests go through all middleware
		r.Post("/api/v1/batch", handleBatch(r))
	})

	return r
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func handleHealth(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var seqVal int64
		pgOK := "connected"
		if err := store.Pool().QueryRow(r.Context(), "SELECT COALESCE(SUM(value), 0) FROM server_seq_counter").Scan(&seqVal); err != nil {
			pgOK = "error: " + err.Error()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     "ok",
			"version":    version,
			"postgresql": pgOK,
			"server_seq": seqVal,
		})
	}
}

func handlePush(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := UserIDFromContext(r.Context())

		// Check idempotency
		idempKey := r.Header.Get("X-Idempotency-Key")
		if idempKey != "" {
			cached, _ := store.CheckIdempotencyKey(r.Context(), idempKey)
			if cached != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write(cached)
				return
			}
		}

		var req struct {
			DeviceID  string               `json:"device_id"`
			Project   string               `json:"project"`
			Mutations []cloudstore.Mutation `json:"mutations"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		// Check membership
		isMember, _ := store.IsMember(r.Context(), req.Project, userID)
		if !isMember {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this project"})
			return
		}

		result, err := store.ProcessPush(r.Context(), req.Mutations, userID, req.Project)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		// Cache response for idempotency
		if idempKey != "" {
			_ = store.SaveIdempotencyKey(r.Context(), idempKey, result)
		}

		writeJSON(w, http.StatusOK, result)
	}
}

func handlePull(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := UserIDFromContext(r.Context())
		project := r.URL.Query().Get("project")
		sinceSeq := queryInt(r, "since_seq", 0)
		limit := queryInt(r, "limit", 500)

		if project == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project parameter required"})
			return
		}

		isMember, _ := store.IsMember(r.Context(), project, userID)
		if !isMember {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this project"})
			return
		}

		result, err := store.Pull(r.Context(), project, int64(sinceSeq), userID, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, result)
	}
}

func handleRotateKey(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := UserIDFromContext(r.Context())
		newKey, err := store.RotateKey(r.Context(), userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"api_key": newKey})
	}
}

func handleListProjects(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := UserIDFromContext(r.Context())
		projects, err := store.ListUserProjects(r.Context(), userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
	}
}

func handleCreateObservation(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := UserIDFromContext(r.Context())

		var req struct {
			SessionID string `json:"session_id"`
			Type      string `json:"type"`
			Title     string `json:"title"`
			Content   string `json:"content"`
			ToolName  string `json:"tool_name,omitempty"`
			Project   string `json:"project"`
			Scope     string `json:"scope,omitempty"`
			TopicKey  string `json:"topic_key,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
			return
		}
		if req.Project == "" || req.Title == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project and title required"})
			return
		}

		isMember, _ := store.IsMember(r.Context(), req.Project, userID)
		if !isMember {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"})
			return
		}

		params := types.AddObservationParams{
			SessionID: req.SessionID, Type: req.Type, Title: req.Title,
			Content: req.Content, ToolName: req.ToolName, Scope: req.Scope, TopicKey: req.TopicKey,
		}
		obsID, err := store.CreateObservation(r.Context(), params, req.Project, userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"id": obsID})
	}
}

func handleGetObservation(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := UserIDFromContext(r.Context())
		id := chi.URLParam(r, "id")
		project := r.URL.Query().Get("project")
		if project == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project required"})
			return
		}

		obs, err := store.GetObservation(r.Context(), id, project, userID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, obs)
	}
}

func handleSearch(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := UserIDFromContext(r.Context())
		query := r.URL.Query().Get("q")
		project := r.URL.Query().Get("project")
		limit := queryInt(r, "limit", 20)

		if query == "" || project == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q and project required"})
			return
		}

		isMember, _ := store.IsMember(r.Context(), project, userID)
		if !isMember {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"})
			return
		}

		results, err := store.Search(r.Context(), query, project, userID, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results, "count": len(results)})
	}
}

func handleContext(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := UserIDFromContext(r.Context())
		project := r.URL.Query().Get("project")
		if project == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project required"})
			return
		}

		ctx, err := store.GetContext(r.Context(), project, userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"context": ctx})
	}
}

func handleStats(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := UserIDFromContext(r.Context())
		project := r.URL.Query().Get("project")
		if project == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project required"})
			return
		}

		stats, err := store.GetStats(r.Context(), project, userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, stats)
	}
}

// helpers

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
