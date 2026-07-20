package cloudserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// legacyAdminOnlyAuthService returns a real cloudauth.Service configured so
// the legacy sync bearer token never matches "legacy-admin-token" (so
// ResolveBearerToken/principal resolution falls through to the
// WithDashboardAdminToken match) while still registering
// "legacy-admin-token" as a valid dashboard session token so the legacy
// admin's old-style codec session cookie can be parsed back on subsequent
// requests, matching the existing TestHandlerDashboardAdminTokenFlow...
// pattern in cloudserver_test.go. Dashboard login is intentionally disabled
// when CloudServer.auth is nil (see routes()), so tests cannot pass a nil
// Authenticator here.
func legacyAdminOnlyAuthService(t *testing.T) *cloudauth.Service {
	t.Helper()
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("unrelated-legacy-sync-token")
	authSvc.SetDashboardSessionTokens([]string{"legacy-admin-token"})
	return authSvc
}

// loginAuditTestStore extends adminTestStore with the principal lookup and
// active-admin check needed by dashboard login/recovery auditing
// (dashboardPrincipalStore), without touching admin_handlers_test.go.
type loginAuditTestStore struct {
	*adminTestStore

	principals        map[string]cloudstore.Principal
	hasActiveAdmin    bool
	hasActiveAdminErr error

	// createFirstAdminMu guards CreateFirstAdminHumanUser's check-then-create
	// critical section, mirroring the real cloudstore advisory-lock-for-the-
	// whole-transaction contract (see cloudstore.CreateFirstAdminHumanUser).
	createFirstAdminMu    sync.Mutex
	createFirstAdminCalls int
}

// CreateFirstAdminHumanUser mirrors cloudstore.CreateFirstAdminHumanUser's
// atomic check-then-create contract for dashboard bootstrap tests: see
// handleDashboardBootstrapSubmit, which MUST call this instead of a separate
// HasActiveAdmin-then-CreateHumanUser sequence.
func (s *loginAuditTestStore) CreateFirstAdminHumanUser(ctx context.Context, params cloudstore.CreateHumanUserParams) (cloudstore.HumanUser, error) {
	s.createFirstAdminMu.Lock()
	defer s.createFirstAdminMu.Unlock()
	s.createFirstAdminCalls++
	if s.hasActiveAdminErr != nil {
		return cloudstore.HumanUser{}, s.hasActiveAdminErr
	}
	if s.hasActiveAdmin {
		return cloudstore.HumanUser{}, cloudstore.ErrAdminAlreadyExists
	}
	user, err := s.CreateHumanUser(ctx, cloudstore.CreateHumanUserParams{
		Username:    params.Username,
		Email:       params.Email,
		DisplayName: params.DisplayName,
		Role:        cloudstore.PrincipalRoleAdmin,
	})
	if err != nil {
		return cloudstore.HumanUser{}, err
	}
	s.hasActiveAdmin = true
	return user, nil
}

func newLoginAuditTestStore() *loginAuditTestStore {
	return &loginAuditTestStore{
		adminTestStore: newAdminTestStore(),
		principals:     make(map[string]cloudstore.Principal),
	}
}

func (s *loginAuditTestStore) GetPrincipal(_ context.Context, id string) (cloudstore.Principal, error) {
	principal, ok := s.principals[strings.TrimSpace(id)]
	if !ok {
		return cloudstore.Principal{}, cloudauth.ErrUnknownToken
	}
	return principal, nil
}

func (s *loginAuditTestStore) HasActiveAdmin(context.Context) (bool, error) {
	if s.hasActiveAdminErr != nil {
		return false, s.hasActiveAdminErr
	}
	return s.hasActiveAdmin, nil
}

func lastAuditEvent(t *testing.T, store *loginAuditTestStore) cloudstore.AuthAuditEvent {
	t.Helper()
	if len(store.auditEvents) == 0 {
		t.Fatal("expected at least one audit event, got none")
	}
	return store.auditEvents[len(store.auditEvents)-1]
}

