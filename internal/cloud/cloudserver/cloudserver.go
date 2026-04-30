package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/internal/cloud/dashboard"
	engramproject "github.com/Gentleman-Programming/engram/internal/project"
	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
)

type Option func(*CloudServer)

type ChunkStore interface {
	ReadManifest(ctx context.Context, project string) (*engramsync.Manifest, error)
	WriteChunk(ctx context.Context, project, chunkID, createdBy, clientCreatedAt string, payload []byte) error
	ReadChunk(ctx context.Context, project, chunkID string) ([]byte, error)
	KnownSessionIDs(ctx context.Context, project string) (map[string]struct{}, error)
}

type Authenticator interface {
	Authorize(r *http.Request) error
}

type ProjectAuthorizer interface {
	AuthorizeProject(project string) error
}

type dashboardSessionCodec interface {
	MintDashboardSession(bearerToken string) (string, error)
	ParseDashboardSession(sessionToken string) (string, error)
}

type staticStatusProvider struct{ status dashboard.SyncStatus }

func (s staticStatusProvider) Status() dashboard.SyncStatus { return s.status }

type CloudServer struct {
	store          ChunkStore
	auth           Authenticator
	projectAuth    ProjectAuthorizer
	dashboardAdmin string
	port           int
	host           string
	mux            *http.ServeMux
	syncStatus     dashboard.SyncStatusProvider
	listenAndServe func(addr string, handler http.Handler) error
}

const defaultHost = "127.0.0.1"
const maxPushBodyBytes int64 = 8 * 1024 * 1024
const maxDashboardLoginBodyBytes int64 = 16 * 1024
const dashboardSessionCookieName = "engram_dashboard_token"

var ErrDashboardSessionCodecRequired = errors.New("dashboard session codec is required for dashboard auth")

func WithSyncStatusProvider(provider dashboard.SyncStatusProvider) Option {
	return func(s *CloudServer) {
		s.syncStatus = provider
	}
}

func WithHost(host string) Option {
	return func(s *CloudServer) {
		s.host = strings.TrimSpace(host)
	}
}

func WithProjectAuthorizer(authorizer ProjectAuthorizer) Option {
	return func(s *CloudServer) {
		s.projectAuth = authorizer
	}
}

func WithDashboardAdminToken(adminToken string) Option {
	return func(s *CloudServer) {
		s.dashboardAdmin = strings.TrimSpace(adminToken)
	}
}

func New(store ChunkStore, authSvc Authenticator, port int, opts ...Option) *CloudServer {
	s := &CloudServer{
		store: store,
		auth:  authSvc,
		port:  port,
		host:  defaultHost,
		syncStatus: staticStatusProvider{status: dashboard.SyncStatus{
			Phase:         "degraded",
			ReasonCode:    constants.ReasonTransportFailed,
			ReasonMessage: "sync status provider is unavailable",
		}},
		listenAndServe: http.ListenAndServe,
	}
	if projectAuthorizer, ok := authSvc.(ProjectAuthorizer); ok {
		s.projectAuth = projectAuthorizer
	}
	for _, opt := range opts {
		opt(s)
	}
	s.routes()
	return s
}

func (s *CloudServer) Start() error {
	host := strings.TrimSpace(s.host)
	if host == "" {
		host = defaultHost
	}
	addr := fmt.Sprintf("%s:%d", host, s.port)
	log.Printf("[engram-cloud] listening on %s", addr)
	return s.listenAndServe(addr, s.Handler())
}

func (s *CloudServer) Handler() http.Handler {
	if s.mux == nil {
		s.routes()
	}
	return s.mux
}

