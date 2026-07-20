package cloudserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// dashboardAdminUsersTestServer builds a CloudServer wired with a managed
// admin dashboard session cookie plus a managed-token hasher, so the
// dashboard-rendered managed-user mutation routes (cloud-user-token-management
// PR4) can be exercised the same way a browser session would use them.
func dashboardAdminUsersTestServer(t *testing.T) (*CloudServer, *loginAuditTestStore, *http.Cookie) {
	t.Helper()
	store := newLoginAuditTestStore()
	store.principals["p-admin"] = dashboardStoredPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true)
	admin := dashboardManagedPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true)
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"admin-token": admin}}
	hasher, err := cloudauth.NewManagedTokenHasher([]byte("test-token-pepper-at-least-32-bytes"))
	if err != nil {
		t.Fatalf("new token hasher: %v", err)
	}
	srv := New(store, authn, 0, WithAdminIdentityStore(store), WithManagedTokenHasher(hasher))
	cookie := managedDashboardLogin(t, srv, "admin-token", false)
	return srv, store, cookie
}

func performDashboardForm(srv *CloudServer, method, path, form string, cookie *http.Cookie, htmx bool) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(form))
	if form != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func dashboardRedirect(rec *httptest.ResponseRecorder) string {
	if loc := rec.Header().Get("Location"); loc != "" {
		return loc
	}
	return rec.Header().Get("HX-Redirect")
}

// TestDashboardCreateManagedUserRequiresManagedAdmin proves member and legacy
// admin dashboard sessions cannot create managed users through the dashboard
// mutation route, and that no state changes for a forbidden request. This
// reuses the exact requireManagedAdmin policy path already tested by
// admin_handlers_test.go — the dashboard route never re-decides authorization.
func TestDashboardCreateManagedUserRequiresManagedAdmin(t *testing.T) {
	store := newLoginAuditTestStore()
	store.principals["p-member"] = dashboardStoredPrincipal("p-member", cloudstore.PrincipalRoleMember, true)
	member := dashboardManagedPrincipal("p-member", cloudstore.PrincipalRoleMember, true)
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"member-token": member}}
	srv := New(store, authn, 0, WithAdminIdentityStore(store))
	cookie := managedDashboardLogin(t, srv, "member-token", false)

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users", "username=mallory&role=member", cookie, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden for managed member dashboard user create, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.createUserCalls != 0 {
		t.Fatalf("forbidden dashboard request must not create a user, got %d calls", store.createUserCalls)
	}
	// The member's own dashboard.login audit event is expected (login is a
	// separate, already-authorized action); the forbidden mutation attempt
	// itself must not add a user.create success audit event.
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionUserCreate {
			t.Fatalf("forbidden dashboard request must not record a user.create audit event, got %+v", event)
		}
	}
}

// TestDashboardCreateManagedUserSucceedsRedirectsAndAudits proves a managed
// admin dashboard session can create a managed user via the dashboard form,
// is redirected back to the managed users shell, and the mutation is
// audited via the same recordAdminAudit path used by the JSON /admin/* API.
func TestDashboardCreateManagedUserSucceedsRedirectsAndAudits(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users", "username=newuser&role=member&display_name=New+User", cookie, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect after managed user create, got %d body=%q", rec.Code, rec.Body.String())
	}
	if dashboardRedirect(rec) != "/dashboard/admin/users" {
		t.Fatalf("expected redirect to /dashboard/admin/users, got %q", dashboardRedirect(rec))
	}
	if store.createUserCalls != 1 {
		t.Fatalf("expected exactly one CreateHumanUser call, got %d", store.createUserCalls)
	}
	event := lastAuditEvent(t, store)
	if event.Action != authAuditActionUserCreate || event.Outcome != authAuditOutcomeSuccess {
		t.Fatalf("unexpected audit event for dashboard user create: %+v", event)
	}
	if event.ActorPrincipalID != "p-admin" {
		t.Fatalf("expected dashboard-created-user audit actor to be the managed admin, got %+v", event)
	}
	assertNoSensitiveAuditMetadata(t, event)

	// HTMX variant returns HX-Redirect instead of a 303 Location header.
	htmxRec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users", "username=htmxuser&role=member", cookie, true)
	if htmxRec.Code != http.StatusOK {
		t.Fatalf("expected 200 with HX-Redirect for HTMX managed user create, got %d body=%q", htmxRec.Code, htmxRec.Body.String())
	}
	if htmxRec.Header().Get("HX-Redirect") != "/dashboard/admin/users" {
		t.Fatalf("expected HX-Redirect header for HTMX managed user create, got %q", htmxRec.Header().Get("HX-Redirect"))
	}
}

