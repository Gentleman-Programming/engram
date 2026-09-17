package cloudserver

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/v2/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v2/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v2/internal/cloud/dashboard"
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

type dashboardPrincipalGrantAuthorizer struct {
	grants map[string][]string
	err    error
}

func (a *dashboardPrincipalGrantAuthorizer) AuthorizeProjectForPrincipal(_ context.Context, _ cloudauth.Principal, _ string) error {
	return nil
}

func (a *dashboardPrincipalGrantAuthorizer) EnrolledProjectsForPrincipal(_ context.Context, principal cloudauth.Principal) ([]string, error) {
	if a.err != nil {
		return nil, a.err
	}
	return append([]string(nil), a.grants[principal.ID]...), nil
}

type dashboardPrincipalHTTPStore struct {
	*managedDashboardPrincipalStore
	allowed     map[string]struct{}
	syncToggles int
}

func (s *dashboardPrincipalHTTPStore) scoped(projects []string) dashboard.DashboardStore {
	view := *s
	view.allowed = make(map[string]struct{}, len(projects))
	for _, project := range projects {
		view.allowed[project] = struct{}{}
	}
	return &view
}

func (s *dashboardPrincipalHTTPStore) projectAllowed(project string) error {
	if s.allowed == nil {
		return nil
	}
	if _, ok := s.allowed[project]; !ok {
		return cloudstore.ErrDashboardProjectForbidden
	}
	return nil
}