func (s *CloudServer) routes() {
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /health", s.handleHealth)
	var dashboardStore dashboard.DashboardStore
	if store, ok := s.store.(dashboard.DashboardStore); ok {
		dashboardStore = store
	}
	validateLoginToken := func(token string) error {
		token = strings.TrimSpace(token)
		if token == "" {
			return fmt.Errorf("bearer token is required")
		}
		if adminToken := strings.TrimSpace(s.dashboardAdmin); adminToken != "" && token == adminToken {
			return nil
		}
		if s.auth == nil {
			return nil
		}
		req, _ := http.NewRequest(http.MethodGet, "/dashboard/login", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return s.auth.Authorize(req)
	}
	createSessionCookie := func(w http.ResponseWriter, r *http.Request, token string) error {
		sessionToken, err := s.dashboardSessionToken(token)
		if err != nil {
			return err
		}
		http.SetCookie(w, &http.Cookie{
			Name:     dashboardSessionCookieName,
			Value:    sessionToken,
			Path:     "/dashboard",
			HttpOnly: true,
			Secure:   dashboardCookieSecure(r),
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int((8 * time.Hour).Seconds()),
		})
		return nil
	}
	if s.auth == nil {
		validateLoginToken = nil
		createSessionCookie = nil
	}
	dashboard.Mount(s.mux, dashboard.MountConfig{
		RequireSession:      s.authorizeDashboardRequest,
		ValidateLoginToken:  validateLoginToken,
		CreateSessionCookie: createSessionCookie,
		ClearSessionCookie: func(w http.ResponseWriter, r *http.Request) {
			http.SetCookie(w, &http.Cookie{
				Name:     dashboardSessionCookieName,
				Value:    "",
				Path:     "/dashboard",
				HttpOnly: true,
				Secure:   dashboardCookieSecure(r),
				SameSite: http.SameSiteLaxMode,
				MaxAge:   -1,
			})
		},
		IsAdmin: func(r *http.Request) bool {
			return s.isDashboardAdmin(r)
		},
		// GetDisplayName: returns "OPERATOR" until the session codec surfaces a
		// display name (out of scope for this change). Satisfies REQ-103 / AD-2.
		GetDisplayName:    func(r *http.Request) string { return "OPERATOR" },
		Store:             dashboardStore,
		MaxLoginBodyBytes: maxDashboardLoginBodyBytes,
		StatusProvider:    s.syncStatus,
	})
	s.mux.HandleFunc("GET /sync/pull", s.withAuth(s.handlePullManifest))
	s.mux.HandleFunc("GET /sync/pull/{chunkID}", s.withAuth(s.handlePullChunk))
	s.mux.HandleFunc("POST /sync/push", s.withAuth(s.handlePushChunk))
	s.mux.HandleFunc("POST /sync/mutations/push", s.withAuth(s.handleMutationPush))
	s.mux.HandleFunc("GET /sync/mutations/pull", s.withAuth(s.handleMutationPull))
}

func (s *CloudServer) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.auth != nil {
			if err := s.auth.Authorize(r); err != nil {
				http.Error(w, fmt.Sprintf("unauthorized: %v", err), http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (s *CloudServer) withAuthHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth != nil {
			if err := s.auth.Authorize(r); err != nil {
				http.Error(w, fmt.Sprintf("unauthorized: %v", err), http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *CloudServer) authorizeDashboardRequest(r *http.Request) error {
	if s.auth == nil {
		return nil
	}
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil {
		return err
	}
	bearerToken, err := s.dashboardBearerToken(cookie.Value)
	if err != nil {
		return err
	}
	if strings.TrimSpace(bearerToken) == "" {
		return fmt.Errorf("dashboard session token is empty")
	}
	if adminToken := strings.TrimSpace(s.dashboardAdmin); adminToken != "" && bearerToken == adminToken {
		return nil
	}
	req, _ := http.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	return s.auth.Authorize(req)
}

func (s *CloudServer) dashboardSessionToken(bearerToken string) (string, error) {
	if codec, ok := s.auth.(dashboardSessionCodec); ok {
		return codec.MintDashboardSession(bearerToken)
	}
	return "", ErrDashboardSessionCodecRequired
}

func (s *CloudServer) dashboardBearerToken(sessionToken string) (string, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return "", fmt.Errorf("dashboard session token is empty")
	}
	if codec, ok := s.auth.(dashboardSessionCodec); ok {
		return codec.ParseDashboardSession(sessionToken)
	}
	return "", ErrDashboardSessionCodecRequired
}

func dashboardCookieSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	forwardedProto := r.Header.Get("X-Forwarded-Proto")
	for _, proto := range strings.Split(forwardedProto, ",") {
		if strings.EqualFold(strings.TrimSpace(proto), "https") {
			return true
		}
	}
	return false
}

func (s *CloudServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "service": "engram-cloud"})
}

func (s *CloudServer) isDashboardAdmin(r *http.Request) bool {
	if s.auth == nil {
		return false
	}
	adminToken := strings.TrimSpace(s.dashboardAdmin)
	if adminToken == "" {
		return false
	}
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil {
		return false
	}
	token, err := s.dashboardBearerToken(cookie.Value)
	if err != nil {
		return false
	}
	return token == adminToken
}

func (s *CloudServer) handlePullManifest(w http.ResponseWriter, r *http.Request) {
	project, ok := projectFromRequest(w, r)
	if !ok {
		return
	}
	if !s.authorizeProjectScope(w, project) {
		return
	}
	manifest, err := s.store.ReadManifest(r.Context(), project)
	if err != nil {
		http.Error(w, fmt.Sprintf("read manifest: %v", err), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, manifest)
}

func (s *CloudServer) handlePullChunk(w http.ResponseWriter, r *http.Request) {
	project, ok := projectFromRequest(w, r)
	if !ok {
		return
	}
	if !s.authorizeProjectScope(w, project) {
		return
	}
	chunkID := strings.TrimSpace(r.PathValue("chunkID"))
	if chunkID == "" {
		http.Error(w, "chunkID is required", http.StatusBadRequest)
		return
	}
	chunk, err := s.store.ReadChunk(r.Context(), project, chunkID)
	if err != nil {
		if errors.Is(err, cloudstore.ErrChunkNotFound) {
			http.Error(w, fmt.Sprintf("read chunk: %v", err), http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("read chunk: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(chunk)
}

func (s *CloudServer) handlePushChunk(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPushBodyBytes)
	var req struct {
		ChunkID         string          `json:"chunk_id"`
		CreatedBy       string          `json:"created_by"`
		ClientCreatedAt string          `json:"client_created_at"`
		Project         string          `json:"project"`
		Data            json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeActionableError(w, http.StatusRequestEntityTooLarge, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadTooLarge, fmt.Sprintf("push payload too large (max %d bytes)", maxPushBodyBytes))
			return
		}
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid push payload: %v", err))
		return
	}
	if len(req.Data) == 0 {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "data is required")
		return
	}
	project := strings.TrimSpace(req.Project)
	if project == "" {
		project = strings.TrimSpace(r.URL.Query().Get("project"))
	}
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeProjectRequired, "project is required")
		return
	}
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeProjectRequired, "project is required")
		return
	}
	if !s.authorizeProjectScope(w, project) {
		return
	}

	// Push-path pause guard: check project sync control before accepting the chunk.
	// Uses a structural interface assertion so the ChunkStore interface is NOT extended.
	// Satisfies REQ-109 / Design Decision 5.
	if storeForControls, ok := s.store.(interface {
		IsProjectSyncEnabled(project string) (bool, error)
	}); ok {
		enabled, err := storeForControls.IsProjectSyncEnabled(project)
		if err != nil {
			writeActionableError(w, http.StatusInternalServerError,
				constants.UpgradeErrorClassBlocked,
				constants.UpgradeErrorCodeInternal,
				fmt.Sprintf("check project control: %v", err))
			return
		}
		if !enabled {
			// REQ-405: emit audit entry for chunk-push pause-rejection before writing 409.
			// Structural type assertion — ChunkStore is NOT extended.
			contributor := strings.TrimSpace(req.CreatedBy)
			if contributor == "" {
				contributor = "unknown"
			}
			if auditor, ok := s.store.(interface {
				InsertAuditEntry(ctx context.Context, entry cloudstore.AuditEntry) error
			}); ok {
				if aerr := auditor.InsertAuditEntry(r.Context(), cloudstore.AuditEntry{
					Contributor: contributor,
					Project:     project,
					Action:      cloudstore.AuditActionChunkPush,
					Outcome:     cloudstore.AuditOutcomeRejectedProjectPaused,
					ReasonCode:  "sync-paused",
				}); aerr != nil {
					log.Printf("cloudserver: audit insert failed (chunk push): %v", aerr)
				}
			} else {
				log.Printf("cloudserver: store (%T) does not implement InsertAuditEntry; audit skipped", s.store)
			}
			// JW4: include project envelope fields in 409 response, consistent
			// with the mutation push 409 envelope (REQ-414 parity for chunk path).
			jsonResponse(w, http.StatusConflict, map[string]any{
				"error_class":    strings.TrimSpace(constants.UpgradeErrorClassPolicy),
				"error_code":     "sync-paused",
				"error":          fmt.Sprintf("sync is paused for project %q", project),
				"project":        project,
				"project_source": engramproject.SourceRequestBody,
				"project_path":   "",
			})
			return
		}
	}

	normalizedData, err := coerceChunkProject(req.Data, project)
	if err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid push payload: %v", err))
		return
	}
	chunk, err := validateImportableChunkPayload(normalizedData)
	if err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid push payload: %v", err))
		return
	}
	knownSessionIDs, err := s.store.KnownSessionIDs(r.Context(), project)
	if err != nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("validate push payload: %v", err))
		return
	}
	if err := validateChunkSessionReferences(chunk, knownSessionIDs); err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid push payload: %v", err))
		return
	}

	computedChunkID := chunkIDFromPayload(normalizedData)
	providedChunkID := strings.TrimSpace(req.ChunkID)
	if providedChunkID != "" && providedChunkID != computedChunkID {
		log.Printf("cloudserver: chunk_id mismatch for project %q: client=%q server=%q; accepting server-canonicalized payload", project, providedChunkID, computedChunkID)
	}
	clientCreatedAt := strings.TrimSpace(req.ClientCreatedAt)
	if clientCreatedAt != "" {
		if _, err := time.Parse(time.RFC3339, clientCreatedAt); err != nil {
			writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "client_created_at must be RFC3339")
			return
		}
	}

	if err := s.store.WriteChunk(r.Context(), project, computedChunkID, req.CreatedBy, clientCreatedAt, normalizedData); err != nil {
		if errors.Is(err, cloudstore.ErrChunkConflict) {
			writeActionableError(w, http.StatusConflict, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodeChunkConflict, fmt.Sprintf("write chunk: %v", err))
			return
		}
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("write chunk: %v", err))
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "chunk_id": computedChunkID})
}