func assertNoSensitiveAuditMetadata(t *testing.T, event cloudstore.AuthAuditEvent) {
	t.Helper()
	for key, value := range event.Metadata {
		lowerKey := strings.ToLower(key)
		if strings.Contains(lowerKey, "token") || strings.Contains(lowerKey, "bearer") || strings.Contains(lowerKey, "cookie") || strings.Contains(lowerKey, "authorization") || strings.Contains(lowerKey, "hash") {
			t.Fatalf("audit metadata used a sensitive-looking key %q: %+v", key, event.Metadata)
		}
		if str, ok := value.(string); ok {
			lowerVal := strings.ToLower(str)
			if strings.Contains(lowerVal, "egc_live_") || strings.Contains(lowerVal, "bearer ") {
				t.Fatalf("audit metadata leaked raw token-shaped value: %+v", event.Metadata)
			}
		}
	}
}

func performDashboardLogin(t *testing.T, srv *CloudServer, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader("token="+token))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestDashboardAdminLoginAuditsAcceptedManagedPrincipal(t *testing.T) {
	admin := dashboardManagedPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true)
	store := newLoginAuditTestStore()
	store.principals["p-admin"] = dashboardStoredPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true)
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"admin-token": admin}}
	srv := New(store, authn, 0, WithAdminIdentityStore(store))

	rec := performDashboardLogin(t, srv, "admin-token")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected successful login redirect, got %d body=%q", rec.Code, rec.Body.String())
	}

	event := lastAuditEvent(t, store)
	if event.Action != authAuditActionDashboardLogin || event.Outcome != authAuditOutcomeSuccess {
		t.Fatalf("unexpected admin login audit event: %+v", event)
	}
	if event.ActorPrincipalID != "p-admin" || event.ActorSource != string(cloudauth.PrincipalSourceManagedToken) {
		t.Fatalf("expected admin login audit actor to be the managed principal, got %+v", event)
	}
	assertNoSensitiveAuditMetadata(t, event)
}

func TestDashboardAdminLoginAuditsRejectedInvalidToken(t *testing.T) {
	store := newLoginAuditTestStore()
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{}}
	srv := New(store, authn, 0, WithAdminIdentityStore(store))

	rec := performDashboardLogin(t, srv, "not-a-real-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected login page re-render on rejection, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid token") {
		t.Fatalf("expected invalid token message, body=%q", rec.Body.String())
	}

	event := lastAuditEvent(t, store)
	if event.Action != authAuditActionDashboardLogin || event.Outcome != authAuditOutcomeDenied || event.ReasonCode != authAuditReasonInvalidToken {
		t.Fatalf("unexpected rejected admin login audit event: %+v", event)
	}
	// A rejected login has no resolved principal, so it has no valid
	// cloudauth.PrincipalSource. The event must still carry a non-empty
	// actor source or the real cloudstore.InsertAuthAuditEvent would reject
	// it outright ("actor source is required"), silently dropping every
	// rejected-login audit event in production.
	if event.ActorSource == "" {
		t.Fatalf("rejected login audit must carry a non-empty actor source so it is not silently dropped by the real store, got %+v", event)
	}
	if event.ActorSource != authAuditActorSourceUnauthenticated {
		t.Fatalf("expected rejected login audit actor source to be the unauthenticated sentinel %q, got %+v", authAuditActorSourceUnauthenticated, event)
	}
	assertNoSensitiveAuditMetadata(t, event)
}

// TestDashboardManagedAdminLoginFailsClosedOnAuditError proves that a
// successful managed-principal dashboard login never mints a session cookie
// when the audit store cannot record the login: it must fail closed with the
// existing "unable to create dashboard session" 500 response, rather than
// silently minting a session with no audit trail.
func TestDashboardManagedAdminLoginFailsClosedOnAuditError(t *testing.T) {
	admin := dashboardManagedPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true)
	store := newLoginAuditTestStore()
	store.principals["p-admin"] = dashboardStoredPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true)
	store.auditErr = errors.New("audit store unavailable")
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"admin-token": admin}}
	srv := New(store, authn, 0, WithAdminIdentityStore(store))

	rec := performDashboardLogin(t, srv, "admin-token")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected managed admin login to fail closed with 500 when audit insert fails, got %d body=%q", rec.Code, rec.Body.String())
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == dashboardSessionCookieName {
			t.Fatalf("expected no dashboard session cookie to be issued when login audit fails, got cookie=%+v", cookie)
		}
	}
	if len(store.auditEvents) != 0 {
		t.Fatalf("expected no audit event to be persisted (the audit insert itself failed), got %+v", store.auditEvents)
	}
}