func (s *dashboardPrincipalHTTPStore) ListProjects(string) ([]cloudstore.DashboardProjectRow, error) {
	rows := []cloudstore.DashboardProjectRow{}
	for _, row := range []cloudstore.DashboardProjectRow{{Project: "project-a", Chunks: 1}, {Project: "project-b", Chunks: 1}} {
		if s.projectAllowed(row.Project) == nil {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (s *dashboardPrincipalHTTPStore) ProjectDetail(project string) (cloudstore.DashboardProjectDetail, error) {
	if err := s.projectAllowed(project); err != nil {
		return cloudstore.DashboardProjectDetail{}, err
	}
	return cloudstore.DashboardProjectDetail{Project: project, Stats: cloudstore.DashboardProjectRow{Project: project, Chunks: 1}}, nil
}

func (s *dashboardPrincipalHTTPStore) ListContributors(string) ([]cloudstore.DashboardContributorRow, error) {
	return []cloudstore.DashboardContributorRow{}, nil
}

func (s *dashboardPrincipalHTTPStore) ListRecentSessions(project, query string, limit int) ([]cloudstore.DashboardSessionRow, error) {
	rows, _, err := s.ListRecentSessionsPaginated(project, query, limit, 0)
	return rows, err
}

func (s *dashboardPrincipalHTTPStore) ListRecentObservations(project, query string, limit int) ([]cloudstore.DashboardObservationRow, error) {
	rows, _, err := s.ListRecentObservationsPaginated(project, query, "", limit, 0)
	return rows, err
}

func (s *dashboardPrincipalHTTPStore) ListRecentPrompts(project, query string, limit int) ([]cloudstore.DashboardPromptRow, error) {
	rows, _, err := s.ListRecentPromptsPaginated(project, query, limit, 0)
	return rows, err
}

func (s *dashboardPrincipalHTTPStore) AdminOverview() (cloudstore.DashboardAdminOverview, error) {
	rows, _ := s.ListProjects("")
	return cloudstore.DashboardAdminOverview{Projects: len(rows), Chunks: len(rows)}, nil
}

func (s *dashboardPrincipalHTTPStore) ListProjectsPaginated(query string, limit, offset int) ([]cloudstore.DashboardProjectRow, int, error) {
	rows, err := s.ListProjects(query)
	if err != nil {
		return nil, 0, err
	}
	return rows, len(rows), nil
}

func (s *dashboardPrincipalHTTPStore) ListRecentObservationsPaginated(project, query, obsType string, limit, offset int) ([]cloudstore.DashboardObservationRow, int, error) {
	if project != "" {
		if err := s.projectAllowed(project); err != nil {
			return nil, 0, err
		}
	}
	rows := []cloudstore.DashboardObservationRow{}
	for _, row := range []cloudstore.DashboardObservationRow{{Project: "project-a", SessionID: "session-a", SyncID: "observation-a", Title: "allowed observation"}, {Project: "project-b", SessionID: "session-b", SyncID: "observation-b", Title: "private observation"}} {
		if s.projectAllowed(row.Project) == nil && (project == "" || project == row.Project) {
			rows = append(rows, row)
		}
	}
	return rows, len(rows), nil
}

func (s *dashboardPrincipalHTTPStore) ListRecentSessionsPaginated(project, query string, limit, offset int) ([]cloudstore.DashboardSessionRow, int, error) {
	if project != "" {
		if err := s.projectAllowed(project); err != nil {
			return nil, 0, err
		}
	}
	return []cloudstore.DashboardSessionRow{}, 0, nil
}

func (s *dashboardPrincipalHTTPStore) ListRecentPromptsPaginated(project, query string, limit, offset int) ([]cloudstore.DashboardPromptRow, int, error) {
	if project != "" {
		if err := s.projectAllowed(project); err != nil {
			return nil, 0, err
		}
	}
	return []cloudstore.DashboardPromptRow{}, 0, nil
}

func (s *dashboardPrincipalHTTPStore) ListContributorsPaginated(string, int, int) ([]cloudstore.DashboardContributorRow, int, error) {
	return []cloudstore.DashboardContributorRow{}, 0, nil
}

func (s *dashboardPrincipalHTTPStore) GetSessionDetail(project, sessionID string) (cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, []cloudstore.DashboardPromptRow, error) {
	if err := s.projectAllowed(project); err != nil {
		return cloudstore.DashboardSessionRow{}, nil, nil, err
	}
	return cloudstore.DashboardSessionRow{Project: project, SessionID: sessionID}, nil, nil, nil
}

func (s *dashboardPrincipalHTTPStore) GetObservationDetail(project, sessionID, syncID string) (cloudstore.DashboardObservationRow, cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, error) {
	if err := s.projectAllowed(project); err != nil {
		return cloudstore.DashboardObservationRow{}, cloudstore.DashboardSessionRow{}, nil, err
	}
	return cloudstore.DashboardObservationRow{Project: project, SessionID: sessionID, SyncID: syncID}, cloudstore.DashboardSessionRow{Project: project, SessionID: sessionID}, nil, nil
}

func (s *dashboardPrincipalHTTPStore) GetPromptDetail(project, sessionID, syncID string) (cloudstore.DashboardPromptRow, cloudstore.DashboardSessionRow, []cloudstore.DashboardPromptRow, error) {
	if err := s.projectAllowed(project); err != nil {
		return cloudstore.DashboardPromptRow{}, cloudstore.DashboardSessionRow{}, nil, err
	}
	return cloudstore.DashboardPromptRow{Project: project, SessionID: sessionID, SyncID: syncID}, cloudstore.DashboardSessionRow{Project: project, SessionID: sessionID}, nil, nil
}

func (s *dashboardPrincipalHTTPStore) SystemHealth() (cloudstore.DashboardSystemHealth, error) {
	return cloudstore.DashboardSystemHealth{}, nil
}

func (s *dashboardPrincipalHTTPStore) ListProjectSyncControls() ([]cloudstore.ProjectSyncControl, error) {
	return []cloudstore.ProjectSyncControl{}, nil
}

func (s *dashboardPrincipalHTTPStore) GetProjectSyncControl(project string) (*cloudstore.ProjectSyncControl, error) {
	return &cloudstore.ProjectSyncControl{Project: project, SyncEnabled: true}, nil
}

func (s *dashboardPrincipalHTTPStore) SetProjectSyncEnabled(string, bool, string, string) error {
	s.syncToggles++
	return nil
}

func (s *dashboardPrincipalHTTPStore) IsProjectSyncEnabled(string) (bool, error) { return true, nil }

func (s *dashboardPrincipalHTTPStore) GetContributorDetail(string) (cloudstore.DashboardContributorRow, []cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, []cloudstore.DashboardPromptRow, error) {
	return cloudstore.DashboardContributorRow{}, nil, nil, nil, cloudstore.ErrDashboardContributorNotFound
}

func (s *dashboardPrincipalHTTPStore) ListDistinctTypes() ([]string, error) { return nil, nil }

func (s *dashboardPrincipalHTTPStore) ListAuditEntriesPaginated(context.Context, cloudstore.AuditFilter, int, int) ([]cloudstore.DashboardAuditRow, int, error) {
	return nil, 0, nil
}

func TestDashboardPrincipalHTTPRequestsUseScopedStoresAndFailClosed(t *testing.T) {
	principals := []cloudauth.Principal{
		dashboardManagedPrincipal("principal-a", cloudstore.PrincipalRoleAdmin, true),
		dashboardManagedPrincipal("principal-b", cloudstore.PrincipalRoleAdmin, true),
	}
	state := newManagedDashboardPrincipalStore(
		dashboardStoredPrincipal("principal-a", cloudstore.PrincipalRoleAdmin, true),
		dashboardStoredPrincipal("principal-b", cloudstore.PrincipalRoleAdmin, true),
	)
	store := &dashboardPrincipalHTTPStore{managedDashboardPrincipalStore: state}
	authorizer := &dashboardPrincipalGrantAuthorizer{grants: map[string][]string{"principal-a": {"project-a"}, "principal-b": {"project-b"}}}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"token-a": principals[0], "token-b": principals[1]}}
	srv := New(store, authn, 0,
		WithPrincipalStateStore(store),
		WithPrincipalProjectAuthorizer(authorizer),
		WithDashboardStoreForProjects(func(projects []string) (dashboard.DashboardStore, error) { return store.scoped(projects), nil }),
	)
	alice := managedDashboardLogin(t, srv, "token-a", false)
	bob := managedDashboardLogin(t, srv, "token-b", false)

	assertDashboardPrincipalHTTPBody(t, srv, alice, "/dashboard/activity", false, "allowed observation", "private observation")
	assertDashboardPrincipalHTTPBody(t, srv, alice, "/dashboard/browser/observations", true, "allowed observation", "private observation")
	assertDashboardPrincipalHTTPStatus(t, srv, alice, "/dashboard/projects/project-b", http.StatusForbidden)
	assertDashboardPrincipalHTTPStatus(t, srv, alice, "/dashboard/observations/project-b/session-b/observation-b", http.StatusForbidden)
	assertDashboardPrincipalHTTPBody(t, srv, bob, "/dashboard/activity", false, "private observation", "allowed observation")
	assertDashboardPrincipalHTTPBody(t, srv, alice, "/dashboard/activity", false, "allowed observation", "private observation")

	authorizer.err = context.DeadlineExceeded
	assertDashboardPrincipalHTTPStatus(t, srv, alice, "/dashboard/activity", http.StatusServiceUnavailable)
	authorizer.err = nil

	form := httptest.NewRequest(http.MethodPost, "/dashboard/admin/projects/project-b/sync", strings.NewReader("enabled=false"))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form.AddCookie(alice)
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, form)
	if response.Code != http.StatusSeeOther || store.syncToggles != 1 {
		t.Fatalf("expected unscoped existing sync-toggle behavior, status=%d toggles=%d", response.Code, store.syncToggles)
	}
}