func chunkIDFromPayload(payload []byte) string {
	return chunkcodec.ChunkID(payload)
}

func projectFromRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeProjectRequired, "project is required")
		return "", false
	}
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeProjectRequired, "project is required")
		return "", false
	}
	return project, true
}

func (s *CloudServer) authorizeProjectScope(w http.ResponseWriter, project string) bool {
	if s.projectAuth == nil {
		return true
	}
	if err := s.projectAuth.AuthorizeProject(project); err != nil {
		writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, "forbidden: project is not allowed")
		return false
	}
	return true
}

func writeActionableError(w http.ResponseWriter, status int, class, code, message string) {
	jsonResponse(w, status, map[string]any{
		"error_class": strings.TrimSpace(class),
		"error_code":  strings.TrimSpace(code),
		"error":       strings.TrimSpace(message),
	})
}

func coerceChunkProject(payload []byte, project string) ([]byte, error) {
	return chunkcodec.CanonicalizeForProject(payload, project)
}

func decodeSyncMutationPayload(payload string, dest any) error {
	return chunkcodec.DecodeSyncMutationPayload(payload, dest)
}

func validateImportableChunkPayload(payload []byte) (engramsync.ChunkData, error) {
	var chunk engramsync.ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return engramsync.ChunkData{}, fmt.Errorf("chunk schema: %w", err)
	}
	if err := validateDirectChunkArrayEntries(chunk); err != nil {
		return engramsync.ChunkData{}, err
	}
	return chunk, nil

}

