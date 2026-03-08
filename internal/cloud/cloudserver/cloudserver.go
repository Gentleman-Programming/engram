package cloudserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/dashboard"
)

// ─── CloudServer ────────────────────────────────────────────────────────────

// CloudServer provides the HTTP API for Engram cloud mode.
type CloudServer struct {
	store   *cloudstore.CloudStore
	auth    *auth.Service
	mux     *http.ServeMux
	port    int
	listen  func(network, address string) (net.Listener, error)
	serve   func(net.Listener, http.Handler) error
	now     func() time.Time
	limit   *authRateLimiter
	dashCfg dashboard.DashboardConfig
}

// New creates a new CloudServer and registers all routes.
func New(store *cloudstore.CloudStore, authSvc *auth.Service, port int, opts ...Option) *CloudServer {
	srv := &CloudServer{
		store:  store,
		auth:   authSvc,
		port:   port,
		listen: net.Listen,
		serve:  http.Serve,
		now:    time.Now,
	}
	for _, opt := range opts {
		opt(srv)
	}
	srv.limit = newAuthRateLimiter(func() time.Time { return srv.now() })
	srv.mux = http.NewServeMux()
	srv.routes()
	return srv
}

// Option configures a CloudServer.
type Option func(*CloudServer)

// WithDashboard enables the embedded web dashboard with the given config.
func WithDashboard(cfg dashboard.DashboardConfig) Option {
	return func(s *CloudServer) {
		s.dashCfg = cfg
	}
}

// Start binds to the configured port and serves HTTP traffic. It matches
// the pattern from internal/server/server.go.
func (s *CloudServer) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
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
		return fmt.Errorf("engram cloud server: listen %s: %w", addr, err)
	}
	log.Printf("[engram-cloud] HTTP server listening on %s", addr)
	return serveFn(ln, s.mux)
}

// Handler returns the underlying http.Handler for testing.
func (s *CloudServer) Handler() http.Handler {
	return s.mux
}

// ─── Route Registration ─────────────────────────────────────────────────────

func (s *CloudServer) routes() {
	// Health (no auth)
	s.mux.HandleFunc("GET /health", s.handleHealth)

	// Auth routes (no auth required)
	s.mux.HandleFunc("POST /auth/register", s.handleRegister)
	s.mux.HandleFunc("POST /auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /auth/refresh", s.handleRefresh)

	// API key management (auth required)
	s.mux.HandleFunc("POST /auth/api-key", s.withAuth(s.handleGenerateAPIKey))
	s.mux.HandleFunc("DELETE /auth/api-key", s.withAuth(s.handleRevokeAPIKey))

	// Sync routes (auth required)
	s.mux.HandleFunc("POST /sync/push", s.withAuth(s.handlePush))
	s.mux.HandleFunc("GET /sync/pull", s.withAuth(s.handlePullManifest))
	s.mux.HandleFunc("GET /sync/pull/{chunk_id}", s.withAuth(s.handlePullChunk))

	// Mutation-based sync routes (auth required)
	s.mux.HandleFunc("POST /sync/mutations/push", s.withAuth(s.handleMutationPush))
	s.mux.HandleFunc("GET /sync/mutations/pull", s.withAuth(s.handleMutationPull))

	// Search & context (auth required)
	s.mux.HandleFunc("GET /sync/search", s.withAuth(s.handleSearch))
	s.mux.HandleFunc("GET /sync/context", s.withAuth(s.handleContext))

	// Dashboard — embedded web UI
	dashboard.Mount(s.mux, s.store, s.auth, s.dashCfg)
}

// ─── Health ─────────────────────────────────────────────────────────────────

func (s *CloudServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		if err := s.store.Ping(); err != nil {
			jsonResponse(w, http.StatusServiceUnavailable, map[string]any{
				"status":   "degraded",
				"service":  "engram-cloud",
				"version":  "0.1.0",
				"database": "unavailable",
			})
			return
		}
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "engram-cloud",
		"version": "0.1.0",
	})
}

// ─── Auth Handlers ──────────────────────────────────────────────────────────

func (s *CloudServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	if retryAfter, limited := s.checkRateLimit(r, "register", 5); limited {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		jsonError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	var body struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if body.Username == "" || body.Email == "" || body.Password == "" {
		jsonError(w, http.StatusBadRequest, "username, email, and password are required")
		return
	}
	if err := validateRegisterRequest(body.Username, body.Email, body.Password); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := s.auth.Register(body.Username, body.Email, body.Password)
	if err != nil {
		if err == auth.ErrWeakPassword {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		if duplicateField := duplicateRegistrationField(err); duplicateField != "" {
			jsonError(w, http.StatusConflict, duplicateField+" is already registered")
			return
		}
		writeStoreError(w, err, err.Error())
		return
	}

	jsonResponse(w, http.StatusCreated, result)
}

func duplicateRegistrationField(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "cloud_users_email_key") || strings.Contains(msg, "duplicate key") && strings.Contains(msg, "email"):
		return "email"
	case strings.Contains(msg, "cloud_users_username_key") || strings.Contains(msg, "duplicate key") && strings.Contains(msg, "username"):
		return "username"
	default:
		return ""
	}
}