// TestDashboardEnableDisableManagedUserRedirectsAndAudits proves the
// enable/disable dashboard forms mutate state, redirect, and audit using the
// same action names as the JSON /admin/* API.
func TestDashboardEnableDisableManagedUserRedirectsAndAudits(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target", Role: "member", Enabled: true})

	disableRec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-target/disable", "", cookie, false)
	if disableRec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect after disable, got %d body=%q", disableRec.Code, disableRec.Body.String())
	}
	if dashboardRedirect(disableRec) != "/dashboard/admin/users" {
		t.Fatalf("expected redirect to /dashboard/admin/users after disable, got %q", dashboardRedirect(disableRec))
	}
	if store.users[0].Enabled {
		t.Fatalf("expected user Enabled=false after disable")
	}
	disableEvent := lastAuditEvent(t, store)
	if disableEvent.Action != authAuditActionUserDisable {
		t.Fatalf("expected user.disable audit action, got %+v", disableEvent)
	}

	enableRec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-target/enable", "", cookie, false)
	if enableRec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect after enable, got %d body=%q", enableRec.Code, enableRec.Body.String())
	}
	if !store.users[0].Enabled {
		t.Fatalf("expected user Enabled=true after enable")
	}
	enableEvent := lastAuditEvent(t, store)
	if enableEvent.Action != authAuditActionUserEnable {
		t.Fatalf("expected user.enable audit action, got %+v", enableEvent)
	}
}

// TestDashboardManagedUserDetailPageRendersTokensAndGrants proves the detail
// page renders the target user, its tokens, and its grants, and requires a
// valid dashboard session (redirect to login otherwise).
func TestDashboardManagedUserDetailPageRendersTokensAndGrants(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target-user", Role: "member", Enabled: true})
	store.tokens = append(store.tokens, cloudstore.PrincipalToken{ID: "tok-1", PrincipalID: "p-target", TokenPrefix: "egc_live_abc", Name: "laptop"})
	store.grants = append(store.grants, cloudstore.ProjectGrant{PrincipalID: "p-target", Project: "alpha"})

	rec := performDashboardRequest(srv, http.MethodGet, "/dashboard/admin/users/p-target", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for managed user detail page, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{"target-user", "egc_live_abc", "laptop", "alpha"} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected %q in managed user detail page, body=%q", marker, body[:min(len(body), 1500)])
		}
	}

	// No session cookie -> redirect to login, mirroring the dashboard package's requireSession behavior.
	noCookieRec := performDashboardRequest(srv, http.MethodGet, "/dashboard/admin/users/p-target", nil)
	if noCookieRec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect to login without a session cookie, got %d body=%q", noCookieRec.Code, noCookieRec.Body.String())
	}
}

func TestDashboardManagedUserDetailPageDisabledUserHidesTokenCreateForm(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-disabled", Username: "disabled-user", Role: "member", Enabled: false})

	rec := performDashboardRequest(srv, http.MethodGet, "/dashboard/admin/users/p-disabled", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for disabled managed user detail page, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `action="/dashboard/admin/users/p-disabled/tokens"`) || strings.Contains(body, "Create Token") {
		t.Fatalf("disabled managed user detail page must not present active token creation affordance, body=%q", body)
	}
	if !strings.Contains(body, "Enable this managed user before creating a managed token.") {
		t.Fatalf("expected inline disabled-user token creation guidance, body=%q", body)
	}
}