// TestDashboardLegacyAdminLoginFailsClosedOnAuditError proves the same
// fail-closed contract holds for the legacy ENGRAM_CLOUD_ADMIN dashboard
// login success path, which shares the same synchronous audit-then-mint
// login flow as managed-principal login.
func TestDashboardLegacyAdminLoginFailsClosedOnAuditError(t *testing.T) {
	store := newLoginAuditTestStore()
	store.auditErr = errors.New("audit store unavailable")
	srv := New(store, legacyAdminOnlyAuthService(t), 0, WithAdminIdentityStore(store), WithDashboardAdminToken("legacy-admin-token"))

	rec := performDashboardLogin(t, srv, "legacy-admin-token")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected legacy admin login to fail closed with 500 when audit insert fails, got %d body=%q", rec.Code, rec.Body.String())
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == dashboardSessionCookieName {
			t.Fatalf("expected no dashboard session cookie to be issued when legacy login audit fails, got cookie=%+v", cookie)
		}
	}
	if len(store.auditEvents) != 0 {
		t.Fatalf("expected no audit event to be persisted (the audit insert itself failed), got %+v", store.auditEvents)
	}
}

func TestDashboardLegacyAdminLoginAuditsRecoveryAfterManagedAdminExists(t *testing.T) {
	store := newLoginAuditTestStore()
	store.hasActiveAdmin = true
	srv := New(store, legacyAdminOnlyAuthService(t), 0, WithAdminIdentityStore(store), WithDashboardAdminToken("legacy-admin-token"))

	rec := performDashboardLogin(t, srv, "legacy-admin-token")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected legacy admin login redirect, got %d body=%q", rec.Code, rec.Body.String())
	}

	event := lastAuditEvent(t, store)
	if event.Action != authAuditActionDashboardLogin || event.Outcome != authAuditOutcomeSuccess {
		t.Fatalf("unexpected legacy admin login audit event: %+v", event)
	}
	if event.ActorSource != string(cloudauth.PrincipalSourceLegacyEnvAdmin) {
		t.Fatalf("expected legacy admin actor source, got %+v", event)
	}
	// The legacy admin principal's ID ("legacy:admin") is a synthetic
	// sentinel, not a real cloud_principals row. cloudstore's real
	// InsertAuthAuditEvent stores ActorPrincipalID as a nullable
	// BIGINT REFERENCES cloud_principals(id); this fake store does not
	// enforce that type, so it would silently accept "legacy:admin" even
	// though the real database rejects it outright. Once
	// WithAdminIdentityStore is wired into a running server (the runtime
	// managed-token wiring slice), that real-database rejection turned every
	// legacy admin dashboard login into a 500 ("unable to create dashboard
	// session") — recordDashboardLoginAudit fails closed. This assertion
	// pins the fix (auditActorPrincipalIDRef) at the real call site.
	if event.ActorPrincipalID != "" {
		t.Fatalf("expected legacy admin actor principal ID to be empty (not a real cloud_principals row), got %+v", event)
	}
	if recovery, ok := event.Metadata["recovery"].(bool); !ok || !recovery {
		t.Fatalf("expected legacy admin login after managed admin exists to be tagged as recovery, got %+v", event.Metadata)
	}
	assertNoSensitiveAuditMetadata(t, event)
}

func TestDashboardLegacyAdminLoginIsNotTaggedRecoveryBeforeManagedAdminExists(t *testing.T) {
	store := newLoginAuditTestStore()
	store.hasActiveAdmin = false
	srv := New(store, legacyAdminOnlyAuthService(t), 0, WithAdminIdentityStore(store), WithDashboardAdminToken("legacy-admin-token"))

	rec := performDashboardLogin(t, srv, "legacy-admin-token")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected legacy admin login redirect, got %d body=%q", rec.Code, rec.Body.String())
	}

	event := lastAuditEvent(t, store)
	if recovery, ok := event.Metadata["recovery"]; ok && recovery == true {
		t.Fatalf("expected legacy admin login before any managed admin exists to not be tagged recovery, got %+v", event.Metadata)
	}
}

