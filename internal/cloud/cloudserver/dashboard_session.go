package cloudserver

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/constants"
)

const dashboardPrincipalSessionVersion = 1
const dashboardSessionKeyBytes = 32
const authAuditOutcomeDenied = "denied"
const authAuditActionDashboardBootstrap = "bootstrap.dashboard"
const authAuditActionDashboardLogin = "dashboard.login"
const authAuditReasonInvalidToken = "invalid_token"
const authAuditReasonLegacyRecoveryRequired = "legacy_recovery_credential_required"

// authAuditActorSourceUnauthenticated is a dedicated, non-empty audit-only
// actor source used exclusively to label rejected/anonymous dashboard login
// attempts. It is intentionally NOT a cloudauth.PrincipalSource: no
// cloudauth.Principal is ever actually constructed with this value, so it
// never participates in Principal.Validate() or authentication/authorization
// decisions. It exists only because cloudstore.InsertAuthAuditEvent requires
// a non-empty ActorSource, and a rejected login has no resolved principal
// (and therefore no valid Source) to report. None of the existing
// cloudauth.PrincipalSource constants semantically fit "no principal was
// resolved" (they all describe a specific successfully-identified actor),
// so a minimal sentinel is added here rather than reusing a misleading one.
const authAuditActorSourceUnauthenticated = "unauthenticated"

type principalStateStore interface {
	GetPrincipal(ctx context.Context, id string) (cloudstore.Principal, error)
}

type dashboardPrincipalStore interface {
	principalStateStore
	HasActiveAdmin(ctx context.Context) (bool, error)
	CreateHumanUser(ctx context.Context, params cloudstore.CreateHumanUserParams) (cloudstore.HumanUser, error)
	// CreateFirstAdminHumanUser MUST be used (instead of a separate
	// HasActiveAdmin-then-CreateHumanUser sequence) for first-admin
	// dashboard bootstrap: see handleDashboardBootstrapSubmit. Doing the
	// check and the create as two separate calls reintroduces a
	// check-then-act (TOCTOU) race where two concurrent bootstrap attempts
	// (dashboard and/or CLI) could both observe "no active admin" and both
	// create a first admin.
	CreateFirstAdminHumanUser(ctx context.Context, params cloudstore.CreateHumanUserParams) (cloudstore.HumanUser, error)
	InsertAuthAuditEvent(ctx context.Context, event cloudstore.AuthAuditEvent) error
}

type dashboardPrincipalSessionClaims struct {
	PrincipalID     string `json:"principal_id"`
	PrincipalSource string `json:"principal_source"`
	Kind            string `json:"kind"`
	Role            string `json:"role"`
	DisplayName     string `json:"display_name"`
	IssuedAt        int64  `json:"iat"`
	ExpiresAt       int64  `json:"exp"`
	SessionVersion  int    `json:"session_version"`
	ManagedTokenID  string `json:"token_id,omitempty"`
}

func WithPrincipalStateStore(store principalStateStore) Option {
	return func(s *CloudServer) {
		s.principalState = store
	}
}

func (s *CloudServer) dashboardSessionTokenForRequest(ctx context.Context, bearerToken string) (string, error) {
	bearerToken = strings.TrimSpace(bearerToken)
	if bearerToken == "" {
		return "", fmt.Errorf("bearer token is required")
	}
	if s.principalAuth != nil {
		principal, err := s.principalAuth.ResolveBearerToken(ctx, bearerToken)
		if err == nil {
			// Successful admin/dashboard login is an authoritative security
			// event: audit it synchronously and fail closed (never mint a
			// session) if the audit event cannot be recorded. This is an
			// audit-then-mint order: the opposite of the
			// mutation-then-audit-then-fail pattern used by recordAdminAudit
			// for admin API mutations, where the audit happens AFTER an
			// authoritative mutation already occurred. Here, minting the
			// session IS the authoritative action, so it must never happen
			// without a recorded audit trail.
			if auditErr := s.recordDashboardLoginAudit(ctx, principal, authAuditOutcomeSuccess, "", false, false); auditErr != nil {
				return "", auditErr
			}
			if principal.Source == cloudauth.PrincipalSourceManagedToken {
				return s.mintDashboardPrincipalSession(principal)
			}
			return s.dashboardSessionToken(bearerToken)
		}
		if adminToken := strings.TrimSpace(s.dashboardAdmin); adminToken == "" || !hmac.Equal([]byte(bearerToken), []byte(adminToken)) {
			return "", err
		}
		legacyPrincipal := legacyDashboardAdminAuditPrincipal()
		recovery, recoveryCheckFailed := s.isLegacyRecoveryLogin(ctx)
		if auditErr := s.recordDashboardLoginAudit(ctx, legacyPrincipal, authAuditOutcomeSuccess, "", recovery, recoveryCheckFailed); auditErr != nil {
			return "", auditErr
		}
	}
	return s.dashboardSessionToken(bearerToken)
}