func (s *CloudServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if retryAfter, limited := s.checkRateLimit(r, "login", 10); limited {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		jsonError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	var body struct {
		Identifier string `json:"identifier"`
		Username   string `json:"username"`
		Password   string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	identifier := strings.TrimSpace(body.Identifier)
	if identifier == "" {
		identifier = strings.TrimSpace(body.Username)
	}
	if identifier == "" || body.Password == "" {
		jsonError(w, http.StatusBadRequest, "identifier and password are required")
		return
	}

	result, err := s.auth.Login(identifier, body.Password)
	if err != nil {
		if err == auth.ErrInvalidCredentials {
			jsonError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeStoreError(w, err, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

func (s *CloudServer) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if body.RefreshToken == "" {
		jsonError(w, http.StatusBadRequest, "refresh_token is required")
		return
	}

	newAccessToken, err := s.auth.RefreshAccessToken(body.RefreshToken)
	if err != nil {
		jsonError(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"access_token": newAccessToken,
		"expires_in":   3600,
	})
}

// ─── API Key Handlers ───────────────────────────────────────────────────────

func (s *CloudServer) handleGenerateAPIKey(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)

	plainKey, hash, err := auth.GenerateAPIKey()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to generate api key")
		return
	}

	if err := s.store.SetAPIKeyHash(userID, hash); err != nil {
		writeStoreError(w, err, "failed to store api key")
		return
	}

	jsonResponse(w, http.StatusCreated, map[string]string{
		"api_key": plainKey,
		"message": "Store this key securely. It will not be shown again.",
	})
}

func (s *CloudServer) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)

	if err := s.store.SetAPIKeyHash(userID, ""); err != nil {
		writeStoreError(w, err, "failed to revoke api key")
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{
		"status": "revoked",
	})
}

// ─── Search Handler ─────────────────────────────────────────────────────────

func (s *CloudServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)

	query := r.URL.Query().Get("q")
	if query == "" {
		jsonError(w, http.StatusBadRequest, "q parameter is required")
		return
	}
	limit := queryInt(r, "limit", 10)
	if err := validateSearchParams(query, limit); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	results, err := s.store.Search(userID, query, cloudstore.CloudSearchOptions{
		Type:    r.URL.Query().Get("type"),
		Project: r.URL.Query().Get("project"),
		Scope:   r.URL.Query().Get("scope"),
		Limit:   limit,
	})
	if err != nil {
		writeStoreError(w, err, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"results": results,
	})
}

// ─── Context Handler ────────────────────────────────────────────────────────

func (s *CloudServer) handleContext(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)

	project := r.URL.Query().Get("project")
	scope := r.URL.Query().Get("scope")

	ctx, err := s.store.FormatContext(userID, project, scope)
	if err != nil {
		writeStoreError(w, err, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{
		"context": ctx,
	})
}

// ─── Helpers ────────────────────────────────────────────────────────────────

func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, map[string]string{"error": msg})
}

func writeStoreError(w http.ResponseWriter, err error, fallback string) {
	if errors.Is(err, cloudstore.ErrNotFound) {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	if isDBConnectionError(err) {
		jsonError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	jsonError(w, http.StatusInternalServerError, fallback)
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

// isDBConnectionError checks if an error indicates a Postgres connection
// failure, mapping to HTTP 503.
func isDBConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrConnDone) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "no connection") ||
		strings.Contains(msg, "driver: bad connection") ||
		strings.Contains(msg, "database is closed") ||
		strings.Contains(msg, "connection not open") ||
		strings.Contains(msg, "sql: database is closed")
}

type authRateLimiter struct {
	now      func() time.Time
	mu       sync.Mutex
	attempts map[string]rateLimitState
}

type rateLimitState struct {
	count   int
	resetAt time.Time
}

func newAuthRateLimiter(now func() time.Time) *authRateLimiter {
	return &authRateLimiter{
		now:      now,
		attempts: make(map[string]rateLimitState),
	}
}

func (s *CloudServer) checkRateLimit(r *http.Request, endpoint string, maxAttempts int) (int, bool) {
	if s.limit == nil {
		return 0, false
	}
	key := endpoint + ":" + clientIP(r)
	return s.limit.allow(key, maxAttempts, time.Minute)
}

func (rl *authRateLimiter) allow(key string, maxAttempts int, window time.Duration) (int, bool) {
	now := rl.now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	for existingKey, state := range rl.attempts {
		if !now.Before(state.resetAt) {
			delete(rl.attempts, existingKey)
		}
	}

	state := rl.attempts[key]
	if state.resetAt.IsZero() || !now.Before(state.resetAt) {
		state = rateLimitState{resetAt: now.Add(window)}
	}
	if state.count >= maxAttempts {
		retryAfter := int(time.Until(state.resetAt).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		rl.attempts[key] = state
		return retryAfter, true
	}

	state.count++
	rl.attempts[key] = state
	return 0, false
}

func clientIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	if strings.TrimSpace(r.RemoteAddr) != "" {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return "unknown"
}