// TestDashboardLegacyAdminLoginRecoveryTagUndeterminedOnHasActiveAdminError
// proves that a transient HasActiveAdmin store error does not silently
// downgrade a possible recovery login to a plain, clean non-recovery audit
// event. The event must be tagged with an explicit degraded/undetermined
// indicator instead.
func TestDashboardLegacyAdminLoginRecoveryTagUndeterminedOnHasActiveAdminError(t *testing.T) {
	store := newLoginAuditTestStore()
	store.hasActiveAdminErr = errors.New("active admin lookup unavailable")
	srv := New(store, legacyAdminOnlyAuthService(t), 0, WithAdminIdentityStore(store), WithDashboardAdminToken("legacy-admin-token"))

	rec := performDashboardLogin(t, srv, "legacy-admin-token")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected legacy admin login redirect despite recovery-check error, got %d body=%q", rec.Code, rec.Body.String())
	}

	event := lastAuditEvent(t, store)
	if event.Action != authAuditActionDashboardLogin || event.Outcome != authAuditOutcomeSuccess {
		t.Fatalf("unexpected legacy admin login audit event: %+v", event)
	}
	if recovery, ok := event.Metadata["recovery"]; ok && recovery == false {
		t.Fatalf("expected undetermined recovery status to NOT be reported as a plain recovery:false, got %+v", event.Metadata)
	}
	if failed, ok := event.Metadata["recovery_check_failed"].(bool); !ok || !failed {
		t.Fatalf("expected an explicit recovery_check_failed indicator when HasActiveAdmin errors, got %+v", event.Metadata)
	}
	assertNoSensitiveAuditMetadata(t, event)
}

func TestDashboardBootstrapAuditsAcceptedFirstAdminCreation(t *testing.T) {
	store := newLoginAuditTestStore()
	srv := New(store, legacyAdminOnlyAuthService(t), 0, WithAdminIdentityStore(store), WithDashboardAdminToken("legacy-admin-token"))

	cookie := managedDashboardLogin(t, srv, "legacy-admin-token", false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/bootstrap", strings.NewReader("username=alice&email=alice%40example.com&display_name=Alice"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected first-admin bootstrap redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.createUserCalls != 1 {
		t.Fatalf("expected bootstrap to create exactly one human user, got %d", store.createUserCalls)
	}

	event := lastAuditEvent(t, store)
	if event.Action != authAuditActionDashboardBootstrap || event.Outcome != authAuditOutcomeSuccess {
		t.Fatalf("unexpected bootstrap audit event: %+v", event)
	}
	if event.ActorSource != string(cloudauth.PrincipalSourceLegacyEnvAdmin) {
		t.Fatalf("expected bootstrap actor source to be legacy admin, got %+v", event)
	}
	// See the identical assertion/comment in
	// TestDashboardLegacyAdminLoginAuditsRecoveryAfterManagedAdminExists:
	// the legacy admin's synthetic ID must never be sent as
	// ActorPrincipalID, or the real cloudstore.InsertAuthAuditEvent rejects
	// the insert (invalid bigint literal) once an admin identity store is
	// actually wired into a running server.
	if event.ActorPrincipalID != "" {
		t.Fatalf("expected legacy admin bootstrap actor principal ID to be empty (not a real cloud_principals row), got %+v", event)
	}
	assertNoSensitiveAuditMetadata(t, event)
}