// validateDashboardLoginToken accepts or rejects a bearer token presented on
// the dashboard login form. It is the accept/reject decision point for the
// `dashboard.login` audit event; rejected attempts are audited best-effort so a
// transient audit-store hiccup never blocks reporting an already-rejected
// login, matching the best-effort convention used for sync rejection audits.
func (s *CloudServer) validateDashboardLoginToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("bearer token is required")
	}
	ctx := context.Background()
	if adminToken := strings.TrimSpace(s.dashboardAdmin); adminToken != "" && hmac.Equal([]byte(token), []byte(adminToken)) {
		return nil
	}
	if s.principalAuth != nil {
		_, err := s.principalAuth.ResolveBearerToken(ctx, token)
		if err != nil {
			s.recordDashboardLoginAuditBestEffort(ctx, cloudauth.Principal{}, authAuditOutcomeDenied, authAuditReasonInvalidToken, false)
		}
		return err
	}
	if s.auth == nil {
		return nil
	}
	req, _ := http.NewRequest(http.MethodGet, "/dashboard/login", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if err := s.auth.Authorize(req); err != nil {
		s.recordDashboardLoginAuditBestEffort(ctx, cloudauth.Principal{}, authAuditOutcomeDenied, authAuditReasonInvalidToken, false)
		return err
	}
	return nil
}

// dashboardLoginAuditStore returns the identity store when it supports the
// dashboard-facing principal/audit surface (GetPrincipal, HasActiveAdmin,
// CreateHumanUser, InsertAuthAuditEvent). Dashboard login/recovery audit
// events are skipped (not an error) when no such store is configured, so
// deployments that never wired an admin identity store keep their existing
// login behavior unchanged.
func (s *CloudServer) dashboardLoginAuditStore() (dashboardPrincipalStore, bool) {
	store, ok := s.adminIdentity.(dashboardPrincipalStore)
	if !ok || store == nil {
		return nil, false
	}
	return store, true
}

// recordDashboardLoginAudit persists a dashboard login audit event. actorSource
// is always non-empty: when principal.Source is empty (a rejected/unresolved
// login has no valid principal), it falls back to
// authAuditActorSourceUnauthenticated so the event is never dropped by the
// store's "actor source is required" validation (see cloudstore.InsertAuthAuditEvent).
//
// recoveryCheckFailed takes precedence over recovery: when the legacy
// active-admin lookup itself failed, the event must not silently claim
// "recovery: false" (which would misreport a possible recovery login as a
// normal one); instead it records an explicit "recovery_check_failed"
// indicator so the audit trail reflects the undetermined/degraded state.
func (s *CloudServer) recordDashboardLoginAudit(ctx context.Context, principal cloudauth.Principal, outcome, reason string, recovery, recoveryCheckFailed bool) error {
	store, ok := s.dashboardLoginAuditStore()
	if !ok {
		return nil
	}
	actorSource := auditActorSource(principal)
	metadata := map[string]any{"source": actorSource, "role": string(principal.Role)}
	switch {
	case recoveryCheckFailed:
		metadata["recovery_check_failed"] = true
	case recovery:
		metadata["recovery"] = true
	}
	return store.InsertAuthAuditEvent(ctx, cloudstore.AuthAuditEvent{
		ActorPrincipalID: auditActorPrincipalIDRef(principal),
		ActorSource:      actorSource,
		Action:           authAuditActionDashboardLogin,
		Outcome:          strings.TrimSpace(outcome),
		ReasonCode:       strings.TrimSpace(reason),
		Metadata:         metadata,
	})
}