// TestDashboardCreateManagedTokenShowsRawTokenOnceAndSanitizesFutureViews
// proves the show-once contract end to end: the token-created response body
// contains the raw token exactly once, is a direct POST response (never a
// redirect, so the raw token is never placed in a URL/Location header/browser
// history entry), and the raw token/hash never appear on the detail page
// rendered afterward.
func TestDashboardCreateManagedTokenShowsRawTokenOnceAndSanitizesFutureViews(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target-user", Role: "member", Enabled: true})

	createRec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-target/tokens", "name=laptop", cookie, false)
	if createRec.Code != http.StatusOK {
		t.Fatalf("expected 200 direct render (not a redirect) for token creation, got %d body=%q", createRec.Code, createRec.Body.String())
	}
	if loc := createRec.Header().Get("Location"); loc != "" {
		t.Fatalf("token creation must never redirect (would leak the raw token via Location/history), got Location=%q", loc)
	}
	if store.createTokenCalls != 1 {
		t.Fatalf("expected exactly one CreatePrincipalToken call, got %d", store.createTokenCalls)
	}
	if len(store.tokens) != 1 {
		t.Fatalf("expected exactly one persisted token, got %d", len(store.tokens))
	}
	rawTokenBody := createRec.Body.String()
	if !strings.Contains(rawTokenBody, "egc_live_") {
		t.Fatalf("expected show-once page to contain the raw managed token, body=%q", rawTokenBody)
	}

	event := lastAuditEvent(t, store)
	if event.Action != authAuditActionTokenCreate {
		t.Fatalf("expected token.create audit action, got %+v", event)
	}
	// token_prefix is safe, documented audit metadata for token.create (see
	// design.md's audit model example); assertNoSensitiveAuditMetadata is
	// deliberately not used here since it flags any "token"-containing key
	// name. What must never appear in metadata is the token's hash or its
	// full raw secret — only the short, non-secret prefix and the operator
	// label are recorded.
	prefix, _ := event.Metadata["token_prefix"].(string)
	if prefix == "" || prefix != store.tokens[0].TokenPrefix {
		t.Fatalf("expected token.create metadata to carry the safe token_prefix, got %+v", event.Metadata)
	}
	if !strings.Contains(rawTokenBody, prefix) {
		t.Fatalf("expected the show-once page to contain the token prefix %q as well as the raw secret", prefix)
	}

	// The safe, short, non-secret prefix legitimately reappears in every
	// later view (list/detail rows show it for operator identification/revoke).
	// What must never reappear is the raw secret suffix from the show-once
	// page. Extract it from the <pre class="token-reveal"> block: the raw
	// token is "<prefix>_<secret>", so strip the known-safe prefix to isolate
	// the secret that must never be shown again.
	revealMatch := tokenRevealPattern.FindStringSubmatch(rawTokenBody)
	if len(revealMatch) != 2 {
		t.Fatalf("expected a token-reveal block containing the raw token, body=%q", rawTokenBody)
	}
	rawToken := revealMatch[1]
	if !strings.HasPrefix(rawToken, prefix+"_") {
		t.Fatalf("expected raw token %q to start with prefix %q", rawToken, prefix+"_")
	}
	secret := strings.TrimPrefix(rawToken, prefix+"_")
	if secret == "" {
		t.Fatalf("expected a non-empty raw token secret, raw token=%q", rawToken)
	}

	detailRec := performDashboardRequest(srv, http.MethodGet, "/dashboard/admin/users/p-target", cookie)
	detailBody := detailRec.Body.String()
	if strings.Contains(detailBody, secret) {
		t.Fatalf("detail page must never render the raw token secret again (show-once contract), body=%q", detailBody)
	}
	if !strings.Contains(detailBody, prefix) {
		t.Errorf("expected the safe token prefix %q to still be visible on the detail page for identification/revoke, body=%q", prefix, detailBody)
	}
}