func TestDashboardPrincipalUnsupportedManagedStoreFailsClosed(t *testing.T) {
	principal := dashboardManagedPrincipal("principal-a", cloudstore.PrincipalRoleAdmin, true)
	state := newManagedDashboardPrincipalStore(dashboardStoredPrincipal("principal-a", cloudstore.PrincipalRoleAdmin, true))
	store := &dashboardPrincipalHTTPStore{managedDashboardPrincipalStore: state}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"token-a": principal}}
	srv := New(store, authn, 0, WithPrincipalStateStore(store), WithPrincipalProjectAuthorizer(&dashboardPrincipalGrantAuthorizer{grants: map[string][]string{"principal-a": {"project-a"}}}))
	cookie := managedDashboardLogin(t, srv, "token-a", false)
	assertDashboardPrincipalHTTPStatus(t, srv, cookie, "/dashboard/activity", http.StatusServiceUnavailable)
}

func assertDashboardPrincipalHTTPStatus(t *testing.T, srv *CloudServer, cookie *http.Cookie, path string, want int) {
	t.Helper()
	response := performDashboardRequest(srv, http.MethodGet, path, cookie)
	if response.Code != want {
		t.Fatalf("expected %d for %s, got %d body=%q", want, path, response.Code, response.Body.String())
	}
}

func assertDashboardPrincipalHTTPBody(t *testing.T, srv *CloudServer, cookie *http.Cookie, path string, htmx bool, want, absent string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(cookie)
	if htmx {
		request.Header.Set("HX-Request", "true")
	}
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200 for %s, got %d body=%q", path, response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, want) || strings.Contains(body, absent) {
		t.Fatalf("expected %q and not %q for %s, body=%q", want, absent, path, body)
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