func (s *CloudServer) recordDashboardLoginAuditBestEffort(ctx context.Context, principal cloudauth.Principal, outcome, reason string, recovery bool) {
	if err := s.recordDashboardLoginAudit(ctx, principal, outcome, reason, recovery, false); err != nil {
		log.Printf("[engram-cloud] dashboard login audit insert failed (best-effort): %v", err)
	}
}

// isLegacyRecoveryLogin reports whether a legacy admin dashboard login is
// happening after at least one managed admin already exists. This tags the
// login audit event as explicit bootstrap/recovery access instead of a
// silent permanent bypass around managed admin roles, per the legacy admin
// compatibility design.
//
// A transient HasActiveAdmin error must not silently downgrade a possible
// recovery login to a plain "recovery: false" entry in the audit trail: that
// would misreport an undetermined case as a definitively clean non-recovery
// login. On error, recoveryCheckFailed is reported so the caller can record
// an explicit degraded/undetermined indicator instead, and the error is
// logged for operational visibility.
func (s *CloudServer) isLegacyRecoveryLogin(ctx context.Context) (recovery bool, recoveryCheckFailed bool) {
	store, ok := s.dashboardLoginAuditStore()
	if !ok {
		return false, false
	}
	hasAdmin, err := store.HasActiveAdmin(ctx)
	if err != nil {
		log.Printf("[engram-cloud] dashboard legacy recovery check failed (active admin lookup): %v", err)
		return false, true
	}
	return hasAdmin, false
}

func legacyDashboardAdminAuditPrincipal() cloudauth.Principal {
	return cloudauth.Principal{
		ID:          "legacy:admin",
		Kind:        cloudauth.PrincipalKindLegacy,
		DisplayName: "OPERATOR",
		Role:        cloudauth.RoleAdmin,
		Source:      cloudauth.PrincipalSourceLegacyEnvAdmin,
		Enabled:     true,
	}
}

func (s *CloudServer) mintDashboardPrincipalSession(principal cloudauth.Principal) (string, error) {
	if err := principal.Validate(); err != nil {
		return "", err
	}
	if !principal.Enabled {
		return "", cloudauth.ErrPrincipalDisabled
	}
	issuedAt := time.Now().UTC()
	claims := dashboardPrincipalSessionClaims{
		PrincipalID:     strings.TrimSpace(principal.ID),
		PrincipalSource: string(principal.Source),
		Kind:            string(principal.Kind),
		Role:            string(principal.Role),
		DisplayName:     strings.TrimSpace(principal.DisplayName),
		IssuedAt:        issuedAt.Unix(),
		ExpiresAt:       issuedAt.Add(8 * time.Hour).Unix(),
		SessionVersion:  dashboardPrincipalSessionVersion,
		ManagedTokenID:  strings.TrimSpace(principal.TokenID),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(payload)
	signature, err := s.signDashboardPrincipalSession(payloadPart)
	if err != nil {
		return "", err
	}
	return payloadPart + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (s *CloudServer) parseDashboardPrincipalSession(sessionToken string) (cloudauth.Principal, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	parts := strings.Split(sessionToken, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return cloudauth.Principal{}, cloudauth.ErrInvalidDashboardSessionToken
	}
	providedSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return cloudauth.Principal{}, cloudauth.ErrInvalidDashboardSessionToken
	}
	expectedSig, err := s.signDashboardPrincipalSession(parts[0])
	if err != nil {
		return cloudauth.Principal{}, err
	}
	if !hmac.Equal(expectedSig, providedSig) {
		return cloudauth.Principal{}, cloudauth.ErrInvalidDashboardSessionToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return cloudauth.Principal{}, cloudauth.ErrInvalidDashboardSessionToken
	}
	var claims dashboardPrincipalSessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return cloudauth.Principal{}, cloudauth.ErrInvalidDashboardSessionToken
	}
	if claims.SessionVersion != dashboardPrincipalSessionVersion || claims.ExpiresAt <= time.Now().UTC().Unix() {
		return cloudauth.Principal{}, cloudauth.ErrInvalidDashboardSessionToken
	}
	principal := cloudauth.Principal{
		ID:          strings.TrimSpace(claims.PrincipalID),
		Kind:        cloudauth.PrincipalKind(strings.TrimSpace(claims.Kind)),
		DisplayName: strings.TrimSpace(claims.DisplayName),
		Role:        cloudauth.Role(strings.TrimSpace(claims.Role)),
		Enabled:     true,
		Source:      cloudauth.PrincipalSource(strings.TrimSpace(claims.PrincipalSource)),
		TokenID:     strings.TrimSpace(claims.ManagedTokenID),
	}
	if err := principal.Validate(); err != nil {
		return cloudauth.Principal{}, err
	}
	return principal, nil
}