// tokenRevealPattern extracts the raw token from the show-once
// <pre class="token-reveal">...</pre> block rendered by
// dashboard.ManagedUserTokenCreatedPage.
var tokenRevealPattern = regexp.MustCompile(`<pre class="token-reveal">([^<]+)</pre>`)

// TestDashboardRevokeManagedTokenRedirectsAndAudits proves the revoke-token
// dashboard form mutates state, redirects to the user detail page, and
// audits using the same token.revoke action as the JSON /admin/* API.
func TestDashboardRevokeManagedTokenRedirectsAndAudits(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target-user", Role: "member", Enabled: true})
	store.tokens = append(store.tokens, cloudstore.PrincipalToken{ID: "tok-1", PrincipalID: "p-target", TokenPrefix: "egc_live_abc", Name: "laptop"})

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/tokens/tok-1/revoke", "reason=lost", cookie, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect after token revoke, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.revokeTokenCalls != 1 {
		t.Fatalf("expected exactly one RevokePrincipalToken call, got %d", store.revokeTokenCalls)
	}
	event := lastAuditEvent(t, store)
	if event.Action != authAuditActionTokenRevoke {
		t.Fatalf("expected token.revoke audit action, got %+v", event)
	}
}

// TestDashboardCreateAndRevokeManagedGrantRedirectsAndAudits proves the
// project-grant create/revoke dashboard forms mutate state, redirect, and
// audit using the same grant.create/grant.revoke actions as the JSON API.
func TestDashboardCreateAndRevokeManagedGrantRedirectsAndAudits(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target-user", Role: "member", Enabled: true})

	createRec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-target/grants", "project=alpha", cookie, false)
	if createRec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect after grant create, got %d body=%q", createRec.Code, createRec.Body.String())
	}
	if store.createGrantCalls != 1 {
		t.Fatalf("expected exactly one CreateProjectGrant call, got %d", store.createGrantCalls)
	}
	createEvent := lastAuditEvent(t, store)
	if createEvent.Action != authAuditActionGrantCreate {
		t.Fatalf("expected grant.create audit action, got %+v", createEvent)
	}

	revokeRec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-target/grants/alpha/revoke", "", cookie, false)
	if revokeRec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect after grant revoke, got %d body=%q", revokeRec.Code, revokeRec.Body.String())
	}
	if store.revokeGrantCalls != 1 {
		t.Fatalf("expected exactly one RevokeProjectGrant call, got %d", store.revokeGrantCalls)
	}
	revokeEvent := lastAuditEvent(t, store)
	if revokeEvent.Action != authAuditActionGrantRevoke {
		t.Fatalf("expected grant.revoke audit action, got %+v", revokeEvent)
	}
}

// TestDashboardManagedUserMutationsRequireSession proves an unauthenticated
// request to a dashboard-owned managed-user mutation route is redirected to
// login rather than performing the mutation, for both plain and HTMX requests.
func TestDashboardManagedUserMutationsRequireSession(t *testing.T) {
	srv, store, _ := dashboardAdminUsersTestServer(t)

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users", "username=nope&role=member", nil, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect to login without a session cookie, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(dashboardRedirect(rec), "/dashboard/login") {
		t.Fatalf("expected redirect target to be the login page, got %q", dashboardRedirect(rec))
	}

	htmxRec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users", "username=nope&role=member", nil, true)
	if htmxRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with HX-Redirect without a session cookie, got %d body=%q", htmxRec.Code, htmxRec.Body.String())
	}
	if !strings.HasPrefix(htmxRec.Header().Get("HX-Redirect"), "/dashboard/login") {
		t.Fatalf("expected HX-Redirect to the login page, got %q", htmxRec.Header().Get("HX-Redirect"))
	}

	if store.createUserCalls != 0 {
		t.Fatalf("unauthenticated request must not create a user, got %d calls", store.createUserCalls)
	}
}

// ─── PR4 review remediation ────────────────────────────────────────────────

