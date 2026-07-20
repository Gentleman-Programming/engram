package cloudserver

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

type managedDashboardPrincipalStore struct {
	fakeStore
	principals map[string]cloudstore.Principal
	getCalls   int
}

func newManagedDashboardPrincipalStore(principals ...cloudstore.Principal) *managedDashboardPrincipalStore {
	store := &managedDashboardPrincipalStore{principals: make(map[string]cloudstore.Principal)}
	for _, principal := range principals {
		store.principals[principal.ID] = principal
	}
	return store
}

func (s *managedDashboardPrincipalStore) GetPrincipal(_ context.Context, id string) (cloudstore.Principal, error) {
	s.getCalls++
	principal, ok := s.principals[strings.TrimSpace(id)]
	if !ok {
		return cloudstore.Principal{}, cloudauth.ErrUnknownToken
	}
	return principal, nil
}

func dashboardManagedPrincipal(id, role string, enabled bool) cloudauth.Principal {
	return cloudauth.Principal{
		ID:          id,
		Kind:        cloudauth.PrincipalKindHuman,
		DisplayName: "Alice Admin",
		Role:        cloudauth.Role(role),
		Enabled:     enabled,
		Source:      cloudauth.PrincipalSourceManagedToken,
		TokenID:     "tok-" + id,
	}
}

func dashboardStoredPrincipal(id, role string, enabled bool) cloudstore.Principal {
	return cloudstore.Principal{
		ID:          id,
		Kind:        cloudstore.PrincipalKindHuman,
		DisplayName: "Alice Admin",
		Role:        role,
		Enabled:     enabled,
		CreatedAt:   time.Date(2026, 7, 3, 14, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 7, 3, 14, 0, 0, 0, time.UTC),
	}
}

func managedDashboardLogin(t *testing.T, srv *CloudServer, token string, https bool) *http.Cookie {
	t.Helper()
	login := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader("token="+token))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if https {
		loginReq.TLS = &tls.ConnectionState{}
	}
	srv.Handler().ServeHTTP(login, loginReq)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("expected managed dashboard login redirect, got %d body=%q", login.Code, login.Body.String())
	}
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == dashboardSessionCookieName {
			return cookie
		}
	}
	t.Fatal("expected managed dashboard login to set session cookie")
	return nil
}

func performDashboardRequest(srv *CloudServer, method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestManagedDashboardAdminLoginSetsSignedCookieAndRevalidatesAccess(t *testing.T) {
	admin := dashboardManagedPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true)
	store := newManagedDashboardPrincipalStore(dashboardStoredPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true))
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"admin-token": admin}}
	srv := New(store, authn, 0, WithPrincipalStateStore(store))

	cookie := managedDashboardLogin(t, srv, "admin-token", true)
	if cookie.Value == "admin-token" || strings.Contains(cookie.Value, "admin-token") {
		t.Fatalf("managed dashboard cookie must be signed claims, not raw token material: %q", cookie.Value)
	}
	if !cookie.HttpOnly {
		t.Fatal("managed dashboard cookie must be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode && cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("managed dashboard cookie must be SameSite=Lax or stronger, got %v", cookie.SameSite)
	}
	if !cookie.Secure {
		t.Fatal("managed dashboard cookie must be Secure for HTTPS requests")
	}

	beforeAdminRequest := store.getCalls
	adminRec := performDashboardRequest(srv, http.MethodGet, "/dashboard/admin", cookie)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("expected managed admin dashboard access to succeed, got %d body=%q", adminRec.Code, adminRec.Body.String())
	}
	if !strings.Contains(adminRec.Body.String(), "ADMIN SURFACE") {
		t.Fatalf("expected admin dashboard content after managed login, body=%q", adminRec.Body.String())
	}
	if got := store.getCalls - beforeAdminRequest; got != 1 {
		t.Fatalf("expected protected dashboard request to revalidate principal state exactly once and reuse request context, got %d revalidations", got)
	}
}

func TestManagedDashboardMemberCannotAccessAdminBehavior(t *testing.T) {
	member := dashboardManagedPrincipal("p-member", cloudstore.PrincipalRoleMember, true)
	store := newManagedDashboardPrincipalStore(dashboardStoredPrincipal("p-member", cloudstore.PrincipalRoleMember, true))
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"member-token": member}}
	srv := New(store, authn, 0, WithPrincipalStateStore(store))

	cookie := managedDashboardLogin(t, srv, "member-token", false)
	home := performDashboardRequest(srv, http.MethodGet, "/dashboard", cookie)
	if home.Code != http.StatusOK {
		t.Fatalf("expected managed member dashboard session to be valid, got %d body=%q", home.Code, home.Body.String())
	}
	adminRec := performDashboardRequest(srv, http.MethodGet, "/dashboard/admin", cookie)
	if adminRec.Code != http.StatusForbidden {
		t.Fatalf("expected managed member admin dashboard request to be forbidden, got %d body=%q", adminRec.Code, adminRec.Body.String())
	}
}

func TestManagedDashboardSessionLosesAccessAfterDisableOrDemotion(t *testing.T) {
	admin := dashboardManagedPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true)
	store := newManagedDashboardPrincipalStore(dashboardStoredPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true))
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"admin-token": admin}}
	srv := New(store, authn, 0, WithPrincipalStateStore(store))

	cookie := managedDashboardLogin(t, srv, "admin-token", false)
	store.principals["p-admin"] = dashboardStoredPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, false)
	disabled := performDashboardRequest(srv, http.MethodGet, "/dashboard", cookie)
	if disabled.Code != http.StatusSeeOther {
		t.Fatalf("expected disabled managed principal to be redirected to login, got %d body=%q", disabled.Code, disabled.Body.String())
	}
	if location := disabled.Header().Get("Location"); location != "/dashboard/login?next=%2Fdashboard" {
		t.Fatalf("expected disabled principal redirect to login preserving next, got %q", location)
	}

	store.principals["p-admin"] = dashboardStoredPrincipal("p-admin", cloudstore.PrincipalRoleAdmin, true)
	cookie = managedDashboardLogin(t, srv, "admin-token", false)
	store.principals["p-admin"] = dashboardStoredPrincipal("p-admin", cloudstore.PrincipalRoleMember, true)
	demoted := performDashboardRequest(srv, http.MethodGet, "/dashboard/admin", cookie)
	if demoted.Code != http.StatusForbidden {
		t.Fatalf("expected demoted managed admin session to lose admin access, got %d body=%q", demoted.Code, demoted.Body.String())
	}
}