func validateDirectChunkArrayEntries(chunk engramsync.ChunkData) error {
	for i, session := range chunk.Sessions {
		if strings.TrimSpace(session.ID) == "" {
			return fmt.Errorf("sessions[%d].id is required", i)
		}
		if strings.TrimSpace(session.Directory) == "" {
			return fmt.Errorf("sessions[%d].directory is required", i)
		}
	}

	for i, observation := range chunk.Observations {
		if strings.TrimSpace(observation.SyncID) == "" {
			return fmt.Errorf("observations[%d].sync_id is required", i)
		}
		if strings.TrimSpace(observation.SessionID) == "" {
			return fmt.Errorf("observations[%d].session_id is required", i)
		}
		if strings.TrimSpace(observation.Type) == "" {
			return fmt.Errorf("observations[%d].type is required", i)
		}
		if strings.TrimSpace(observation.Title) == "" {
			return fmt.Errorf("observations[%d].title is required", i)
		}
		if strings.TrimSpace(observation.Content) == "" {
			return fmt.Errorf("observations[%d].content is required", i)
		}
		if strings.TrimSpace(observation.Scope) == "" {
			return fmt.Errorf("observations[%d].scope is required", i)
		}
	}

	for i, prompt := range chunk.Prompts {
		if strings.TrimSpace(prompt.SyncID) == "" {
			return fmt.Errorf("prompts[%d].sync_id is required", i)
		}
		if strings.TrimSpace(prompt.SessionID) == "" {
			return fmt.Errorf("prompts[%d].session_id is required", i)
		}
		if strings.TrimSpace(prompt.Content) == "" {
			return fmt.Errorf("prompts[%d].content is required", i)
		}
	}

	return nil
}