func (s *CloudServer) dashboardPrincipalFromCookie(ctx context.Context, sessionToken string) (cloudauth.Principal, bool) {
	principal, err := s.parseDashboardPrincipalSession(sessionToken)
	if err != nil {
		return cloudauth.Principal{}, false
	}
	principal, err = s.revalidateDashboardPrincipal(ctx, principal)
	if err != nil {
		return cloudauth.Principal{}, false
	}
	return principal, true
}

func (s *CloudServer) dashboardPrincipalFromRequest(r *http.Request) (cloudauth.Principal, bool) {
	if principal, ok := PrincipalFromContext(r.Context()); ok {
		return principal, true
	}
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil {
		return cloudauth.Principal{}, false
	}
	return s.dashboardPrincipalFromCookie(r.Context(), cookie.Value)
}

func (s *CloudServer) dashboardDisplayName(r *http.Request) string {
	if principal, ok := s.dashboardPrincipalFromRequest(r); ok && strings.TrimSpace(principal.DisplayName) != "" {
		return strings.TrimSpace(principal.DisplayName)
	}
	return "OPERATOR"
}

func (s *CloudServer) revalidateDashboardPrincipal(ctx context.Context, principal cloudauth.Principal) (cloudauth.Principal, error) {
	if principal.Source != cloudauth.PrincipalSourceManagedToken {
		if err := principal.Validate(); err != nil {
			return cloudauth.Principal{}, err
		}
		if !principal.Enabled {
			return cloudauth.Principal{}, cloudauth.ErrPrincipalDisabled
		}
		return principal, nil
	}
	if s.principalState == nil {
		return cloudauth.Principal{}, fmt.Errorf("dashboard principal state store is not configured")
	}
	current, err := s.principalState.GetPrincipal(ctx, principal.ID)
	if err != nil {
		return cloudauth.Principal{}, err
	}
	if !current.Enabled {
		return cloudauth.Principal{}, cloudauth.ErrPrincipalDisabled
	}
	revalidated := cloudauth.Principal{
		ID:          strings.TrimSpace(current.ID),
		Kind:        cloudauth.PrincipalKind(strings.TrimSpace(current.Kind)),
		DisplayName: strings.TrimSpace(current.DisplayName),
		Role:        cloudauth.Role(strings.TrimSpace(current.Role)),
		Enabled:     current.Enabled,
		Source:      cloudauth.PrincipalSourceManagedToken,
		TokenID:     strings.TrimSpace(principal.TokenID),
	}
	if err := revalidated.Validate(); err != nil {
		return cloudauth.Principal{}, err
	}
	return revalidated, nil
}

func (s *CloudServer) signDashboardPrincipalSession(payloadPart string) ([]byte, error) {
	key, err := s.dashboardPrincipalSessionKey()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("engram-dashboard-principal-session:v1:"))
	_, _ = mac.Write([]byte(payloadPart))
	return mac.Sum(nil), nil
}

func (s *CloudServer) dashboardPrincipalSessionKey() ([]byte, error) {
	if len(s.dashboardSessionKey) >= dashboardSessionKeyBytes {
		return s.dashboardSessionKey, nil
	}
	key := make([]byte, dashboardSessionKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate dashboard session key: %w", err)
	}
	s.dashboardSessionKey = key
	return s.dashboardSessionKey, nil
}