// TestDashboardManagedUserDetailPageReturns404ForUnknownPrincipal is the RED
// test for FIX A: an unknown/stale principalID must respond 404, not 200.
// handleDashboardManagedUserDetail previously rendered the "Managed User Not
// Found" EmptyState via renderDashboardAdminComponent, which never sets a
// status code, so the response defaulted to 200.
func TestDashboardManagedUserDetailPageReturns404ForUnknownPrincipal(t *testing.T) {
	srv, _, cookie := dashboardAdminUsersTestServer(t)

	rec := performDashboardRequest(srv, http.MethodGet, "/dashboard/admin/users/p-does-not-exist", cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown managed user detail page, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Managed User Not Found") {
		t.Fatalf("expected not-found body content, got %q", rec.Body.String())
	}
}

// TestDashboardCreateManagedTokenForUnknownUserReturns404NoMintNoAudit is the
// RED test for FIX B: handleDashboardCreateManagedToken previously fell
// through to a zero-value cloudstore.HumanUser{} when principalID matched no
// user, then still minted and rendered a real token for that non-existent
// principal. The target principal must be verified BEFORE minting; an
// unknown principal must 404 with no token created and no success audit.
func TestDashboardCreateManagedTokenForUnknownUserReturns404NoMintNoAudit(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-does-not-exist/tokens", "name=laptop", cookie, false)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for token create against an unknown user, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.createTokenCalls != 0 {
		t.Fatalf("expected no CreatePrincipalToken call for an unknown user, got %d calls", store.createTokenCalls)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("expected no token to be persisted for an unknown user, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("expected no token.create audit event for an unknown user, got %+v", event)
		}
	}
}

func TestDashboardCreateManagedTokenRejectsDisabledUserWithStyledErrorWithoutMinting(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-disabled", Username: "disabled-user", Role: "member", Enabled: false})

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-disabled/tokens", "name=laptop", cookie, false)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict for disabled managed user token create, got %d body=%q", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("expected disabled-user rejection to render a dashboard HTML page, got Content-Type=%q body=%q", contentType, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{"<!doctype html>", "Engram Cloud", "Cannot Create Token", "Enable this managed user before creating a managed token.", "disabled-user"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("expected styled disabled-user error page marker %q, body=%q", marker, body)
		}
	}
	if strings.Contains(body, "egc_live_") || strings.Contains(body, "token-reveal") {
		t.Fatalf("disabled-user rejection must not render raw token material, body=%q", body)
	}
	if store.createTokenCalls != 0 {
		t.Fatalf("disabled-user rejection must happen before CreatePrincipalToken, got %d calls", store.createTokenCalls)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("disabled-user rejection must not persist token metadata, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("disabled-user rejection must not record token.create success audit, got %+v", event)
		}
	}
}

func TestDashboardCreateManagedTokenListUsersFailureFailsClosedBeforeMinting(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.listHumanUsersErr = errors.New("list unavailable")

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-target/tokens", "name=laptop", cookie, false)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when user listing fails before token create, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "egc_live_") || strings.Contains(body, "token-reveal") {
		t.Fatalf("list failure must not render raw token material, body=%q", body)
	}
	if store.createTokenCalls != 0 {
		t.Fatalf("list failure must happen before CreatePrincipalToken, got %d calls", store.createTokenCalls)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("list failure must not persist token metadata, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("list failure must not record token.create success audit, got %+v", event)
		}
	}
}