func validateChunkSessionReferences(chunk engramsync.ChunkData, knownSessionIDs map[string]struct{}) error {
	chunkSessionIDs := make(map[string]struct{}, len(chunk.Sessions))
	for i, session := range chunk.Sessions {
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			return fmt.Errorf("sessions[%d].id is required", i)
		}
		chunkSessionIDs[sessionID] = struct{}{}
	}
	for i, mutation := range chunk.Mutations {
		if mutation.Entity != store.SyncEntitySession || mutation.Op != store.SyncOpUpsert {
			continue
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := decodeSyncMutationPayload(mutation.Payload, &body); err != nil {
			return fmt.Errorf("mutations[%d] invalid payload: %w", i, err)
		}
		sessionID := strings.TrimSpace(body.ID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(mutation.EntityKey)
		}
		if sessionID == "" {
			return fmt.Errorf("mutations[%d].payload.id is required for session upsert", i)
		}
		chunkSessionIDs[sessionID] = struct{}{}
	}

	hasSession := func(sessionID string) bool {
		if _, ok := chunkSessionIDs[sessionID]; ok {
			return true
		}
		_, ok := knownSessionIDs[sessionID]
		return ok
	}

	for i, observation := range chunk.Observations {
		sessionID := strings.TrimSpace(observation.SessionID)
		if sessionID == "" {
			return fmt.Errorf("observations[%d].session_id is required", i)
		}
		if !hasSession(sessionID) {
			return fmt.Errorf("observations[%d] references missing session_id %q", i, sessionID)
		}
	}

	for i, prompt := range chunk.Prompts {
		sessionID := strings.TrimSpace(prompt.SessionID)
		if sessionID == "" {
			return fmt.Errorf("prompts[%d].session_id is required", i)
		}
		if !hasSession(sessionID) {
			return fmt.Errorf("prompts[%d] references missing session_id %q", i, sessionID)
		}
	}

	for i, mutation := range chunk.Mutations {
		if mutation.Entity != store.SyncEntityObservation && mutation.Entity != store.SyncEntityPrompt {
			continue
		}
		var body struct {
			SessionID string `json:"session_id"`
		}
		if err := decodeSyncMutationPayload(mutation.Payload, &body); err != nil {
			return fmt.Errorf("mutations[%d] invalid payload: %w", i, err)
		}
		sessionID := strings.TrimSpace(body.SessionID)
		if mutation.Op == store.SyncOpUpsert && sessionID == "" {
			return fmt.Errorf("mutations[%d].payload.session_id is required for upsert", i)
		}
		if mutation.Op == store.SyncOpUpsert && !hasSession(sessionID) {
			return fmt.Errorf("mutations[%d] references missing session_id %q", i, sessionID)
		}
	}
	return nil
}

func jsonResponse(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