func (s *CloudServer) handleDashboardBootstrapPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireLegacyDashboardRecovery(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<html><body><main><h1>Create first managed admin</h1><form method="post" action="/dashboard/bootstrap"><label>Username <input name="username"></label><label>Email <input name="email"></label><label>Display name <input name="display_name"></label><button type="submit">Create admin</button></form></main></body></html>`))
}

func (s *CloudServer) handleDashboardBootstrapSubmit(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireLegacyDashboardRecovery(w, r)
	if !ok {
		return
	}
	store, ok := s.dashboardBootstrapStore(w)
	if !ok {
		return
	}
	// Cap the bootstrap POST body the same way the dashboard login POST body
	// is capped (see dashboard.go's handleLoginSubmit / MaxLoginBodyBytes),
	// instead of calling r.ParseForm() with no size limit.
	r.Body = http.MaxBytesReader(w, r.Body, maxDashboardLoginBodyBytes)
	if err := r.ParseForm(); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, fmt.Sprintf("bootstrap payload too large (max %d bytes)", maxDashboardLoginBodyBytes), http.StatusRequestEntityTooLarge)
			return
		}
		_ = s.recordDashboardBootstrapAudit(r.Context(), store, actor, authAuditOutcomeDenied, "invalid_form", "")
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	if username == "" {
		_ = s.recordDashboardBootstrapAudit(r.Context(), store, actor, authAuditOutcomeDenied, "username_required", "")
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "username is required")
		return
	}
	// Atomic check-and-create: store.CreateFirstAdminHumanUser checks for an
	// existing active admin and creates the new admin within a single
	// transaction (see cloudstore.CreateFirstAdminHumanUser), instead of the
	// old HasActiveAdmin-then-CreateHumanUser sequence, which left a
	// check-then-act (TOCTOU) window where two concurrent bootstrap attempts
	// (dashboard and/or CLI) could both observe "no active admin" and both
	// create a first admin.
	user, err := store.CreateFirstAdminHumanUser(r.Context(), cloudstore.CreateHumanUserParams{Username: username, Email: r.FormValue("email"), DisplayName: r.FormValue("display_name")})
	if err != nil {
		if errors.Is(err, cloudstore.ErrAdminAlreadyExists) {
			_ = s.recordDashboardBootstrapAudit(r.Context(), store, actor, authAuditOutcomeDenied, "managed_admin_exists", "")
			writeActionableError(w, http.StatusConflict, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, "first managed admin already exists")
			return
		}
		_ = s.recordDashboardBootstrapAudit(r.Context(), store, actor, authAuditOutcomeDenied, "create_admin_failed", "")
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("create first admin: %v", err))
		return
	}
	if err := s.recordDashboardBootstrapAudit(r.Context(), store, actor, authAuditOutcomeSuccess, "", user.PrincipalID); err != nil {
		writeAuditFailure(w, err)
		return
	}
	http.Redirect(w, r, "/dashboard/login", http.StatusSeeOther)
}

func (s *CloudServer) requireLegacyDashboardRecovery(w http.ResponseWriter, r *http.Request) (cloudauth.Principal, bool) {
	if err := s.authorizeDashboardRequest(r); err != nil {
		http.Redirect(w, r, dashboardLoginPathWithNextLocal(r.URL.RequestURI()), http.StatusSeeOther)
		return cloudauth.Principal{}, false
	}
	principal, ok := s.dashboardActorPrincipal(r)
	if !ok || principal.Source != cloudauth.PrincipalSourceLegacyEnvAdmin {
		s.recordBootstrapAuditBestEffort(r.Context(), principal, authAuditOutcomeDenied, authAuditReasonLegacyRecoveryRequired, "")
		writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, "forbidden: legacy dashboard recovery credential is required")
		return cloudauth.Principal{}, false
	}
	return principal, true
}

// verifyLegacyDashboardAdminCookie decodes the dashboard session cookie on r
// through the legacy dashboardSessionCodec path and reports whether it
// carries the exact legacy ENGRAM_CLOUD_ADMIN dashboard-admin token. It
// centralizes the decode-cookie + trim-configured-admin-token + hmac.Equal
// check that was previously duplicated across authorizeDashboardRequest,
// isDashboardAdmin, and dashboardActorPrincipal.
func (s *CloudServer) verifyLegacyDashboardAdminCookie(r *http.Request) bool {
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
	return hmac.Equal([]byte(token), []byte(adminToken))
}

// dashboardActorPrincipal resolves the acting principal for a dashboard
// request, covering both signed principal-claim sessions and the
// legacy-admin dashboard session, which is minted through the older
// dashboardSessionCodec path (see dashboardSessionTokenForRequest) and
// therefore never carries a principal in the request context.
func (s *CloudServer) dashboardActorPrincipal(r *http.Request) (cloudauth.Principal, bool) {
	if principal, ok := s.dashboardPrincipalFromRequest(r); ok {
		return principal, true
	}
	if !s.verifyLegacyDashboardAdminCookie(r) {
		return cloudauth.Principal{}, false
	}
	return legacyDashboardAdminAuditPrincipal(), true
}

// recordBootstrapAuditBestEffort audits denied access to the dashboard
// bootstrap/recovery surface. It is best-effort (like sync rejection
// audits) because it only records a rejection that has already happened;
// there is no authoritative mutation to protect by failing closed here.
func (s *CloudServer) recordBootstrapAuditBestEffort(ctx context.Context, actor cloudauth.Principal, outcome, reason, targetPrincipalID string) {
	if s.adminIdentity == nil {
		return
	}
	actorSource := auditActorSource(actor)
	if err := s.adminIdentity.InsertAuthAuditEvent(ctx, cloudstore.AuthAuditEvent{
		ActorPrincipalID:  auditActorPrincipalIDRef(actor),
		ActorSource:       actorSource,
		TargetPrincipalID: strings.TrimSpace(targetPrincipalID),
		Action:            authAuditActionDashboardBootstrap,
		Outcome:           strings.TrimSpace(outcome),
		ReasonCode:        strings.TrimSpace(reason),
		Metadata:          map[string]any{"source": actorSource},
	}); err != nil {
		log.Printf("[engram-cloud] dashboard bootstrap audit insert failed (best-effort): %v", err)
	}
}

func (s *CloudServer) dashboardBootstrapStore(w http.ResponseWriter) (dashboardPrincipalStore, bool) {
	store, ok := s.adminIdentity.(dashboardPrincipalStore)
	if !ok || store == nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, "dashboard bootstrap store is not configured")
		return nil, false
	}
	return store, true
}

func (s *CloudServer) recordDashboardBootstrapAudit(ctx context.Context, store dashboardPrincipalStore, actor cloudauth.Principal, outcome, reason, targetPrincipalID string) error {
	actorSource := auditActorSource(actor)
	return store.InsertAuthAuditEvent(ctx, cloudstore.AuthAuditEvent{ActorPrincipalID: auditActorPrincipalIDRef(actor), ActorSource: actorSource, TargetPrincipalID: strings.TrimSpace(targetPrincipalID), Action: authAuditActionDashboardBootstrap, Outcome: strings.TrimSpace(outcome), ReasonCode: strings.TrimSpace(reason), Metadata: map[string]any{"source": actorSource}})
}

// auditActorSource returns the principal's source string, defaulting to
// authAuditActorSourceUnauthenticated when empty so audit events are never
// dropped by the store's "actor source is required" validation (see
// cloudstore.InsertAuthAuditEvent). Used by every dashboard audit path that
// may record an anonymous/denied actor.
func auditActorSource(actor cloudauth.Principal) string {
	if source := string(actor.Source); source != "" {
		return source
	}
	return authAuditActorSourceUnauthenticated
}

// auditActorPrincipalIDRef returns the principal ID to record as an audit
// event's ActorPrincipalID, or "" when the principal is not an actual row in
// cloud_principals. cloud_auth_audit_log.actor_principal_id is a nullable
// BIGINT REFERENCES cloud_principals(id) (see design.md's persistence
// design and cloudstore's migration), but legacy/bootstrap/unauthenticated
// principals are represented by synthetic, non-numeric sentinel IDs (e.g.
// "legacy:admin", "legacy:sync") that are never persisted as
// cloud_principals rows. Passing one of those sentinels straight through to
// cloudstore.InsertAuthAuditEvent's ActorPrincipalID makes Postgres reject
// the insert outright (invalid bigint literal), which — once
// WithAdminIdentityStore is actually wired into a running server (this
// runtime-wiring slice) — turned a successful legacy admin dashboard login
// into a 500 "unable to create dashboard session" (recordDashboardLoginAudit
// failing closed). Only cloudauth.PrincipalSourceManagedToken principals
// resolve to a real cloud_principals row, so only that source's ID is safe
// to reference here; every other source's actor identity is still fully
// captured in the audit event's ActorSource/Metadata fields, just not as a
// foreign-key-checked column.
func auditActorPrincipalIDRef(actor cloudauth.Principal) string {
	if actor.Source != cloudauth.PrincipalSourceManagedToken {
		return ""
	}
	return strings.TrimSpace(actor.ID)
}

func dashboardLoginPathWithNextLocal(next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return "/dashboard/login"
	}
	return "/dashboard/login?next=" + url.QueryEscape(next)
}