// TestDashboardBootstrapSubmitConcurrentRequestsCreateExactlyOneAdmin is the
// REVIEW REMEDIATION regression test for the dashboard first-admin bootstrap
// TOCTOU race: two concurrent POST /dashboard/bootstrap requests against the
// same store must never both create a first admin. It proves
// handleDashboardBootstrapSubmit goes through the atomic
// CreateFirstAdminHumanUser path (which serializes concurrent callers, see
// loginAuditTestStore.CreateFirstAdminHumanUser) rather than a separate
// HasActiveAdmin-then-CreateHumanUser sequence.
func TestDashboardBootstrapSubmitConcurrentRequestsCreateExactlyOneAdmin(t *testing.T) {
	store := newLoginAuditTestStore()
	srv := New(store, legacyAdminOnlyAuthService(t), 0, WithAdminIdentityStore(store), WithDashboardAdminToken("legacy-admin-token"))
	cookie := managedDashboardLogin(t, srv, "legacy-admin-token", false)
	// Pre-warm the lazily-initialized dashboard principal session signing
	// key single-threaded. dashboardPrincipalSessionKey's lazy
	// generate-on-first-use is an unrelated, pre-existing concurrency gap
	// (not one of the confirmed findings this test targets); resolving it
	// here keeps this test focused on the first-admin bootstrap TOCTOU fix.
	if _, err := srv.dashboardPrincipalSessionKey(); err != nil {
		t.Fatalf("pre-warm dashboard session key: %v", err)
	}

	const attempts = 5
	codes := make(chan int, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/dashboard/bootstrap", strings.NewReader(fmt.Sprintf("username=racer-%d", i)))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(cookie)
			srv.Handler().ServeHTTP(rec, req)
			codes <- rec.Code
		}(i)
	}
	wg.Wait()
	close(codes)

	var redirects, conflicts int
	for code := range codes {
		switch code {
		case http.StatusSeeOther:
			redirects++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected status code from concurrent dashboard bootstrap: %d", code)
		}
	}
	if redirects != 1 {
		t.Fatalf("expected exactly one concurrent dashboard bootstrap request to succeed, got %d redirects and %d conflicts", redirects, conflicts)
	}
	if conflicts != attempts-1 {
		t.Fatalf("expected the other %d requests to be refused as duplicates, got %d", attempts-1, conflicts)
	}
	if store.createUserCalls != 1 {
		t.Fatalf("expected exactly 1 admin user created despite %d concurrent bootstrap requests, got %d", attempts, store.createUserCalls)
	}
	if store.createFirstAdminCalls != attempts {
		t.Fatalf("expected all %d concurrent requests to go through the atomic CreateFirstAdminHumanUser path, got %d calls", attempts, store.createFirstAdminCalls)
	}
}

// TestDashboardMemberLoginIsAuditedUnderRoleNeutralDashboardLoginAction pins
// the audit action semantics: dashboard login is open to member-role
// managed principals (not just admins), so the login audit action must be
// role-neutral (authAuditActionDashboardLogin = "dashboard.login") instead
// of the misleading admin-specific "admin.login" name. Role is already
// carried in the event metadata.
func TestDashboardMemberLoginIsAuditedUnderRoleNeutralDashboardLoginAction(t *testing.T) {
	member := dashboardManagedPrincipal("p-member", cloudstore.PrincipalRoleMember, true)
	store := newLoginAuditTestStore()
	store.principals["p-member"] = dashboardStoredPrincipal("p-member", cloudstore.PrincipalRoleMember, true)
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"member-token": member}}
	srv := New(store, authn, 0, WithAdminIdentityStore(store))

	rec := performDashboardLogin(t, srv, "member-token")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected successful member login redirect, got %d body=%q", rec.Code, rec.Body.String())
	}

	event := lastAuditEvent(t, store)
	if event.Action != authAuditActionDashboardLogin {
		t.Fatalf("expected member dashboard login to be audited under the role-neutral %q action, got %+v", authAuditActionDashboardLogin, event)
	}
	if event.Metadata["role"] != string(cloudauth.RoleMember) {
		t.Fatalf("expected member role to be recorded in audit metadata, got %+v", event.Metadata)
	}
	assertNoSensitiveAuditMetadata(t, event)
}

// TestDashboardBootstrapSubmitRejectsOversizedBody proves the bootstrap POST
// body is capped the same way the dashboard login POST body is capped (see
// TestHandlerDashboardLoginRejectsOversizedFormPayload in cloudserver_test.go),
// instead of calling r.ParseForm() with no size limit.
func TestDashboardBootstrapSubmitRejectsOversizedBody(t *testing.T) {
	store := newLoginAuditTestStore()
	srv := New(store, legacyAdminOnlyAuthService(t), 0, WithAdminIdentityStore(store), WithDashboardAdminToken("legacy-admin-token"))

	cookie := managedDashboardLogin(t, srv, "legacy-admin-token", false)
	oversizedUsername := strings.Repeat("x", int(maxDashboardLoginBodyBytes)+1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/bootstrap", strings.NewReader("username="+oversizedUsername))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized bootstrap payload, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.createUserCalls != 0 {
		t.Fatalf("oversized bootstrap payload must not create a user, got %d calls", store.createUserCalls)
	}
}