func TestDashboardCreateManagedTokenStoreDisabledRaceReturns409WithoutPersistingOrAuditing(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-race", Username: "race", Role: "member", Enabled: true})
	store.createTokenErr = cloudstore.ErrPrincipalDisabled

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-race/tokens", "name=laptop", cookie, false)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when store rejects disabled principal after pre-check, got %d body=%q", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("expected store-level disabled rejection to render a dashboard HTML page, got Content-Type=%q body=%q", contentType, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{"<!doctype html>", "Engram Cloud", "Cannot Create Token", "Enable this managed user before creating a managed token.", "race"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("expected styled store-level disabled error page marker %q, body=%q", marker, body)
		}
	}
	if strings.Contains(body, "egc_live_") || strings.Contains(body, "token-reveal") {
		t.Fatalf("store-level disabled rejection must not render raw token material, body=%q", body)
	}
	if store.createTokenCalls != 1 {
		t.Fatalf("expected store boundary to be reached once for race simulation, got %d calls", store.createTokenCalls)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("store-level disabled rejection must not persist token metadata, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("store-level disabled rejection must not record token.create success audit, got %+v", event)
		}
	}
}

func TestDashboardCreateManagedTokenStoreNotFoundRaceReturns404WithoutPersistingOrAuditing(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-race", Username: "race", Role: "member", Enabled: true})
	store.createTokenErr = cloudstore.ErrPrincipalNotFound

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-race/tokens", "name=laptop", cookie, false)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when store cannot find principal after pre-check, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "egc_live_") || strings.Contains(body, "token-reveal") {
		t.Fatalf("store-level not-found rejection must not render raw token material, body=%q", body)
	}
	if store.createTokenCalls != 1 {
		t.Fatalf("expected store boundary to be reached once for race simulation, got %d calls", store.createTokenCalls)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("store-level not-found rejection must not persist token metadata, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("store-level not-found rejection must not record token.create success audit, got %+v", event)
		}
	}
}

func TestDashboardCreateManagedTokenAuditFailureRollsBackTokenAndDoesNotRenderRawToken(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target", Role: "member", Enabled: true})
	store.auditErr = errors.New("audit unavailable")

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-target/tokens", "name=laptop", cookie, false)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected audit failure to fail token creation, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "egc_live_") || strings.Contains(body, "token-reveal") {
		t.Fatalf("audit failure must not render show-once token material, body=%q", body)
	}
	if store.createTokenCalls != 1 {
		t.Fatalf("expected atomic token create to be attempted once, got %d calls", store.createTokenCalls)
	}
	if store.revokeTokenCalls != 0 {
		t.Fatalf("token audit atomicity must not rely on compensation revoke, got %d revoke calls", store.revokeTokenCalls)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("audit failure must roll back token persistence, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("audit failure must not record token.create success audit, got %+v", event)
		}
	}
}

