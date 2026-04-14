package cloudserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloudstore"
)

type contextKey string

const (
	ctxUserID        contextKey = "user_id"
	ctxProtoVer      contextKey = "protocol_version"
	ctxBatchInternal contextKey = "batch_internal"

	minProtocolVersion     = 1
	currentProtocolVersion = 1
)

// UserIDFromContext extracts the authenticated user ID from the request context.
func UserIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxUserID).(string); ok {
		return v
	}
	return ""
}

// AuthMiddleware validates the Bearer token and injects user_id into context.
func AuthMiddleware(store *cloudstore.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Batch sub-requests already have auth context — skip redundant DB lookup
			if r.Context().Value(ctxBatchInternal) != nil && UserIDFromContext(r.Context()) != "" {
				next.ServeHTTP(w, r)
				return
			}

			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or invalid Authorization header"})
				return
			}

			rawKey := strings.TrimPrefix(auth, "Bearer ")
			userID, err := store.ValidateAPIKey(r.Context(), rawKey)
			if err != nil || userID == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid API key"})
				return
			}

			ctx := context.WithValue(r.Context(), ctxUserID, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ProtocolVersionMiddleware checks the X-Engram-Protocol header.
func ProtocolVersionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Batch sub-requests already have protocol version in context
		if r.Context().Value(ctxBatchInternal) != nil && r.Context().Value(ctxProtoVer) != nil {
			next.ServeHTTP(w, r)
			return
		}

		verStr := r.Header.Get("X-Engram-Protocol")
		if verStr == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "X-Engram-Protocol header required"})
			return
		}

		ver, err := strconv.Atoi(verStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid protocol version"})
			return
		}

		if ver < minProtocolVersion {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":           "client version too old, please upgrade",
				"min_version":     strconv.Itoa(minProtocolVersion),
				"current_version": strconv.Itoa(currentProtocolVersion),
			})
			return
		}

		ctx := context.WithValue(r.Context(), ctxProtoVer, ver)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RateLimitMiddleware enforces per-user sliding window rate limits.
func RateLimitMiddleware(store *cloudstore.Store, endpoint string, maxPerMinute int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := UserIDFromContext(r.Context())
			if userID == "" {
				next.ServeHTTP(w, r)
				return
			}

			allowed, err := checkRateLimit(r.Context(), store, userID, endpoint, maxPerMinute)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "rate limit check failed"})
				return
			}
			if !allowed {
				writeJSON(w, http.StatusTooManyRequests, map[string]string{
					"error":    "rate limit exceeded",
					"endpoint": endpoint,
					"limit":    strconv.Itoa(maxPerMinute) + "/min",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func checkRateLimit(ctx context.Context, store *cloudstore.Store, userID, endpoint string, maxPerMinute int) (bool, error) {
	pool := store.Pool()
	windowStart := time.Now().UTC().Truncate(time.Minute)

	// Upsert the counter for this window
	var count int
	err := pool.QueryRow(ctx, `
		INSERT INTO rate_limits (user_id, endpoint, window_start, request_count)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (user_id, endpoint, window_start)
		DO UPDATE SET request_count = rate_limits.request_count + 1
		RETURNING request_count
	`, userID, endpoint, windowStart).Scan(&count)
	if err != nil {
		return false, err
	}

	return count <= maxPerMinute, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