func TestDashboardBootstrapAuditsRejectedDuplicateAdmin(t *testing.T) {
	store := newLoginAuditTestStore()
	store.hasActiveAdmin = true
	srv := New(store, legacyAdminOnlyAuthService(t), 0, WithAdminIdentityStore(store), WithDashboardAdminToken("legacy-admin-token"))

	cookie := managedDashboardLogin(t, srv, "legacy-admin-token", false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/bootstrap", strings.NewReader("username=alice"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected duplicate first-admin bootstrap to be rejected with 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.createUserCalls != 0 {
		t.Fatalf("duplicate bootstrap must not create a user, got %d calls", store.createUserCalls)
	}

	event := lastAuditEvent(t, store)
	if event.Action != authAuditActionDashboardBootstrap || event.Outcome != authAuditOutcomeDenied {
		t.Fatalf("unexpected duplicate bootstrap audit event: %+v", event)
	}
	assertNoSensitiveAuditMetadata(t, event)
}

func TestDashboardBootstrapAuditsRejectedNonLegacyRecoveryAccess(t *testing.T) {
	admin := dashboardManagedPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true)
	store := newLoginAuditTestStore()
	store.principals["p-admin"] = dashboardStoredPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true)
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"admin-token": admin}}
	srv := New(store, authn, 0, WithAdminIdentityStore(store))

	cookie := managedDashboardLogin(t, srv, "admin-token", false)
	beforeCalls := store.createUserCalls
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/bootstrap", strings.NewReader("username=mallory"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected non-legacy principal to be forbidden from dashboard recovery access, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.createUserCalls != beforeCalls {
		t.Fatalf("forbidden recovery access must not create a user, before=%d after=%d", beforeCalls, store.createUserCalls)
	}

	event := lastAuditEvent(t, store)
	if event.Action != authAuditActionDashboardBootstrap || event.Outcome != authAuditOutcomeDenied || event.ReasonCode != authAuditReasonLegacyRecoveryRequired {
		t.Fatalf("unexpected forbidden recovery access audit event: %+v", event)
	}
	if event.ActorPrincipalID != "p-admin" || event.ActorSource != string(cloudauth.PrincipalSourceManagedToken) {
		t.Fatalf("expected forbidden recovery audit to record the requesting managed principal, got %+v", event)
	}
	assertNoSensitiveAuditMetadata(t, event)
}

// TestBootstrapAuditBestEffortDefaultsEmptyActorSource proves the dashboard
// bootstrap/recovery denial audit path never drops events when the acting
// principal could not be resolved (an empty cloudauth.Principal{} with no
// Source). Without defaulting to the unauthenticated sentinel, the real
// cloudstore.InsertAuthAuditEvent rejects the empty actor source and the
// best-effort denial audit is silently lost — the same class of bug as the
// rejected-login audit drop. The hardened test store replicates that
// validation, so an appended event proves the default is applied.
func TestBootstrapAuditBestEffortDefaultsEmptyActorSource(t *testing.T) {
	store := newLoginAuditTestStore()
	srv := New(store, legacyAdminOnlyAuthService(t), 0, WithAdminIdentityStore(store), WithDashboardAdminToken("legacy-admin-token"))

	srv.recordBootstrapAuditBestEffort(context.Background(), cloudauth.Principal{}, authAuditOutcomeDenied, authAuditReasonLegacyRecoveryRequired, "")

	event := lastAuditEvent(t, store)
	if event.Action != authAuditActionDashboardBootstrap || event.Outcome != authAuditOutcomeDenied {
		t.Fatalf("unexpected bootstrap denial audit event: %+v", event)
	}
	if event.ActorSource != authAuditActorSourceUnauthenticated {
		t.Fatalf("expected empty-principal bootstrap audit to default to the unauthenticated sentinel %q, got %+v", authAuditActorSourceUnauthenticated, event)
	}
	assertNoSensitiveAuditMetadata(t, event)
}

func TestDashboardLoginAuditIsSkippedWithoutAdminIdentityStore(t *testing.T) {
	admin := dashboardManagedPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true)
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"admin-token": admin}}
	stateStore := newManagedDashboardPrincipalStore(dashboardStoredPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true))
	srv := New(stateStore, authn, 0, WithPrincipalStateStore(stateStore))

	rec := performDashboardLogin(t, srv, "admin-token")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected login to succeed without an admin identity store, got %d body=%q", rec.Code, rec.Body.String())
	}
}