// TestDashboardDisableManagedUserSurfacesStoreErrorWithoutAudit is part of
// FIX C: a generic store failure on disable must surface as an error
// response (not 200/303) with no user.disable success audit recorded.
func TestDashboardDisableManagedUserSurfacesStoreErrorWithoutAudit(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target", Role: "member", Enabled: true})
	store.setEnabledErr = errors.New("store unavailable")

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-target/disable", "", cookie, false)
	if rec.Code == http.StatusSeeOther || rec.Code == http.StatusOK {
		t.Fatalf("expected disable to fail when the store errors, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !store.users[0].Enabled {
		t.Fatalf("expected user to remain enabled after a failed disable")
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionUserDisable {
			t.Fatalf("expected no user.disable audit event on store failure, got %+v", event)
		}
	}
}

// TestDashboardDisableLastActiveAdminSurfacesErrorNotSuccess is the
// CRITICAL last-admin case in FIX C: disabling the last active admin through
// the dashboard must surface cloudstore.ErrLastActiveAdmin as an error to
// the user (never a 200/303 success), with no false success audit.
func TestDashboardDisableLastActiveAdminSurfacesErrorNotSuccess(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.setEnabledErr = cloudstore.ErrLastActiveAdmin

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-admin/disable", "", cookie, false)
	if rec.Code == http.StatusSeeOther || rec.Code == http.StatusOK {
		t.Fatalf("expected disabling the last active admin to be rejected, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), cloudstore.ErrLastActiveAdmin.Error()) {
		t.Fatalf("expected the error body to mention the last-active-admin guard, got %q", rec.Body.String())
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionUserDisable {
			t.Fatalf("expected no user.disable audit event when disabling the last active admin, got %+v", event)
		}
	}
}

// TestDashboardCreateManagedTokenSurfacesStoreErrorWithoutAudit is part of
// FIX C: a store failure during token creation must not mint/persist a
// token nor record a success audit.
func TestDashboardCreateManagedTokenSurfacesStoreErrorWithoutAudit(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target", Role: "member", Enabled: true})
	store.createTokenErr = errors.New("token store unavailable")

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-target/tokens", "name=laptop", cookie, false)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected token create to fail when the store errors, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(store.tokens) != 0 {
		t.Fatalf("expected no token to be persisted on store failure, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("expected no token.create audit event on store failure, got %+v", event)
		}
	}
}

// TestDashboardRevokeManagedTokenSurfacesStoreErrorWithoutAudit is part of
// FIX C: a store failure during token revoke must not record a success
// audit and must leave the token unrevoked.
func TestDashboardRevokeManagedTokenSurfacesStoreErrorWithoutAudit(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.tokens = append(store.tokens, cloudstore.PrincipalToken{ID: "tok-1", PrincipalID: "p-target", TokenPrefix: "egc_live_abc"})
	store.revokeTokenErr = errors.New("revoke unavailable")

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/tokens/tok-1/revoke", "reason=lost", cookie, false)
	if rec.Code == http.StatusOK || rec.Code == http.StatusSeeOther {
		t.Fatalf("expected revoke to fail when the store errors, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.tokens[0].RevokedAt != nil {
		t.Fatalf("expected the token to remain unrevoked on store failure")
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenRevoke {
			t.Fatalf("expected no token.revoke audit event on store failure, got %+v", event)
		}
	}
}

// TestDashboardCreateManagedGrantSurfacesStoreErrorWithoutAudit is part of
// FIX C: a store failure during grant create must not record a success
// audit.
func TestDashboardCreateManagedGrantSurfacesStoreErrorWithoutAudit(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target", Role: "member", Enabled: true})
	store.createGrantErr = errors.New("grant store unavailable")

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-target/grants", "project=alpha", cookie, false)
	if rec.Code == http.StatusSeeOther || rec.Code == http.StatusOK {
		t.Fatalf("expected grant create to fail when the store errors, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(store.grants) != 0 {
		t.Fatalf("expected no grant to be persisted on store failure, got %+v", store.grants)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionGrantCreate {
			t.Fatalf("expected no grant.create audit event on store failure, got %+v", event)
		}
	}
}

// TestDashboardRevokeManagedGrantSurfacesStoreErrorWithoutAudit is part of
// FIX C: a store failure during grant revoke must not record a success
// audit and must leave the grant in place.
func TestDashboardRevokeManagedGrantSurfacesStoreErrorWithoutAudit(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.grants = append(store.grants, cloudstore.ProjectGrant{PrincipalID: "p-target", Project: "alpha"})
	store.revokeGrantErr = errors.New("grant revoke unavailable")

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-target/grants/alpha/revoke", "", cookie, false)
	if rec.Code == http.StatusSeeOther || rec.Code == http.StatusOK {
		t.Fatalf("expected grant revoke to fail when the store errors, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(store.grants) != 1 {
		t.Fatalf("expected the grant to remain in place on store failure, got %+v", store.grants)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionGrantRevoke {
			t.Fatalf("expected no grant.revoke audit event on store failure, got %+v", event)
		}
	}
}

// TestDashboardCreateManagedUserRejectsOversizedBody is the RED test for
// FIX D: dashboard mutation POST forms must cap the request body the same
// way handleDashboardBootstrapSubmit and dashboard.go's handleLoginSubmit
// already do, instead of calling r.ParseForm() with no size limit.
func TestDashboardCreateManagedUserRejectsOversizedBody(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	oversized := "username=" + strings.Repeat("x", int(maxDashboardLoginBodyBytes)+1)

	rec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users", oversized, cookie, false)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for an oversized managed-user create payload, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.createUserCalls != 0 {
		t.Fatalf("oversized payload must not create a user, got %d calls", store.createUserCalls)
	}
}

// TestDashboardManagedUserDetailHidesRevokeFormForRevokedTokens is a WARNING
// coverage addition (FIX E): a revoked token must render a "Revoked"
// indicator and must NOT render a revoke form/button.
func TestDashboardManagedUserDetailHidesRevokeFormForRevokedTokens(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target-user", Role: "member", Enabled: true})
	revokedAt := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	store.tokens = append(store.tokens, cloudstore.PrincipalToken{ID: "tok-1", PrincipalID: "p-target", TokenPrefix: "egc_live_abc", Name: "laptop", RevokedAt: &revokedAt})

	rec := performDashboardRequest(srv, http.MethodGet, "/dashboard/admin/users/p-target", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for managed user detail page, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Revoked") {
		t.Fatalf("expected a Revoked status indicator for the revoked token, body=%q", body)
	}
	if strings.Contains(body, "/dashboard/admin/tokens/tok-1/revoke") {
		t.Fatalf("expected no revoke form/action for an already-revoked token, body=%q", body)
	}
}

// TestDashboardLegacyAdminCanViewButNotMutateManagedUsers is a WARNING
// coverage addition (FIX E): a legacy/bootstrap-admin dashboard session can
// read the managed user detail page (lenient isDashboardAdmin check) but is
// forbidden from mutating (requireManagedAdmin), proving the dual-tier
// authorization boundary.
func TestDashboardLegacyAdminCanViewButNotMutateManagedUsers(t *testing.T) {
	store := newLoginAuditTestStore()
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target-user", Role: "member", Enabled: true})
	hasher, err := cloudauth.NewManagedTokenHasher([]byte("test-token-pepper-at-least-32-bytes"))
	if err != nil {
		t.Fatalf("new token hasher: %v", err)
	}
	srv := New(store, legacyAdminOnlyAuthService(t), 0, WithAdminIdentityStore(store), WithManagedTokenHasher(hasher), WithDashboardAdminToken("legacy-admin-token"))
	cookie := managedDashboardLogin(t, srv, "legacy-admin-token", false)

	detailRec := performDashboardRequest(srv, http.MethodGet, "/dashboard/admin/users/p-target", cookie)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("expected legacy admin dashboard session to view the managed user detail page, got %d body=%q", detailRec.Code, detailRec.Body.String())
	}

	createTokenRec := performDashboardForm(srv, http.MethodPost, "/dashboard/admin/users/p-target/tokens", "name=laptop", cookie, false)
	if createTokenRec.Code != http.StatusForbidden {
		t.Fatalf("expected legacy admin dashboard session to be forbidden from a managed-token mutation, got %d body=%q", createTokenRec.Code, createTokenRec.Body.String())
	}
	if store.createTokenCalls != 0 {
		t.Fatalf("forbidden mutation must not create a token, got %d calls", store.createTokenCalls)
	}
}

// TestDashboardAdminUsersListRouteNotShadowedByDetailWildcard is a WARNING
// coverage addition (FIX E): through the full composed CloudServer mux (with
// dashboard.Mount plus the manually-registered
// /dashboard/admin/users/{principalID}), GET /dashboard/admin/users/list
// must still route to the list partial, not the detail wildcard — locking
// in literal-vs-wildcard ServeMux precedence.
func TestDashboardAdminUsersListRouteNotShadowedByDetailWildcard(t *testing.T) {
	srv, store, cookie := dashboardAdminUsersTestServer(t)
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target-user", Role: "member", Enabled: true})

	rec := performDashboardRequest(srv, http.MethodGet, "/dashboard/admin/users/list", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for the managed users list partial, got %d body=%q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Managed User Not Found") {
		t.Fatalf("expected /dashboard/admin/users/list to route to the list partial, not the detail not-found branch, body=%q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "target-user") {
		t.Fatalf("expected the managed users list partial to render known users, body=%q", rec.Body.String())
	}
}
