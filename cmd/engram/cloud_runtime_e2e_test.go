package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/v2/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v2/internal/cloud/cloudstore"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// openIsolatedCloudRuntimeSchema mirrors cloudstore's own
// openIsolatedCloudStore test helper (a fresh Postgres schema per test, via
// search_path) so this end-to-end runtime-wiring test can run repeatedly and
// in parallel with other Postgres-gated suites without cross-test
// interference, while still exercising the real newCloudRuntime constructor
// end to end (not a fake).
func openIsolatedCloudRuntimeSchema(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("CLOUDSTORE_TEST_DSN"))
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping end-to-end runtime wiring test (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN so a per-test search_path can be attached")
	}

	schema := fmt.Sprintf("cloud_runtime_wiring_%d", time.Now().UnixNano())
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	t.Cleanup(func() { _ = adminDB.Close() })
	if _, err := adminDB.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + schema
}

// TestCloudRuntimeWiresManagedTokenAuthEndToEnd is the RED-first end-to-end
// proof for the runtime managed-token wiring slice (design.md migration step
// 3: "Wire resolver to check managed token storage first, then legacy env
// tokens"). It calls the real, unmodified newCloudRuntime constructor (not a
// fake), backed by a real Postgres-backed cloudstore.CloudStore, and drives
// real HTTP requests through the assembled *cloudserver.CloudServer, proving:
//
//   - (a) a managed token bearer request to a managed-admin-gated route
//     succeeds (only possible if the resolved principal has
//     Source == PrincipalSourceManagedToken and Role == RoleAdmin — see
//     requireManagedAdmin);
//   - (b) POST /dashboard/bootstrap no longer 500s for want of a store;
//   - (c) a revoked managed token, and an unknown bearer token, are rejected;
//   - (d) the legacy ENGRAM_CLOUD_TOKEN sync credential still authenticates
//     (compatibility, unaffected by managed-token wiring);
//   - (e) with no dedicated token pepper configured, the server still starts
//     and legacy auth still works (graceful degradation).
func TestCloudRuntimeWiresManagedTokenAuthEndToEnd(t *testing.T) {
	testDSN := openIsolatedCloudRuntimeSchema(t)

	const legacySyncToken = "e2e-legacy-sync-token"
	const legacyAdminToken = "e2e-legacy-admin-token"
	const tokenPepper = "e2e-dedicated-cloud-token-pepper-at-least-32-bytes"

	t.Setenv("ENGRAM_CLOUD_TOKEN", legacySyncToken)
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")

	cfg := cloud.Config{
		DSN:              testDSN,
		JWTSecret:        "e2e-runtime-wiring-jwt-secret-32-bytes-plus",
		AdminToken:       legacyAdminToken,
		AllowedProjects:  []string{"demo-project"},
		TokenPepper:      tokenPepper,
		MaxPushBodyBytes: cloud.DefaultMaxPushBodyBytes,
	}

	rt, err := newCloudRuntime(cfg)
	if err != nil {
		t.Fatalf("newCloudRuntime: %v", err)
	}
	dcr, ok := rt.(*defaultCloudRuntime)
	if !ok {
		t.Fatalf("expected *defaultCloudRuntime, got %T", rt)
	}
	t.Cleanup(func() { _ = dcr.store.Close() })
	handler := dcr.server.Handler()

	ctx := context.Background()
	admin, err := dcr.store.CreateHumanUser(ctx, cloudstore.CreateHumanUserParams{Username: "e2e-admin", Email: "e2e-admin@example.test", DisplayName: "E2E Admin", Role: cloudstore.PrincipalRoleAdmin})
	if err != nil {
		t.Fatalf("seed managed admin: %v", err)
	}
	managedToken, err := cloudauth.GenerateManagedToken("test")
	if err != nil {
		t.Fatalf("GenerateManagedToken: %v", err)
	}
	hasher, err := cloudauth.NewManagedTokenHasher([]byte(tokenPepper))
	if err != nil {
		t.Fatalf("NewManagedTokenHasher: %v", err)
	}
	tokenHash, err := hasher.Hash(managedToken.Raw)
	if err != nil {
		t.Fatalf("hash managed token: %v", err)
	}
	principalToken, err := dcr.store.CreatePrincipalToken(ctx, cloudstore.CreatePrincipalTokenParams{PrincipalID: admin.PrincipalID, TokenPrefix: managedToken.Prefix, TokenHash: tokenHash, Name: "e2e-token"})
	if err != nil {
		t.Fatalf("seed managed token: %v", err)
	}

	// (a) managed token authenticates against a managed-admin-gated route.
	adminUsersStatus, adminUsersBody := doBearerRequest(t, handler, http.MethodGet, "/admin/users", managedToken.Raw)
	if adminUsersStatus != http.StatusOK {
		t.Fatalf("expected managed admin token to reach /admin/users, got status=%d body=%s", adminUsersStatus, adminUsersBody)
	}
	if !strings.Contains(adminUsersBody, "e2e-admin") {
		t.Fatalf("expected /admin/users response to include seeded admin, got %s", adminUsersBody)
	}

	// (d) legacy ENGRAM_CLOUD_TOKEN still authenticates sync routes.
	legacyPullStatus, legacyPullBody := doBearerRequest(t, handler, http.MethodGet, "/sync/pull?project=demo-project", legacySyncToken)
	if legacyPullStatus != http.StatusOK {
		t.Fatalf("expected legacy sync token to authenticate /sync/pull, got status=%d body=%s", legacyPullStatus, legacyPullBody)
	}

	// (b) dashboard bootstrap no longer 500s for want of a store: log in as
	// the legacy admin, then hit /dashboard/bootstrap. Since a managed admin
	// already exists (seeded above), the atomic first-admin guard returns
	// ErrAdminAlreadyExists -> 409, which is only reachable if the
	// dashboard's identity store is actually configured (previously this
	// route unconditionally 500'd with "dashboard bootstrap store is not
	// configured" because newCloudRuntime never wired one in).
	cookie := dashboardLoginCookie(t, handler, legacyAdminToken)
	bootstrapStatus, bootstrapBody := doCookieFormRequest(t, handler, "/dashboard/bootstrap", cookie, url.Values{"username": {"second-admin-attempt"}})
	if bootstrapStatus == http.StatusInternalServerError {
		t.Fatalf("dashboard bootstrap must not 500 for want of a store now that WithAdminIdentityStore is wired, got 500 body=%s", bootstrapBody)
	}
	if bootstrapStatus != http.StatusConflict {
		t.Fatalf("expected 409 (managed admin already exists) proving the bootstrap store is wired, got status=%d body=%s", bootstrapStatus, bootstrapBody)
	}

	// (c) unknown and revoked managed tokens are rejected.
	if status, _ := doBearerRequest(t, handler, http.MethodGet, "/admin/users", "totally-unknown-bearer-token"); status != http.StatusUnauthorized {
		t.Fatalf("expected unknown bearer token to be rejected with 401, got %d", status)
	}
	if err := dcr.store.RevokePrincipalToken(ctx, principalToken.ID, admin.PrincipalID, "e2e-revoke"); err != nil {
		t.Fatalf("revoke managed token: %v", err)
	}
	if status, body := doBearerRequest(t, handler, http.MethodGet, "/admin/users", managedToken.Raw); status != http.StatusUnauthorized {
		t.Fatalf("expected revoked managed token to be rejected with 401, got status=%d body=%s", status, body)
	}

	// (e) graceful degradation: with no dedicated token pepper configured,
	// the server must still start and legacy auth must still work.
	noPepperCfg := cfg
	noPepperCfg.TokenPepper = ""
	noPepperRT, err := newCloudRuntime(noPepperCfg)
	if err != nil {
		t.Fatalf("newCloudRuntime with no token pepper must not fail to start: %v", err)
	}
	noPepperDCR, ok := noPepperRT.(*defaultCloudRuntime)
	if !ok {
		t.Fatalf("expected *defaultCloudRuntime, got %T", noPepperRT)
	}
	t.Cleanup(func() { _ = noPepperDCR.store.Close() })
	noPepperHandler := noPepperDCR.server.Handler()
	if status, body := doBearerRequest(t, noPepperHandler, http.MethodGet, "/sync/pull?project=demo-project", legacySyncToken); status != http.StatusOK {
		t.Fatalf("expected legacy sync token to still authenticate with no token pepper configured, got status=%d body=%s", status, body)
	}
	if status, _ := doBearerRequest(t, noPepperHandler, http.MethodGet, "/admin/users", managedToken.Raw); status != http.StatusUnauthorized {
		t.Fatalf("expected managed token auth to be disabled (not misresolved) with no token pepper configured, got %d", status)
	}
}

