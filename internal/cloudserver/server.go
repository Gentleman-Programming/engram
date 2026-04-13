// Package cloudserver implements the HTTP API for the engram cloud sync server.
package cloudserver

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Gentleman-Programming/engram/internal/cloudstore"
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
		r.Route("/api/v1", func(r chi.Router) {
			r.Use(RateLimitMiddleware(store, "crud", 600))

			r.Post("/observations", handleCreateObservation(store))
			r.Get("/observations/{id}", handleGetObservation(store))
			r.Get("/search", handleSearch(store))
			r.Get("/context", handleContext(store))
			r.Get("/stats", handleStats(store))
		})
	})

	return r
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func handleHealth(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var seqVal int64
		pgOK := "connected"
		if err := store.Pool().QueryRow(r.Context(), "SELECT value FROM server_seq_counter").Scan(&seqVal); err != nil {
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
		// Placeholder — will be fully implemented in Phase 2.5
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not yet implemented"})
	}
}

func handleGetObservation(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not yet implemented"})
	}
}

func handleSearch(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not yet implemented"})
	}
}

func handleContext(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not yet implemented"})
	}
}

func handleStats(store *cloudstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not yet implemented"})
	}
}

// helpers

func queryInt(r *http.Request, key string, defaultVal int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	var n int
	if _, err := json.Number(v).Int64(); err == nil {
		// simple int parse
		for _, c := range v {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			}
		}
		return n
	}
	return defaultVal
}