func TestCloudRuntimeMutationPushAttributionUsesAuthenticatedPrincipal(t *testing.T) {
	testDSN := openIsolatedCloudRuntimeSchema(t)

	const legacySyncToken = "e2e-attribution-legacy-token"
	const tokenPepper = "e2e-attribution-token-pepper-at-least-32-bytes"
	const project = "attribution-e2e"
	t.Setenv("ENGRAM_CLOUD_TOKEN", legacySyncToken)
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")

	rt, err := newCloudRuntime(cloud.Config{
		DSN:              testDSN,
		JWTSecret:        "e2e-attribution-jwt-secret-32-bytes-plus",
		AllowedProjects:  []string{project},
		TokenPepper:      tokenPepper,
		MaxPushBodyBytes: cloud.DefaultMaxPushBodyBytes,
	})
	if err != nil {
		t.Fatalf("newCloudRuntime: %v", err)
	}
	dcr, ok := rt.(*defaultCloudRuntime)
	if !ok {
		t.Fatalf("expected *defaultCloudRuntime, got %T", rt)
	}
	t.Cleanup(func() { _ = dcr.store.Close() })
	ctx := context.Background()
	managedUser, err := dcr.store.CreateHumanUser(ctx, cloudstore.CreateHumanUserParams{
		Username:    "managed-attribution-user",
		DisplayName: "Managed E2E User",
		Role:        cloudstore.PrincipalRoleMember,
	})
	if err != nil {
		t.Fatalf("CreateHumanUser: %v", err)
	}
	if _, err := dcr.store.CreateProjectGrant(ctx, cloudstore.CreateProjectGrantParams{
		PrincipalID:          managedUser.PrincipalID,
		Project:              project,
		GrantedByPrincipalID: managedUser.PrincipalID,
	}); err != nil {
		t.Fatalf("CreateProjectGrant: %v", err)
	}
	managedToken, err := cloudauth.GenerateManagedToken("test")
	if err != nil {
		t.Fatalf("GenerateManagedToken: %v", err)
	}
	hasher, err := cloudauth.NewManagedTokenHasher([]byte(tokenPepper))
	if err != nil {
		t.Fatalf("NewManagedTokenHasher: %v", err)
	}
	tokenHash, err := hasher.Hash(managedToken.Raw)
	if err != nil {
		t.Fatalf("hash managed token: %v", err)
	}
	if _, err := dcr.store.CreatePrincipalToken(ctx, cloudstore.CreatePrincipalTokenParams{
		PrincipalID: managedUser.PrincipalID,
		TokenPrefix: managedToken.Prefix,
		TokenHash:   tokenHash,
		Name:        "attribution-e2e-token",
	}); err != nil {
		t.Fatalf("CreatePrincipalToken: %v", err)
	}

	body := `{"created_by":"forged-client-identity","entries":[{"project":"attribution-e2e","entity":"session","entity_key":"session-1","op":"upsert","payload":{"id":"session-1","directory":"/tmp/session-1"}}]}`
	unauthorized := httptest.NewRecorder()
	unauthorizedRequest := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", strings.NewReader(body))
	unauthorizedRequest.Header.Set("Content-Type", "application/json")
	dcr.server.Handler().ServeHTTP(unauthorized, unauthorizedRequest)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized mutation push status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}
	manifest, err := dcr.store.ReadManifest(ctx, project)
	if err != nil {
		t.Fatalf("ReadManifest after rejected mutation push: %v", err)
	}
	if len(manifest.Chunks) != 0 {
		t.Fatalf("rejected mutation push materialized %d chunks", len(manifest.Chunks))
	}

	push := httptest.NewRecorder()
	pushRequest := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", strings.NewReader(body))
	pushRequest.Header.Set("Authorization", "Bearer "+managedToken.Raw)
	pushRequest.Header.Set("Content-Type", "application/json")
	dcr.server.Handler().ServeHTTP(push, pushRequest)
	if push.Code != http.StatusOK {
		t.Fatalf("authenticated mutation push status = %d, want %d body=%q", push.Code, http.StatusOK, push.Body.String())
	}

	manifest, err = dcr.store.ReadManifest(ctx, project)
	if err != nil {
		t.Fatalf("ReadManifest after accepted mutation push: %v", err)
	}
	if len(manifest.Chunks) != 1 {
		t.Fatalf("materialized chunks = %d, want 1", len(manifest.Chunks))
	}
	if manifest.Chunks[0].CreatedBy != managedUser.DisplayName {
		t.Fatalf("materialized chunk created_by = %q, want authenticated principal %q", manifest.Chunks[0].CreatedBy, managedUser.DisplayName)
	}
	contributors, err := dcr.store.ListContributors("")
	if err != nil {
		t.Fatalf("ListContributors: %v", err)
	}
	if len(contributors) != 1 || contributors[0].CreatedBy != managedUser.DisplayName {
		t.Fatalf("contributors = %+v, want authenticated principal %q", contributors, managedUser.DisplayName)
	}

	replay := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", strings.NewReader(body))
	replayRequest.Header.Set("Authorization", "Bearer "+managedToken.Raw)
	replayRequest.Header.Set("Content-Type", "application/json")
	dcr.server.Handler().ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusOK {
		t.Fatalf("replayed mutation push status = %d, want %d body=%q", replay.Code, http.StatusOK, replay.Body.String())
	}
	manifest, err = dcr.store.ReadManifest(ctx, project)
	if err != nil {
		t.Fatalf("ReadManifest after replay: %v", err)
	}
	if len(manifest.Chunks) != 1 || manifest.Chunks[0].CreatedBy != managedUser.DisplayName {
		t.Fatalf("replay changed materialized attribution: %+v", manifest.Chunks)
	}

	explicitPush := httptest.NewRecorder()
	explicitPushRequest := httptest.NewRequest(http.MethodPost, "/sync/push", strings.NewReader(`{"project":"attribution-e2e","created_by":"explicit-client-attribution","data":{"sessions":[{"id":"session-explicit","directory":"/tmp/session-explicit"}]}}`))
	explicitPushRequest.Header.Set("Authorization", "Bearer "+managedToken.Raw)
	explicitPushRequest.Header.Set("Content-Type", "application/json")
	dcr.server.Handler().ServeHTTP(explicitPush, explicitPushRequest)
	if explicitPush.Code != http.StatusOK {
		t.Fatalf("explicit chunk push status = %d, want %d body=%q", explicitPush.Code, http.StatusOK, explicitPush.Body.String())
	}
	manifest, err = dcr.store.ReadManifest(ctx, project)
	if err != nil {
		t.Fatalf("ReadManifest after explicit chunk push: %v", err)
	}
	foundExplicitAttribution := false
	for _, chunk := range manifest.Chunks {
		if chunk.CreatedBy == "explicit-client-attribution" {
			foundExplicitAttribution = true
		}
	}
	if !foundExplicitAttribution {
		t.Fatalf("explicit chunk push attribution missing from manifest: %+v", manifest.Chunks)
	}
}

func doBearerRequest(t *testing.T, handler http.Handler, method, path, bearerToken string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

// dashboardLoginCookie logs into /dashboard/login with the given bearer
// token via a real HTTP round trip through handler and returns the minted
// dashboard session cookie for reuse on a follow-up request.
func dashboardLoginCookie(t *testing.T, handler http.Handler, token string) *http.Cookie {
	t.Helper()
	form := url.Values{"token": {token}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	resp := rec.Result()
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "engram_dashboard_token" {
			return cookie
		}
	}
	t.Fatalf("dashboard login did not set a session cookie (status=%d)", rec.Code)
	return nil
}

func doCookieFormRequest(t *testing.T, handler http.Handler, path string, cookie *http.Cookie, form url.Values) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}
