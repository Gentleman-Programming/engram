package dashboard

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	nethtml "golang.org/x/net/html"
)

type parityStoreStub struct {
	projects      []cloudstore.DashboardProjectRow
	contributors  []cloudstore.DashboardContributorRow
	sessions      []cloudstore.DashboardSessionRow
	observations  []cloudstore.DashboardObservationRow
	prompts       []cloudstore.DashboardPromptRow
	adminOverview cloudstore.DashboardAdminOverview
	projectDetail cloudstore.DashboardProjectDetail
	systemHealth  cloudstore.DashboardSystemHealth
	syncControls  []cloudstore.ProjectSyncControl
	distinctTypes []string
	auditRows     []cloudstore.DashboardAuditRow
	managedUsers  []cloudstore.HumanUser

	errManagedUsers           error
	errListProjects           error
	errProjectDetail          error
	errListContributors       error
	errListRecentSessions     error
	errListRecentObservations error
	errListRecentPrompts      error
	errAdminOverview          error
	errSystemHealth           error
	errSyncControls           error
	isProjectSyncEnabled      bool
	// R5-4: per-method error overrides for detail handlers.
	errGetSessionDetail     error
	errGetObservationDetail error
	errGetPromptDetail      error
}

func (s parityStoreStub) ListProjects(_ string) ([]cloudstore.DashboardProjectRow, error) {
	if s.errListProjects != nil {
		return nil, s.errListProjects
	}
	return s.projects, nil
}

func (s parityStoreStub) ProjectDetail(_ string) (cloudstore.DashboardProjectDetail, error) {
	if s.errProjectDetail != nil {
		return cloudstore.DashboardProjectDetail{}, s.errProjectDetail
	}
	return s.projectDetail, nil
}

func (s parityStoreStub) ListContributors(_ string) ([]cloudstore.DashboardContributorRow, error) {
	if s.errListContributors != nil {
		return nil, s.errListContributors
	}
	return s.contributors, nil
}

func (s parityStoreStub) ListRecentSessions(_ string, _ string, _ int) ([]cloudstore.DashboardSessionRow, error) {
	if s.errListRecentSessions != nil {
		return nil, s.errListRecentSessions
	}
	return s.sessions, nil
}

func (s parityStoreStub) ListRecentObservations(_ string, _ string, _ int) ([]cloudstore.DashboardObservationRow, error) {
	if s.errListRecentObservations != nil {
		return nil, s.errListRecentObservations
	}
	return s.observations, nil
}

func (s parityStoreStub) ListRecentPrompts(_ string, _ string, _ int) ([]cloudstore.DashboardPromptRow, error) {
	if s.errListRecentPrompts != nil {
		return nil, s.errListRecentPrompts
	}
	return s.prompts, nil
}

func (s parityStoreStub) AdminOverview() (cloudstore.DashboardAdminOverview, error) {
	if s.errAdminOverview != nil {
		return cloudstore.DashboardAdminOverview{}, s.errAdminOverview
	}
	return s.adminOverview, nil
}

// Paginated list methods — return all rows (no real pagination in stub).
func (s parityStoreStub) ListProjectsPaginated(_ string, _, _ int) ([]cloudstore.DashboardProjectRow, int, error) {
	if s.errListProjects != nil {
		return nil, 0, s.errListProjects
	}
	return s.projects, len(s.projects), nil
}
func (s parityStoreStub) ListRecentObservationsPaginated(_, _, _ string, _, _ int) ([]cloudstore.DashboardObservationRow, int, error) {
	if s.errListRecentObservations != nil {
		return nil, 0, s.errListRecentObservations
	}
	return s.observations, len(s.observations), nil
}
func (s parityStoreStub) ListRecentSessionsPaginated(_, _ string, _, _ int) ([]cloudstore.DashboardSessionRow, int, error) {
	return s.sessions, len(s.sessions), nil
}
func (s parityStoreStub) ListRecentPromptsPaginated(_, _ string, _, _ int) ([]cloudstore.DashboardPromptRow, int, error) {
	return s.prompts, len(s.prompts), nil
}
func (s parityStoreStub) ListContributorsPaginated(_ string, _, _ int) ([]cloudstore.DashboardContributorRow, int, error) {
	if s.errListContributors != nil {
		return nil, 0, s.errListContributors
	}
	return s.contributors, len(s.contributors), nil
}

// Detail methods — return zero values (not exercised in existing tests).
func (s parityStoreStub) GetSessionDetail(_, _ string) (cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, []cloudstore.DashboardPromptRow, error) {
	if s.errGetSessionDetail != nil {
		return cloudstore.DashboardSessionRow{}, nil, nil, s.errGetSessionDetail
	}
	if len(s.sessions) > 0 {
		return s.sessions[0], s.observations, s.prompts, nil
	}
	return cloudstore.DashboardSessionRow{}, nil, nil, nil
}
func (s parityStoreStub) GetObservationDetail(_, _, _ string) (cloudstore.DashboardObservationRow, cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, error) {
	if s.errGetObservationDetail != nil {
		return cloudstore.DashboardObservationRow{}, cloudstore.DashboardSessionRow{}, nil, s.errGetObservationDetail
	}
	if len(s.observations) > 0 {
		var sess cloudstore.DashboardSessionRow
		if len(s.sessions) > 0 {
			sess = s.sessions[0]
		}
		return s.observations[0], sess, s.observations, nil
	}
	return cloudstore.DashboardObservationRow{}, cloudstore.DashboardSessionRow{}, nil, nil
}
func (s parityStoreStub) GetPromptDetail(_, _, _ string) (cloudstore.DashboardPromptRow, cloudstore.DashboardSessionRow, []cloudstore.DashboardPromptRow, error) {
	if s.errGetPromptDetail != nil {
		return cloudstore.DashboardPromptRow{}, cloudstore.DashboardSessionRow{}, nil, s.errGetPromptDetail
	}
	if len(s.prompts) > 0 {
		var sess cloudstore.DashboardSessionRow
		if len(s.sessions) > 0 {
			sess = s.sessions[0]
		}
		return s.prompts[0], sess, s.prompts, nil
	}
	return cloudstore.DashboardPromptRow{}, cloudstore.DashboardSessionRow{}, nil, nil
}

// SystemHealth — returns stub health data.
func (s parityStoreStub) SystemHealth() (cloudstore.DashboardSystemHealth, error) {
	if s.errSystemHealth != nil {
		return cloudstore.DashboardSystemHealth{}, s.errSystemHealth
	}
	return s.systemHealth, nil
}

// Sync control methods.
func (s parityStoreStub) ListProjectSyncControls() ([]cloudstore.ProjectSyncControl, error) {
	if s.errSyncControls != nil {
		return nil, s.errSyncControls
	}
	return s.syncControls, nil
}
func (s parityStoreStub) GetProjectSyncControl(_ string) (*cloudstore.ProjectSyncControl, error) {
	if len(s.syncControls) > 0 {
		c := s.syncControls[0]
		return &c, nil
	}
	return nil, nil
}
func (s parityStoreStub) SetProjectSyncEnabled(_ string, _ bool, _, _ string) error { return nil }
func (s parityStoreStub) IsProjectSyncEnabled(_ string) (bool, error) {
	return s.isProjectSyncEnabled, nil
}

// Batch 6: Connected navigation stubs.
func (s parityStoreStub) GetContributorDetail(name string) (cloudstore.DashboardContributorRow, []cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, []cloudstore.DashboardPromptRow, error) {
	for _, c := range s.contributors {
		if c.CreatedBy == name {
			return c, s.sessions, s.observations, s.prompts, nil
		}
	}
	// If no exact match but contributors exist, return first one (for tests that don't match by name).
	if len(s.contributors) > 0 {
		return s.contributors[0], s.sessions, s.observations, s.prompts, nil
	}
	// R4-7: return ErrDashboardContributorNotFound when no contributors exist (name is missing).
	return cloudstore.DashboardContributorRow{}, nil, nil, nil, fmt.Errorf("%w: contributor %s", cloudstore.ErrDashboardContributorNotFound, name)
}
func (s parityStoreStub) ListDistinctTypes() ([]string, error) {
	return s.distinctTypes, nil
}

// ListAuditEntriesPaginated — no-op stub for interface parity (REQ-409).
func (s parityStoreStub) ListAuditEntriesPaginated(_ context.Context, _ cloudstore.AuditFilter, _, _ int) ([]cloudstore.DashboardAuditRow, int, error) {
	return s.auditRows, len(s.auditRows), nil
}

// ListHumanUsers satisfies ManagedUsersStore (cloud-user-token-management
// PR4). Deliberately separate from the contributor fixtures above: managed
// users and contributor analytics must stay distinct.
func (s parityStoreStub) ListHumanUsers(context.Context) ([]cloudstore.HumanUser, error) {
	if s.errManagedUsers != nil {
		return nil, s.errManagedUsers
	}
	return s.managedUsers, nil
}

// ─── Batch 6 Tests ───────────────────────────────────────────────────────────

// TestContributorDetailPageRendersDrillDown asserts that GET /dashboard/contributors/{name}
// renders the ContributorDetailPage templ (not raw HTML stub). Satisfies (h).
func TestContributorDetailPageRendersDrillDown(t *testing.T) {
	store := parityStoreStub{
		contributors: []cloudstore.DashboardContributorRow{
			{CreatedBy: "alice", Chunks: 5, Projects: 2, LastChunkAt: "2026-04-23T10:00:00Z"},
		},
		sessions: []cloudstore.DashboardSessionRow{
			{Project: "proj-alice", SessionID: "sess-alice-1", StartedAt: "2026-04-23T08:00:00Z"},
		},
		observations: []cloudstore.DashboardObservationRow{
			{Project: "proj-alice", SessionID: "sess-alice-1", Type: "decision", Title: "Alice obs", CreatedAt: "2026-04-23T08:10:00Z"},
		},
		prompts: []cloudstore.DashboardPromptRow{
			{Project: "proj-alice", SessionID: "sess-alice-1", Content: "Alice prompt", CreatedAt: "2026-04-23T08:20:00Z"},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/contributors/alice?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{"CONTRIBUTOR DETAIL", "Recent Sessions", "Recent Observations"} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected %q in contributor detail body, got body=%q", marker, body)
		}
	}
}

// TestBrowserObservationsAreClickable asserts GET /dashboard/browser/observations
// returns HTML with href links to observation detail pages. Satisfies (a).
func TestBrowserObservationsAreClickable(t *testing.T) {
	store := parityStoreStub{
		observations: []cloudstore.DashboardObservationRow{
			{Project: "proj-a", SessionID: "s1", SyncID: "sync-obs-1", ChunkID: "c1", Type: "decision", Title: "My Obs", CreatedAt: "2026-04-23T10:00:00Z"},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/dashboard/observations/`) {
		t.Errorf("expected clickable observation links in browser partial, got body=%q", body)
	}
}

// TestBrowserSessionsAreClickable asserts GET /dashboard/browser/sessions
// returns HTML with href links to session detail pages. Satisfies (b).
func TestBrowserSessionsAreClickable(t *testing.T) {
	store := parityStoreStub{
		sessions: []cloudstore.DashboardSessionRow{
			{Project: "proj-a", SessionID: "sess-1", StartedAt: "2026-04-23T08:00:00Z"},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/sessions?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/dashboard/sessions/`) {
		t.Errorf("expected clickable session links in browser partial, got body=%q", body)
	}
}

// TestBrowserPromptsAreClickable asserts GET /dashboard/browser/prompts
// returns HTML with href links to prompt detail pages. Satisfies (c).
func TestBrowserPromptsAreClickable(t *testing.T) {
	store := parityStoreStub{
		prompts: []cloudstore.DashboardPromptRow{
			{Project: "proj-a", SessionID: "s1", SyncID: "sync-prompt-1", ChunkID: "c1", Content: "Test prompt", CreatedAt: "2026-04-23T10:00:00Z"},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/prompts?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/dashboard/prompts/`) {
		t.Errorf("expected clickable prompt links in browser partial, got body=%q", body)
	}
}

// TestBrowserTypePillsSourcedFromStore asserts GET /dashboard/browser renders
// type pill elements when ListDistinctTypes returns types. Satisfies (m).
func TestBrowserTypePillsSourcedFromStore(t *testing.T) {
	store := parityStoreStub{
		distinctTypes: []string{"architecture", "bugfix"},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/browser?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "architecture") || !strings.Contains(body, "bugfix") {
		t.Errorf("expected type pills from store in browser body, got body=%q", body)
	}
}

// TestProjectCardShowsPausedBadge asserts GET /dashboard/projects/list renders
// Paused badge when project sync control has SyncEnabled=false. Satisfies (i).
func TestProjectCardShowsPausedBadge(t *testing.T) {
	reason := "maintenance"
	store := parityStoreStub{
		projects: []cloudstore.DashboardProjectRow{
			{Project: "proj-a", Chunks: 3, Sessions: 2, Observations: 2, Prompts: 1},
		},
		syncControls: []cloudstore.ProjectSyncControl{
			{Project: "proj-a", SyncEnabled: false, PausedReason: &reason},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/projects/list?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Paused") {
		t.Errorf("expected Paused badge in projects list for paused project, got body=%q", body)
	}
}

// TestAdminProjectsPageRendersToggles asserts GET /dashboard/admin/projects
// renders the AdminProjectsPage with hx-post toggle forms. Satisfies (k).
func TestAdminProjectsPageRendersToggles(t *testing.T) {
	reason := "maintenance"
	store := parityStoreStub{
		projects: []cloudstore.DashboardProjectRow{
			{Project: "proj-a", Chunks: 3},
		},
		syncControls: []cloudstore.ProjectSyncControl{
			{Project: "proj-a", SyncEnabled: false, PausedReason: &reason},
		},
	}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/projects?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-post="/dashboard/admin/projects/`) && !strings.Contains(body, `action="/dashboard/admin/projects/`) {
		t.Errorf("expected admin toggle forms in /dashboard/admin/projects body, got body=%q", body)
	}
}

// TestProjectDetailShowsPauseAudit asserts GET /dashboard/projects/{name}
// renders PROJECT DETAIL with pause audit info from the sync control. Satisfies (j).
func TestProjectDetailShowsPauseAudit(t *testing.T) {
	reason := "scheduled maintenance"
	updatedBy := "alice"
	store := parityStoreStub{
		projectDetail: cloudstore.DashboardProjectDetail{
			Project: "proj-a",
			Stats:   cloudstore.DashboardProjectRow{Project: "proj-a", Chunks: 3, Sessions: 1, Observations: 1},
		},
		syncControls: []cloudstore.ProjectSyncControl{
			{Project: "proj-a", SyncEnabled: false, PausedReason: &reason, UpdatedBy: &updatedBy},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/projects/proj-a?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "PROJECT DETAIL") {
		t.Errorf("expected PROJECT DETAIL in body, got body=%q", body)
	}
	if !strings.Contains(body, "Paused") {
		t.Errorf("expected Paused in project detail body (from sync control), got body=%q", body)
	}
}

type stubSyncStatusProvider struct {
	status SyncStatus
}

func TestMountHTMXAndProjectDetailParity(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
		Store: parityStoreStub{
			projects:     []cloudstore.DashboardProjectRow{{Project: "proj-a", Chunks: 3, Sessions: 2, Observations: 2, Prompts: 1}},
			contributors: []cloudstore.DashboardContributorRow{{CreatedBy: "alan@example.com", Chunks: 2, Projects: 1, LastChunkAt: "2026-04-21T12:00:00Z"}},
			sessions:     []cloudstore.DashboardSessionRow{{Project: "proj-a", SessionID: "s-1", StartedAt: "2026-04-21T09:00:00Z"}},
			observations: []cloudstore.DashboardObservationRow{{Project: "proj-a", SessionID: "s-1", Type: "decision", Title: "Shared decision", CreatedAt: "2026-04-21T10:00:00Z"}},
			prompts:      []cloudstore.DashboardPromptRow{{Project: "proj-a", SessionID: "s-1", Content: "Prompt text", CreatedAt: "2026-04-21T11:00:00Z"}},
			projectDetail: cloudstore.DashboardProjectDetail{
				Project: "proj-a",
				Stats:   cloudstore.DashboardProjectRow{Project: "proj-a", Chunks: 3, Sessions: 2, Observations: 2, Prompts: 1},
				Contributors: []cloudstore.DashboardContributorRow{
					{CreatedBy: "alan@example.com", Chunks: 2, Projects: 1, LastChunkAt: "2026-04-21T12:00:00Z"},
				},
				Sessions:     []cloudstore.DashboardSessionRow{{Project: "proj-a", SessionID: "s-1", StartedAt: "2026-04-21T09:00:00Z"}},
				Observations: []cloudstore.DashboardObservationRow{{Project: "proj-a", SessionID: "s-1", Type: "decision", Title: "Shared decision", CreatedAt: "2026-04-21T10:00:00Z"}},
				Prompts:      []cloudstore.DashboardPromptRow{{Project: "proj-a", SessionID: "s-1", Content: "Prompt text", CreatedAt: "2026-04-21T11:00:00Z"}},
			},
		},
	})

	t.Run("browser partial endpoint returns fragment for htmx requests", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok&project=proj-a", nil)
		req.Header.Set("HX-Request", "true")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "<!DOCTYPE html>") {
			t.Fatalf("expected htmx partial fragment, got full page=%q", body)
		}
		if !strings.Contains(body, "Shared decision") {
			t.Fatalf("expected meaningful standalone partial content, body=%q", body)
		}
	})

	t.Run("browser partial endpoint returns full page for non-htmx navigation", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok&project=proj-a", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		// MIGRATED: templ generates <!doctype html> (lowercase), not <!DOCTYPE html>.
		// The browser observations non-htmx path now renders BrowserPage shell (HTMX-driven).
		if !strings.Contains(body, "<!doctype html>") && !strings.Contains(body, "<!DOCTYPE html>") {
			t.Fatalf("expected full page html fallback, body=%q", body)
		}
		if !strings.Contains(body, "Knowledge Browser") {
			t.Fatalf("expected browser page heading, body=%q", body)
		}
	})

	t.Run("project detail route renders queryable project-specific data", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/projects/proj-a?auth=ok", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for project detail, got %d", rec.Code)
		}
		body := rec.Body.String()
		// MIGRATED: handleProjectDetail now uses ProjectDetailPage templ component.
		// Old raw-HTML builder rendered "Project: proj-a" inline; templ renders
		// "PROJECT DETAIL" kicker + "proj-a" in breadcrumb. Observations/sessions/prompts
		// are now HTMX-driven (loaded as partials), not embedded inline.
		if !strings.Contains(body, "PROJECT DETAIL") {
			t.Fatalf("expected PROJECT DETAIL kicker in project detail body, body=%q", body)
		}
		if !strings.Contains(body, "proj-a") {
			t.Fatalf("expected project name in project detail body, body=%q", body)
		}
	})

	t.Run("stats and activity routes expose richer dashboard surfaces", func(t *testing.T) {
		stats := httptest.NewRecorder()
		mux.ServeHTTP(stats, httptest.NewRequest(http.MethodGet, "/dashboard/stats?auth=ok", nil))
		if stats.Code != http.StatusOK {
			t.Fatalf("expected stats route 200, got %d", stats.Code)
		}
		if !strings.Contains(stats.Body.String(), "Cloud Stats") {
			t.Fatalf("expected stats surface content, body=%q", stats.Body.String())
		}

		activity := httptest.NewRecorder()
		mux.ServeHTTP(activity, httptest.NewRequest(http.MethodGet, "/dashboard/activity?auth=ok", nil))
		if activity.Code != http.StatusOK {
			t.Fatalf("expected activity route 200, got %d", activity.Code)
		}
		if !strings.Contains(activity.Body.String(), "Recent Observation Activity") {
			t.Fatalf("expected activity surface content, body=%q", activity.Body.String())
		}
	})

	t.Run("contributor detail route provides deep-link surface", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/contributors/alan@example.com?auth=ok", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for contributor detail, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "CONTRIBUTOR DETAIL") {
			t.Fatalf("expected contributor detail section, body=%q", rec.Body.String())
		}
	})

	t.Run("admin route denies authenticated non-admin users", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin?auth=ok", nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for authenticated non-admin, got %d", rec.Code)
		}
	})

	t.Run("already-authenticated login redirects to dashboard home", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/login?auth=ok", nil))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 when already authenticated, got %d", rec.Code)
		}
		if location := rec.Header().Get("Location"); location != "/dashboard/" {
			t.Fatalf("expected redirect to /dashboard/, got %q", location)
		}
	})
}

func TestMountAddsHTMXNavigationWiringForBrowserProjectsAndAdmin(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return true },
		Store: parityStoreStub{
			projects:      []cloudstore.DashboardProjectRow{{Project: "proj-a", Chunks: 1, Sessions: 1, Observations: 1, Prompts: 1}},
			adminOverview: cloudstore.DashboardAdminOverview{Projects: 1, Contributors: 1, Chunks: 1},
		},
	})

	browser := httptest.NewRecorder()
	mux.ServeHTTP(browser, httptest.NewRequest(http.MethodGet, "/dashboard/browser?auth=ok", nil))
	if browser.Code != http.StatusOK {
		t.Fatalf("expected browser page 200, got %d", browser.Code)
	}
	// UPDATED: new browser page uses templ BrowserPage component with HTMX subtabs.
	// The query params are no longer forwarded via URL string manipulation —
	// HTMX uses hx-include to pass active filter state. Assertions updated accordingly.
	browserBody := browser.Body.String()
	if !strings.Contains(browserBody, `hx-get="/dashboard/browser/sessions"`) || !strings.Contains(browserBody, `hx-target="#browser-content"`) {
		t.Fatalf("expected browser subtab htmx wiring, body=%q", browserBody)
	}

	projects := httptest.NewRecorder()
	mux.ServeHTTP(projects, httptest.NewRequest(http.MethodGet, "/dashboard/projects?auth=ok", nil))
	if projects.Code != http.StatusOK {
		t.Fatalf("expected projects page 200, got %d", projects.Code)
	}
	// UPDATED: new projects page uses ProjectsPage templ component with HTMX-driven list.
	// The project list is loaded via /dashboard/projects/list partial, not inline.
	projectsBody := projects.Body.String()
	if !strings.Contains(projectsBody, `hx-get="/dashboard/projects/list"`) || !strings.Contains(projectsBody, `hx-target="#projects-content"`) {
		t.Fatalf("expected projects htmx wiring, body=%q", projectsBody)
	}

	admin := httptest.NewRecorder()
	mux.ServeHTTP(admin, httptest.NewRequest(http.MethodGet, "/dashboard/admin?auth=ok", nil))
	if admin.Code != http.StatusOK {
		t.Fatalf("expected admin page 200, got %d", admin.Code)
	}
	// UPDATED: new admin page uses AdminPage templ component with nav links.
	adminBody := admin.Body.String()
	if !strings.Contains(adminBody, "ADMIN SURFACE") || !strings.Contains(adminBody, "/dashboard/admin/users") {
		t.Fatalf("expected admin surface copy and nav links, body=%q", adminBody)
	}
}

func TestMountContributorsSurfaceRendersCloudstoreBackedRows(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
		Store: parityStoreStub{
			contributors: []cloudstore.DashboardContributorRow{
				{CreatedBy: "alan@example.com", Chunks: 5, Projects: 2, LastChunkAt: "2026-04-22T10:00:00Z"},
			},
		},
	})

	// R6-1 update: the shell page no longer embeds contributor rows directly.
	// Check shell has hx-get trigger, then check the list partial for actual data.
	shellRec := httptest.NewRecorder()
	mux.ServeHTTP(shellRec, httptest.NewRequest(http.MethodGet, "/dashboard/contributors?auth=ok&q=alan", nil))
	if shellRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for contributors page, got %d", shellRec.Code)
	}
	shellBody := shellRec.Body.String()
	if !strings.Contains(shellBody, "Contributors") {
		t.Fatalf("expected contributors heading, body=%q", shellBody)
	}
	if !strings.Contains(shellBody, `hx-get="/dashboard/contributors/list"`) {
		t.Fatalf("expected HTMX load trigger in contributors shell, body=%q", shellBody)
	}

	// Check the list partial serves the actual data.
	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/dashboard/contributors/list?auth=ok&q=alan", nil)
	listReq.Header.Set("HX-Request", "true")
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for contributors list partial, got %d", listRec.Code)
	}
	listBody := listRec.Body.String()
	if !strings.Contains(listBody, "alan@example.com") {
		t.Fatalf("expected cloudstore contributor row to render in list partial, body=%q", listBody)
	}
	if !strings.Contains(listBody, ">5<") || !strings.Contains(listBody, ">2<") {
		t.Fatalf("expected contributor aggregate metrics to render in list partial, body=%q", listBody)
	}
}

func TestMountStoreErrorsReturnDegradedNon200Responses(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
		Store: parityStoreStub{
			errListProjects:           errors.New("projects query failed"),
			errListRecentObservations: errors.New("observations query failed"),
		},
	})

	t.Run("projects list partial surfaces backend failures", func(t *testing.T) {
		// UPDATED: /dashboard/projects now renders ProjectsPage (HTMX shell) which
		// does NOT call ListProjects directly — errors surface via /dashboard/projects/list.
		// R6-2: partial-only endpoints return 502 (fragment only, no Layout wrapper).
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/projects/list?auth=ok", nil))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected 502 for projects list partial failure, got %d", rec.Code)
		}
		body := rec.Body.String()
		// R6-2: fragment must not contain a full Layout shell (no status-ribbon).
		if strings.Contains(body, "status-ribbon") {
			t.Fatalf("expected error fragment (no Layout) for partial-only endpoint, got full page, body=%q", body)
		}
		if strings.Contains(body, "projects query failed") {
			t.Fatalf("expected backend error to be hidden from degraded HTML, body=%q", body)
		}
	})

	t.Run("htmx route surfaces backend failures as standalone fragment", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok", nil)
		req.Header.Set("HX-Request", "true")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 for htmx observations query failure, got %d", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "<!DOCTYPE html>") {
			t.Fatalf("expected standalone degraded fragment for htmx, got full page=%q", body)
		}
		if !strings.Contains(body, "Observations unavailable") {
			t.Fatalf("expected degraded observations fragment, body=%q", body)
		}
	})
}

func TestMountProjectScopedErrorsMapToExplicitHTTPStatuses(t *testing.T) {
	tests := []struct {
		name     string
		store    parityStoreStub
		path     string
		wantCode int
		wantText string
	}{
		{
			name:     "forbidden project returns 403",
			store:    parityStoreStub{errProjectDetail: cloudstore.ErrDashboardProjectForbidden},
			path:     "/dashboard/projects/proj-a?auth=ok",
			wantCode: http.StatusForbidden,
			wantText: "Project access denied",
		},
		{
			name:     "missing project returns 404",
			store:    parityStoreStub{errProjectDetail: cloudstore.ErrDashboardProjectNotFound},
			path:     "/dashboard/projects/proj-missing?auth=ok",
			wantCode: http.StatusNotFound,
			wantText: "Project not found",
		},
		{
			name: "invalid project returns 404",
			// MIGRATED: blank/whitespace project is now caught at handler level before store call.
			// New handleProjectDetail calls EmptyState("Project Not Found", ...) for empty project.
			store:    parityStoreStub{errProjectDetail: cloudstore.ErrDashboardProjectInvalid},
			path:     "/dashboard/projects/%20%20%20?auth=ok",
			wantCode: http.StatusNotFound,
			wantText: "Project Not Found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			Mount(mux, MountConfig{
				RequireSession: func(r *http.Request) error {
					if r.URL.Query().Get("auth") == "ok" {
						return nil
					}
					return errUnauthorized
				},
				Store: tt.store,
			})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("expected %d, got %d body=%q", tt.wantCode, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantText) {
				t.Fatalf("expected body to contain %q, got %q", tt.wantText, rec.Body.String())
			}
		})
	}
}

func (s stubSyncStatusProvider) Status() SyncStatus { return s.status }

func TestHandlerWithStatusRendersDeterministicReasonParity(t *testing.T) {
	tests := []struct {
		name          string
		reasonCode    string
		reasonMessage string
		expectedLabel string
	}{
		{
			name:          "blocked unenrolled reason",
			reasonCode:    "blocked_unenrolled",
			reasonMessage: "project \"alpha\" is not enrolled for cloud sync",
			expectedLabel: "Blocked — project unenrolled",
		},
		{
			name:          "auth required reason",
			reasonCode:    "auth_required",
			reasonMessage: "cloud credentials are missing: configure server URL and token",
			expectedLabel: "Authentication required",
		},
		{
			name:          "transport failed reason",
			reasonCode:    "transport_failed",
			reasonMessage: "dial tcp 127.0.0.1:443: connect: connection refused",
			expectedLabel: "Transport failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := HandlerWithStatus(stubSyncStatusProvider{status: SyncStatus{
				Phase:         "degraded",
				ReasonCode:    tt.reasonCode,
				ReasonMessage: tt.reasonMessage,
			}})

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rr.Code)
			}

			body := rr.Body.String()
			if !strings.Contains(body, tt.reasonCode) {
				t.Fatalf("expected body to contain reason code %q, body=%q", tt.reasonCode, body)
			}
			if !strings.Contains(html.UnescapeString(body), tt.reasonMessage) {
				t.Fatalf("expected body to contain reason message %q, body=%q", tt.reasonMessage, body)
			}
			if !strings.Contains(body, tt.expectedLabel) {
				t.Fatalf("expected body to contain reason label %q, body=%q", tt.expectedLabel, body)
			}
		})
	}
}

func TestHandlerWithStatusEscapesDynamicFields(t *testing.T) {
	h := HandlerWithStatus(stubSyncStatusProvider{status: SyncStatus{
		Phase:         `<script>alert("p")</script>`,
		ReasonCode:    `<b>code</b>`,
		ReasonMessage: `<img src=x onerror=alert(1)>`,
	}})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	forbidden := []string{"<script>", "<img", "<b>code</b>"}
	for _, token := range forbidden {
		if strings.Contains(body, token) {
			t.Fatalf("expected escaped output without %q, body=%q", token, body)
		}
	}
	if !strings.Contains(body, "&lt;script&gt;alert") {
		t.Fatalf("expected script tag to be escaped, body=%q", body)
	}
}

// hasElementWithClass walks the HTML token stream and returns true if any element
// of the given tag has a class attribute containing the given class name.
// Satisfies Design Decision 7.
func hasElementWithClass(body string, tag string, class string) bool {
	tokenizer := nethtml.NewTokenizer(strings.NewReader(body))
	for {
		tt := tokenizer.Next()
		if tt == nethtml.ErrorToken {
			break
		}
		if tt == nethtml.StartTagToken || tt == nethtml.SelfClosingTagToken {
			name, hasAttr := tokenizer.TagName()
			if !hasAttr || (tag != "" && string(name) != tag) {
				continue
			}
			for {
				key, val, more := tokenizer.TagAttr()
				if string(key) == "class" {
					for _, cls := range strings.Fields(string(val)) {
						if cls == class {
							return true
						}
					}
				}
				if !more {
					break
				}
			}
		}
	}
	return false
}

// countElementsWithClass counts elements having the given class anywhere in the body.
func countElementsWithClass(body string, class string) int {
	tokenizer := nethtml.NewTokenizer(strings.NewReader(body))
	count := 0
	for {
		tt := tokenizer.Next()
		if tt == nethtml.ErrorToken {
			break
		}
		if tt == nethtml.StartTagToken || tt == nethtml.SelfClosingTagToken {
			_, hasAttr := tokenizer.TagName()
			if !hasAttr {
				continue
			}
			for {
				key, val, more := tokenizer.TagAttr()
				if string(key) == "class" {
					for _, cls := range strings.Fields(string(val)) {
						if cls == class {
							count++
						}
					}
				}
				if !more {
					break
				}
			}
		}
	}
	return count
}

// newAuthedMux creates a test mux with a simple auth=ok query param gate.
func newAuthedMux(store DashboardStore, isAdmin bool) *http.ServeMux {
	mux := http.NewServeMux()
	cfg := MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return isAdmin },
		Store:   store,
	}
	if managed, ok := store.(ManagedUsersStore); ok {
		cfg.ManagedUsers = managed
	}
	Mount(mux, cfg)
	return mux
}

// TestDashboardLayoutHTMLStructure asserts the full shell class hierarchy
// in the rendered layout. Satisfies REQ-107.
func TestDashboardLayoutHTMLStructure(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	classes := []struct {
		tag   string
		class string
	}{
		{"body", "shell-body"},
		{"div", "shell-backdrop"},
		{"div", "app-shell"},
		{"header", "shell-header"},
		{"div", "brand-stack"},
		{"nav", "shell-nav"},
		{"main", "shell-main"},
		{"footer", "shell-footer"},
	}
	for _, tc := range classes {
		if !hasElementWithClass(body, tc.tag, tc.class) {
			t.Errorf("expected <%s class=%q> in layout body", tc.tag, tc.class)
		}
	}
}

// TestStatusRibbonAndFooterPresent asserts that the status ribbon and footer
// are present in the rendered layout. Satisfies REQ-107.
func TestStatusRibbonAndFooterPresent(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	body := rec.Body.String()
	for _, marker := range []string{
		"status-ribbon",
		"status-pill",
		"CLOUD ACTIVE",
		"ENGRAM CLOUD / SHARED MEMORY INDEX / LIVE SYNC READY",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected %q in body", marker)
		}
	}
}

// TestNavTabsRenderedCorrectly asserts that the nav tab hrefs are correct for
// an admin user. Satisfies REQ-107.
func TestNavTabsRenderedCorrectly(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, true)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	body := rec.Body.String()
	for _, href := range []string{
		`href="/dashboard/"`,
		`href="/dashboard/browser"`,
		`href="/dashboard/projects"`,
		`href="/dashboard/contributors"`,
		`href="/dashboard/admin"`,
	} {
		if !strings.Contains(body, href) {
			t.Errorf("expected nav href %q in body", href)
		}
	}
}

// TestLoginPageTokenFormAndCopy asserts login page structure. Satisfies REQ-111.
func TestLoginPageTokenFormAndCopy(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/login?next=%2Fdashboard%2Fbrowser", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, marker := range []string{
		`name="token"`,
		"Engram Cloud",
		"CLOUD ACTIVE",
		`name="next"`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected %q in login page body", marker)
		}
	}
}

// TestGetDisplayNameFallback asserts that when GetDisplayName is nil, the
// Principal.DisplayName() returns "OPERATOR". Satisfies REQ-103.
func TestGetDisplayNameFallback(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:        func(_ *http.Request) bool { return false },
		Store:          parityStoreStub{},
		GetDisplayName: nil, // deliberately nil — must fall back to "OPERATOR"
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "OPERATOR") {
		t.Errorf("expected OPERATOR fallback display name in body, got: %q", body)
	}
}

// TestPrincipalBridgeNoPanicOnEmptyContext asserts that an empty-string return
// from GetDisplayName is treated as absent and falls back to "OPERATOR". Satisfies REQ-113.
func TestPrincipalBridgeNoPanicOnEmptyContext(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:        func(_ *http.Request) bool { return false },
		Store:          parityStoreStub{},
		GetDisplayName: func(_ *http.Request) string { return "" },
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "OPERATOR") {
		t.Errorf("expected OPERATOR fallback for empty display name, got: %q", body)
	}
}

func TestHandlerWithStatusRendersUpgradePhaseAndReasonParity(t *testing.T) {
	h := HandlerWithStatus(stubSyncStatusProvider{status: SyncStatus{
		Phase:                "degraded",
		ReasonCode:           "blocked_unenrolled",
		ReasonMessage:        "project \"alpha\" is not enrolled for cloud sync",
		UpgradeStage:         "bootstrap_pushed",
		UpgradeReasonCode:    "upgrade_repair_backfill_sync_journal",
		UpgradeReasonMessage: "repair pending",
	}})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "upgrade_stage: bootstrap_pushed") {
		t.Fatalf("expected upgrade stage in dashboard output, body=%q", body)
	}
	if !strings.Contains(body, "upgrade_reason_code: upgrade_repair_backfill_sync_journal") {
		t.Fatalf("expected upgrade reason code in dashboard output, body=%q", body)
	}
	if !strings.Contains(body, "upgrade_reason_message: repair pending") {
		t.Fatalf("expected upgrade reason message in dashboard output, body=%q", body)
	}
}

func TestMountRouteParityAndHTTPFallbacks(t *testing.T) {
	mux := http.NewServeMux()

	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		ValidateLoginToken: func(token string) error {
			if strings.TrimSpace(token) != "valid-token" {
				return errUnauthorized
			}
			return nil
		},
		CreateSessionCookie: func(w http.ResponseWriter, _ *http.Request, token string) error {
			http.SetCookie(w, &http.Cookie{Name: "engram_dashboard_token", Value: token, Path: "/dashboard"})
			return nil
		},
		ClearSessionCookie: func(w http.ResponseWriter, _ *http.Request) {
			http.SetCookie(w, &http.Cookie{Name: "engram_dashboard_token", Value: "", Path: "/dashboard", MaxAge: -1})
		},
		IsAdmin: func(_ *http.Request) bool { return false },
		StatusProvider: stubSyncStatusProvider{status: SyncStatus{
			Phase:                "degraded",
			ReasonCode:           "blocked_unenrolled",
			ReasonMessage:        "project \"proj-a\" is not enrolled for cloud sync",
			UpgradeStage:         "bootstrap_pushed",
			UpgradeReasonCode:    "upgrade_repair_backfill_sync_journal",
			UpgradeReasonMessage: "repair pending",
		}},
	})

	t.Run("login page serves styled HTML and static references", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/login", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "/dashboard/static/pico.min.css") {
			t.Fatalf("expected pico css reference, body=%q", body)
		}
		if !strings.Contains(body, "/dashboard/static/styles.css") {
			t.Fatalf("expected styles css reference, body=%q", body)
		}
		if !strings.Contains(body, "name=\"token\"") {
			t.Fatalf("expected token input for HTTP fallback form, body=%q", body)
		}
	})

	t.Run("static assets are served", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/static/styles.css", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected static asset 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), ".shell-body") {
			t.Fatalf("expected rich stylesheet class markers, body=%q", rec.Body.String())
		}
	})

	t.Run("protected routes redirect when unauthenticated", func(t *testing.T) {
		protected := []struct {
			route        string
			wantLocation string
		}{
			{route: "/dashboard", wantLocation: "/dashboard/login?next=%2Fdashboard"},
			{route: "/dashboard/", wantLocation: "/dashboard/login?next=%2Fdashboard"},
			{route: "/dashboard/browser", wantLocation: "/dashboard/login?next=%2Fdashboard%2Fbrowser"},
			{route: "/dashboard/projects?q=alpha", wantLocation: "/dashboard/login?next=%2Fdashboard%2Fprojects%3Fq%3Dalpha"},
			{route: "/dashboard/contributors", wantLocation: "/dashboard/login?next=%2Fdashboard%2Fcontributors"},
			{route: "/dashboard/admin", wantLocation: "/dashboard/login?next=%2Fdashboard%2Fadmin"},
		}
		for _, tc := range protected {
			t.Run(tc.route, func(t *testing.T) {
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.route, nil))
				if rec.Code != http.StatusSeeOther {
					t.Fatalf("expected redirect for %s, got %d", tc.route, rec.Code)
				}
				if rec.Header().Get("Location") != tc.wantLocation {
					t.Fatalf("expected login redirect for %s to preserve next path, got %q", tc.route, rec.Header().Get("Location"))
				}
			})
		}
	})

	t.Run("htmx protected routes return HX-Redirect instead of normal redirect", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?project=proj-a", nil)
		req.Header.Set("HX-Request", "true")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for unauthenticated htmx request, got %d", rec.Code)
		}
		if location := rec.Header().Get("Location"); location != "" {
			t.Fatalf("expected no plain Location redirect for htmx auth gate, got %q", location)
		}
		if hx := rec.Header().Get("HX-Redirect"); hx != "/dashboard/login?next=%2Fdashboard%2Fbrowser%2Fobservations%3Fproject%3Dproj-a" {
			t.Fatalf("expected HX-Redirect preserving next target, got %q", hx)
		}
	})

	t.Run("login POST fallback establishes session and redirects", func(t *testing.T) {
		form := url.Values{"token": {"valid-token"}, "next": {"/dashboard/projects/proj-a?q=alpha"}}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected login redirect, got %d", rec.Code)
		}
		if rec.Header().Get("Location") != "/dashboard/projects/proj-a?q=alpha" {
			t.Fatalf("expected redirect to preserved next path, got %q", rec.Header().Get("Location"))
		}
		cookies := rec.Result().Cookies()
		if len(cookies) == 0 {
			t.Fatal("expected session cookie to be set")
		}
	})

	t.Run("login page carries validated next link", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/login?next=%2Fdashboard%2Fprojects%3Fq%3Dalpha", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected login page 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `name="next" value="/dashboard/projects?q=alpha"`) {
			t.Fatalf("expected validated next value in hidden login field, body=%q", rec.Body.String())
		}
	})

	t.Run("login POST fallback validates token", func(t *testing.T) {
		form := url.Values{"token": {"wrong"}}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/dashboard/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected form re-render with 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "invalid token") {
			t.Fatalf("expected invalid token feedback, body=%q", rec.Body.String())
		}
	})

	t.Run("shareable URL renders page once authenticated", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/projects?auth=ok&q=alpha", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Projects") {
			t.Fatalf("expected projects page content, body=%q", rec.Body.String())
		}
	})

	t.Run("mounted dashboard home renders shell structure", func(t *testing.T) {
		// UPDATED: new dashboard home uses templ DashboardHome component; upgrade_stage
		// fields are no longer rendered inline in the dashboard home page.
		// The new component renders the shell with HTMX-driven stats loading.
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard?auth=ok", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		for _, token := range []string{
			"shell-body",
			"shell-main",
			"Welcome to Engram Cloud",
		} {
			if !strings.Contains(body, token) {
				t.Fatalf("expected mounted /dashboard route to include %q, body=%q", token, body)
			}
		}
	})
}

// newAuthedAdminMux creates a test mux with admin=true.
func newAuthedAdminMux(store DashboardStore) *http.ServeMux {
	mux := http.NewServeMux()
	cfg := MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:        func(_ *http.Request) bool { return true },
		GetDisplayName: func(_ *http.Request) string { return "OPERATOR" },
		Store:          store,
	}
	if managed, ok := store.(ManagedUsersStore); ok {
		cfg.ManagedUsers = managed
	}
	Mount(mux, cfg)
	return mux
}

// TestDashboardHomeHTMXWiring asserts the home page emits correct HTMX attributes. Satisfies REQ-108.
func TestDashboardHomeHTMXWiring(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	body := rec.Body.String()
	for _, marker := range []string{
		`hx-get="/dashboard/stats"`,
		`hx-trigger="load"`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected %q in dashboard home body", marker)
		}
	}
}

// TestBrowserPageHTMXWiring asserts the browser page emits HTMX attributes. Satisfies REQ-108.
func TestBrowserPageHTMXWiring(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/browser?auth=ok", nil))
	body := rec.Body.String()
	for _, marker := range []string{
		`hx-get="/dashboard/browser/observations"`,
		`hx-target="#browser-content"`,
		`hx-include="#browser-project`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected %q in browser page body", marker)
		}
	}
}

// TestProjectsPageHTMXWiring asserts the projects page emits HTMX attributes. Satisfies REQ-108.
func TestProjectsPageHTMXWiring(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/projects?auth=ok", nil))
	body := rec.Body.String()
	for _, marker := range []string{
		`hx-get="/dashboard/projects/list"`,
		`hx-target="#projects-content"`,
		`hx-trigger`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected %q in projects page body", marker)
		}
	}
}

// ─── Judgment Day Round 2 Hotfix Tests ──────────────────────────────────────

// paginationCapturingStub captures the offset and limit passed to paginated list calls.
// Used to verify that page re-clamping re-computes the correct offset before the second store call.
// R3-6: mu protects capturedOffsets/capturedLimits from data races when used in parallel tests.
type paginationCapturingStub struct {
	parityStoreStub
	mu              sync.Mutex
	capturedOffsets []int
	capturedLimits  []int
	total           int
}

func (s *paginationCapturingStub) ListRecentObservationsPaginated(_, _, _ string, limit, offset int) ([]cloudstore.DashboardObservationRow, int, error) {
	s.mu.Lock()
	s.capturedOffsets = append(s.capturedOffsets, offset)
	s.capturedLimits = append(s.capturedLimits, limit)
	s.mu.Unlock()
	start := offset
	if start > len(s.parityStoreStub.observations) {
		start = len(s.parityStoreStub.observations)
	}
	end := offset + limit
	if end > len(s.parityStoreStub.observations) {
		end = len(s.parityStoreStub.observations)
	}
	return s.parityStoreStub.observations[start:end], s.total, nil
}

func (s *paginationCapturingStub) ListRecentSessionsPaginated(_, _ string, limit, offset int) ([]cloudstore.DashboardSessionRow, int, error) {
	s.mu.Lock()
	s.capturedOffsets = append(s.capturedOffsets, offset)
	s.capturedLimits = append(s.capturedLimits, limit)
	s.mu.Unlock()
	start := offset
	if start > len(s.parityStoreStub.sessions) {
		start = len(s.parityStoreStub.sessions)
	}
	end := offset + limit
	if end > len(s.parityStoreStub.sessions) {
		end = len(s.parityStoreStub.sessions)
	}
	return s.parityStoreStub.sessions[start:end], s.total, nil
}

func (s *paginationCapturingStub) ListRecentPromptsPaginated(_, _ string, limit, offset int) ([]cloudstore.DashboardPromptRow, int, error) {
	s.mu.Lock()
	s.capturedOffsets = append(s.capturedOffsets, offset)
	s.capturedLimits = append(s.capturedLimits, limit)
	s.mu.Unlock()
	start := offset
	if start > len(s.parityStoreStub.prompts) {
		start = len(s.parityStoreStub.prompts)
	}
	end := offset + limit
	if end > len(s.parityStoreStub.prompts) {
		end = len(s.parityStoreStub.prompts)
	}
	return s.parityStoreStub.prompts[start:end], s.total, nil
}

// build25Observations creates 25 dummy observation rows for pagination tests.
func build25Observations() []cloudstore.DashboardObservationRow {
	rows := make([]cloudstore.DashboardObservationRow, 25)
	for i := range rows {
		rows[i] = cloudstore.DashboardObservationRow{
			Project: "proj-a", SessionID: "s1",
			SyncID: url.QueryEscape("obs-" + string(rune('a'+i))),
			Type:   "decision", Title: "Obs " + string(rune('A'+i)),
			CreatedAt: "2026-04-23T10:00:00Z",
		}
	}
	return rows
}

// TestBrowserPaginationHonorsPageParam verifies that ?page=2&pageSize=10 fetches
// the second page (offset=10), not the first page (offset=0). R2-1.
func TestBrowserPaginationHonorsPageParam(t *testing.T) {
	obs := build25Observations()
	stub := &paginationCapturingStub{
		parityStoreStub: parityStoreStub{observations: obs},
		total:           25,
	}
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
		Store:   stub,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok&page=2&pageSize=10", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	// After fix: the handler must call the store with offset=10 (page 2 of 10).
	if len(stub.capturedOffsets) == 0 {
		t.Fatal("expected at least one store call")
	}
	lastOffset := stub.capturedOffsets[len(stub.capturedOffsets)-1]
	if lastOffset != 10 {
		t.Errorf("expected store called with offset=10 for page=2, got offset=%d (capturedOffsets=%v)", lastOffset, stub.capturedOffsets)
	}
}

// TestBrowserPaginationBeyondTotalClampsToLastPage verifies that ?page=5 with 25 total
// items and pageSize=10 clamps to page=3 (the last page), not page=5 or page=1. R2-1.
func TestBrowserPaginationBeyondTotalClampsToLastPage(t *testing.T) {
	obs := build25Observations()
	stub := &paginationCapturingStub{
		parityStoreStub: parityStoreStub{observations: obs},
		total:           25,
	}
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
		Store:   stub,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok&page=5&pageSize=10", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(stub.capturedOffsets) == 0 {
		t.Fatal("expected at least one store call")
	}
	lastOffset := stub.capturedOffsets[len(stub.capturedOffsets)-1]
	// page=5 clamped to page=3 (last) → offset=20.
	if lastOffset != 20 {
		t.Errorf("expected offset=20 (clamped to last page), got offset=%d (capturedOffsets=%v)", lastOffset, stub.capturedOffsets)
	}
}

// TestContributorLinkEscapesName verifies that contributor names with spaces and @
// are url.PathEscape'd in the href. R2-2.
// R6-1 update: links are rendered by the list partial (/dashboard/contributors/list),
// not the shell page (/dashboard/contributors).
func TestContributorLinkEscapesName(t *testing.T) {
	store := parityStoreStub{
		contributors: []cloudstore.DashboardContributorRow{
			{CreatedBy: "alan smith", Chunks: 1, Projects: 1},
			{CreatedBy: "user@domain.com", Chunks: 2, Projects: 1},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/contributors/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// url.PathEscape escapes spaces to %20 but leaves @ unescaped (@ is a valid path char per RFC 3986).
	if strings.Contains(body, `href="/dashboard/contributors/alan smith"`) {
		t.Error("expected space in contributor name to be URL-escaped, got raw space in href")
	}
	if !strings.Contains(body, `href="/dashboard/contributors/alan%20smith"`) {
		t.Errorf("expected escaped href /dashboard/contributors/alan%%20smith, body=%q", body)
	}
	// @ is a valid URI path character — url.PathEscape does not encode it.
	// The important fix is that spaces and truly unsafe chars (like ?) are encoded.
	if !strings.Contains(body, `href="/dashboard/contributors/user@domain.com"`) {
		t.Errorf("expected href /dashboard/contributors/user@domain.com in body=%q", body)
	}
}

// TestAdminSyncToggleHTMXReturnsHXRedirect verifies that HTMX POST returns 200 +
// HX-Redirect header instead of 303. R2-3.
func TestAdminSyncToggleHTMXReturnsHXRedirect(t *testing.T) {
	store := parityStoreStub{
		syncControls: []cloudstore.ProjectSyncControl{
			{Project: "proj-a", SyncEnabled: true},
		},
	}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	form := url.Values{"enabled": {"false"}, "reason": {"test"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/projects/proj-a/sync?auth=ok", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for HTMX sync toggle POST, got %d", rec.Code)
	}
	if hx := rec.Header().Get("HX-Redirect"); hx == "" {
		t.Error("expected HX-Redirect header to be set for HTMX POST")
	}
	if rec.Code == http.StatusSeeOther {
		t.Error("expected no 303 redirect for HTMX POST — HTMX ignores body on 303 and follows natively")
	}
}

// TestAdminSyncToggleHTMXPostIncludesFormFields verifies that the toggle form markup
// includes hx-include="closest form" on the submit buttons so sibling form fields
// (enabled, reason) are sent with the HTMX request. R2-4.
func TestAdminSyncToggleHTMXPostIncludesFormFields(t *testing.T) {
	store := parityStoreStub{
		syncControls: []cloudstore.ProjectSyncControl{
			{Project: "proj-a", SyncEnabled: true},
		},
	}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/projects/proj-a/sync/form?auth=ok", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-include="closest form"`) {
		t.Errorf("expected hx-include=\"closest form\" on toggle buttons, body=%q", body)
	}
}

// TestAdminProjectsRequires403ForNonAdmin verifies that /dashboard/admin/projects
// returns 403 for an authenticated non-admin user. R2-8.
func TestAdminProjectsRequires403ForNonAdmin(t *testing.T) {
	store := parityStoreStub{}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/projects?auth=ok", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin at /dashboard/admin/projects, got %d", rec.Code)
	}
}

// TestAdminContributorsRouteIsGone verifies that /dashboard/admin/contributors
// no longer serves the dedicated admin-contributors surface (R4-10: dead route deleted).
// Since GET /dashboard/ is a subtree catch-all, the URL now falls through to the
// dashboard home — the important thing is it is NOT a dedicated 403 admin gate.
func TestAdminContributorsRouteIsGone(t *testing.T) {
	store := parityStoreStub{}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/contributors?auth=ok", nil))
	// Must NOT return 403 (the old behavior of the now-removed handleAdminContributors).
	// The subtree catch-all returns 200 (dashboard home) or a redirect — both are acceptable.
	if rec.Code == http.StatusForbidden {
		t.Errorf("R4-10: /dashboard/admin/contributors still returns 403 — dead route not fully removed")
	}
	// Body must NOT contain CONTRIBUTOR SIGNAL from the contributors page (proves it's not the old handler).
	body := rec.Body.String()
	if strings.Contains(body, "CONTRIBUTOR SIGNAL") {
		t.Errorf("R4-10: /dashboard/admin/contributors body still shows CONTRIBUTOR SIGNAL — old handler still wired")
	}
}

// TestAdminPageSurfacePresent asserts admin page has ADMIN SURFACE copy. Satisfies REQ-107, REQ-112.
func TestAdminPageSurfacePresent(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{
		systemHealth: cloudstore.DashboardSystemHealth{DBConnected: true, Projects: 1, Sessions: 5},
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ADMIN SURFACE") {
		t.Errorf("expected ADMIN SURFACE copy in admin page body")
	}
}

// TestAdminHealthPageRendersMetrics asserts admin health page renders DB status and counts. Satisfies REQ-106.
func TestAdminHealthPageRendersMetrics(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{
		systemHealth: cloudstore.DashboardSystemHealth{DBConnected: true, Projects: 2, Sessions: 10, Observations: 50, Prompts: 5},
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/health?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin health page, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{"ADMIN SURFACE", "Connected", "System Health"} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected %q in admin health body", marker)
		}
	}
}

// TestAdminUsersPageRendersManagedUsersShell asserts admin users page shell has the
// correct HTMX trigger for the managed-users list partial. Satisfies REQ-106.
// cloud-user-token-management PR4: this surface renders managed cloud
// principals, not contributor analytics (renamed from
// TestAdminUsersPageRendersContributors).
// R6-1 update: the shell no longer embeds inline rows — it contains hx-get="/dashboard/admin/users/list".
// Managed user rows are served by the /list partial endpoint (tested separately).
func TestAdminUsersPageRendersManagedUsersShell(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{
		managedUsers: []cloudstore.HumanUser{
			{PrincipalID: "p-agent", Username: "agent", Role: cloudstore.PrincipalRoleMember, Enabled: true},
		},
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/users?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin users page, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Shell must carry the HTMX load trigger so the list partial is fetched by the browser.
	if !strings.Contains(body, `hx-get="/dashboard/admin/users/list"`) {
		t.Errorf("expected hx-get trigger for /dashboard/admin/users/list in admin users shell body")
	}
}

// TestAdminSyncTogglePosts asserts POST /dashboard/admin/projects/myproject/sync with admin=true
// returns 303 redirect. Satisfies REQ-112.
func TestAdminSyncTogglePosts(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{isProjectSyncEnabled: true})
	body := strings.NewReader("enabled=false&reason=maintenance")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/projects/myproject/sync?auth=ok", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect for admin sync toggle, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminSyncToggleRequiresAdmin asserts POST /dashboard/admin/projects/*/sync returns 403 for non-admin. Satisfies REQ-112.
func TestAdminSyncToggleRequiresAdmin(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	body := strings.NewReader("enabled=false&reason=maintenance")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/projects/myproject/sync?auth=ok", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin sync toggle, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestFullHTMXEndpointSurface asserts all 11 new routes return 200 + text/html for authed principal. Satisfies REQ-106.
func TestFullHTMXEndpointSurface(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{
		projects:     []cloudstore.DashboardProjectRow{{Project: "proj-a", Sessions: 1}},
		sessions:     []cloudstore.DashboardSessionRow{{Project: "proj-a", SessionID: "s1", StartedAt: "2026-04-23T10:00:00Z"}},
		observations: []cloudstore.DashboardObservationRow{{Project: "proj-a", SessionID: "s1", SyncID: "sync-obs-c1", ChunkID: "c1", Type: "decision", Title: "T", CreatedAt: "2026-04-23T10:01:00Z"}},
		prompts:      []cloudstore.DashboardPromptRow{{Project: "proj-a", SessionID: "s1", SyncID: "sync-prompt-c1", ChunkID: "c1", Content: "P", CreatedAt: "2026-04-23T10:02:00Z"}},
		contributors: []cloudstore.DashboardContributorRow{{CreatedBy: "agent", Chunks: 1}},
		systemHealth: cloudstore.DashboardSystemHealth{DBConnected: true, Projects: 1},
	})

	routes := []string{
		"/dashboard/projects/list?auth=ok",
		"/dashboard/projects/proj-a/observations?auth=ok",
		"/dashboard/projects/proj-a/sessions?auth=ok",
		"/dashboard/projects/proj-a/prompts?auth=ok",
		"/dashboard/admin/users?auth=ok",
		"/dashboard/admin/health?auth=ok",
		"/dashboard/admin/projects/proj-a/sync/form?auth=ok",
		"/dashboard/sessions/proj-a/s1?auth=ok",
		"/dashboard/observations/proj-a/s1/sync-obs-c1?auth=ok",
		"/dashboard/prompts/proj-a/s1/sync-prompt-c1?auth=ok",
	}

	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, route, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d body=%q", route, rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
				t.Errorf("expected text/html Content-Type for %s, got %q", route, ct)
			}
		})
	}
}

// TestCopyParityStrings asserts key copy strings are present on each page. Satisfies REQ-111.
func TestCopyParityStrings(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{
		systemHealth: cloudstore.DashboardSystemHealth{DBConnected: true},
	})

	tests := []struct {
		path   string
		marker string
	}{
		{"/dashboard/browser?auth=ok", "KNOWLEDGE BROWSER"},
		{"/dashboard/projects?auth=ok", "PROJECT ATLAS"},
		{"/dashboard/contributors?auth=ok", "CONTRIBUTOR SIGNAL"},
		{"/dashboard/admin?auth=ok", "ADMIN SURFACE"},
		{"/dashboard/login", "Engram Cloud"},
		{"/dashboard/login", "CLOUD ACTIVE"},
	}

	for _, tt := range tests {
		t.Run(tt.path+":"+tt.marker, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if !strings.Contains(rec.Body.String(), tt.marker) {
				t.Errorf("expected %q in %s body", tt.marker, tt.path)
			}
		})
	}
}

// ─── Post-verify Hotfix: Dashboard Handler Regression Tests ──────────────────

// TestObservationCardHrefIsNotMalformed (Bug 2 handler-level regression)
// URL scheme migration: uses SyncID (not ChunkID) as the third URL segment.
// Asserts that /dashboard/browser/observations renders observation card hrefs where
// all three path segments (project, sessionID, syncID) are non-empty.
// An empty SyncID produces /dashboard/observations/proj/sess// which the router
// cannot match and falls back to the dashboard home.
func TestObservationCardHrefIsNotMalformed(t *testing.T) {
	store := parityStoreStub{
		observations: []cloudstore.DashboardObservationRow{
			{
				Project:   "proj-a",
				SessionID: "sess-abc",
				SyncID:    "sync-obs-xyz", // SyncID is the URL segment (C1 URL migration)
				ChunkID:   "chunk-xyz",
				Type:      "decision",
				Title:     "Well-formed href test",
				Content:   "Non-empty content so card renders fully",
				CreatedAt: "2026-04-23T10:00:00Z",
			},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Expect the full well-formed href using SyncID as the third segment.
	wantHref := `/dashboard/observations/proj-a/sess-abc/sync-obs-xyz`
	if !strings.Contains(body, wantHref) {
		t.Errorf("Bug 2 (migrated to syncID): expected well-formed href %q in browser observations body; malformed href will redirect to home\nbody=%q", wantHref, body)
	}
	// Must NOT contain a double-slash pattern that indicates a missing segment.
	if strings.Contains(body, `href="/dashboard/observations/proj-a/sess-abc/"`) ||
		strings.Contains(body, `href="/dashboard/observations/proj-a//"`) ||
		strings.Contains(body, `href="/dashboard/observations/proj-a//`) {
		t.Errorf("Bug 2: malformed href with empty segment found in browser observations body\nbody=%q", body)
	}
	// Content must render (not "No content captured.").
	if strings.Contains(body, "No content captured.") {
		t.Errorf("Bug 1: 'No content captured.' found in browser observations — Content field not propagated\nbody=%q", body)
	}
}

// TestSessionDetailRendersItsObservations (Bug 3 handler-level regression)
// Asserts that GET /dashboard/sessions/{proj}/{sid} renders observation rows from
// that session (not the empty state "No Observations / No Signal Yet").
// When the store's GetSessionDetail returns observations, the template must render them.
func TestSessionDetailRendersItsObservations(t *testing.T) {
	store := parityStoreStub{
		sessions: []cloudstore.DashboardSessionRow{
			{Project: "proj-a", SessionID: "sess-detail-x", StartedAt: "2026-04-23T08:00:00Z"},
		},
		observations: []cloudstore.DashboardObservationRow{
			{
				Project:   "proj-a",
				SessionID: "sess-detail-x",
				SyncID:    "sync-detail-x",
				ChunkID:   "chunk-detail-x",
				Type:      "decision",
				Title:     "Session Detail Observation",
				Content:   "This content must appear in the session trace",
				CreatedAt: "2026-04-23T08:10:00Z",
			},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/sessions/proj-a/sess-detail-x?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Must render the observation title.
	if !strings.Contains(body, "Session Detail Observation") {
		t.Errorf("Bug 3: session detail did not render seeded observation title\nbody=%q", body)
	}
	// Must NOT render the empty-state message.
	if strings.Contains(body, "No Observations") || strings.Contains(body, "No Signal Yet") {
		t.Errorf("Bug 3: session detail rendered empty state despite seeded observations\nbody=%q", body)
	}
}

// ─── Post-verify layout hotfix — Bug 5 ───────────────────────────────────────

// TestSessionsTableWrappedInScrollContainer asserts that SessionsPartial wraps the
// <table> element in a div.table-scroll so that wide session tables can scroll
// horizontally instead of being clipped by the app-shell overflow:hidden.
// Regression guard for Bug 5: "STARTED column truncated on contributor detail page".
func TestSessionsTableWrappedInScrollContainer(t *testing.T) {
	store := parityStoreStub{
		contributors: []cloudstore.DashboardContributorRow{
			{CreatedBy: "alan", Chunks: 10, Projects: 3, LastChunkAt: "2026-04-23T10:00:00Z"},
		},
		sessions: []cloudstore.DashboardSessionRow{
			{Project: "proj-a", SessionID: "sess-layout-x", StartedAt: "2026-04-23T08:00:00Z"},
		},
	}
	mux := newAuthedMux(store, false)

	t.Run("contributor detail page wraps session table in table-scroll", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/contributors/alan?auth=ok", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !hasElementWithClass(body, "div", "table-scroll") {
			t.Errorf("Bug 5: expected div.table-scroll wrapper around session table in contributor detail, body=%q", body)
		}
	})

	t.Run("browser sessions partial wraps session table in table-scroll", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/sessions?auth=ok", nil)
		req.Header.Set("HX-Request", "true")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !hasElementWithClass(body, "div", "table-scroll") {
			t.Errorf("Bug 5: expected div.table-scroll wrapper around session table in browser sessions partial, body=%q", body)
		}
	})
}

// ─── Judgment Day Hotfix Tests ────────────────────────────────────────────────

// TestObservationDetailURLUsesSyncID (C1 URL scheme) asserts that observation card
// hrefs use the SyncID path segment (not ChunkID), so distinct observations in the
// same chunk get distinct URLs.
func TestObservationDetailURLUsesSyncID(t *testing.T) {
	store := parityStoreStub{
		observations: []cloudstore.DashboardObservationRow{
			{
				Project: "proj-a", SessionID: "sess-1",
				SyncID:  "sync-obs-alpha",
				ChunkID: "shared-chunk-x",
				Type:    "decision", Title: "Alpha", CreatedAt: "2026-04-23T08:10:00Z",
			},
			{
				Project: "proj-a", SessionID: "sess-1",
				SyncID:  "sync-obs-beta",
				ChunkID: "shared-chunk-x",
				Type:    "bugfix", Title: "Beta", CreatedAt: "2026-04-23T08:11:00Z",
			},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Both observations must have distinct hrefs using SyncID not ChunkID.
	if !strings.Contains(body, "/dashboard/observations/proj-a/sess-1/sync-obs-alpha") {
		t.Errorf("C1: expected href with syncID=sync-obs-alpha, body=%q", body)
	}
	if !strings.Contains(body, "/dashboard/observations/proj-a/sess-1/sync-obs-beta") {
		t.Errorf("C1: expected href with syncID=sync-obs-beta, body=%q", body)
	}
	// Must NOT contain the chunkID as the URL segment.
	if strings.Contains(body, "/dashboard/observations/proj-a/sess-1/shared-chunk-x") {
		t.Errorf("C1: observation href still uses ChunkID instead of SyncID, body=%q", body)
	}
}

// TestPromptDetailURLUsesSyncID (C1 URL scheme for prompts).
func TestPromptDetailURLUsesSyncID(t *testing.T) {
	store := parityStoreStub{
		prompts: []cloudstore.DashboardPromptRow{
			{
				Project: "proj-a", SessionID: "sess-1",
				SyncID:  "sync-prompt-alpha",
				ChunkID: "shared-chunk-p",
				Content: "Alpha prompt", CreatedAt: "2026-04-23T08:10:00Z",
			},
			{
				Project: "proj-a", SessionID: "sess-1",
				SyncID:  "sync-prompt-beta",
				ChunkID: "shared-chunk-p",
				Content: "Beta prompt", CreatedAt: "2026-04-23T08:11:00Z",
			},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/prompts?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/dashboard/prompts/proj-a/sess-1/sync-prompt-alpha") {
		t.Errorf("C1: expected href with syncID=sync-prompt-alpha, body=%q", body)
	}
	if !strings.Contains(body, "/dashboard/prompts/proj-a/sess-1/sync-prompt-beta") {
		t.Errorf("C1: expected href with syncID=sync-prompt-beta, body=%q", body)
	}
	if strings.Contains(body, "/dashboard/prompts/proj-a/sess-1/shared-chunk-p") {
		t.Errorf("C1: prompt href still uses ChunkID instead of SyncID, body=%q", body)
	}
}

// TestAdminUsersRequires403ForNonAdmin (C5 RED).
func TestAdminUsersRequires403ForNonAdmin(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false) // isAdmin=false
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/users?auth=ok", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("C5: expected 403 for non-admin GET /dashboard/admin/users, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminHealthRequires403ForNonAdmin (C5 RED).
func TestAdminHealthRequires403ForNonAdmin(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false) // isAdmin=false
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/health?auth=ok", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("C5: expected 403 for non-admin GET /dashboard/admin/health, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminSyncToggleFormRequires403ForNonAdmin (C5 RED).
func TestAdminSyncToggleFormRequires403ForNonAdmin(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false) // isAdmin=false
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/projects/proj-a/sync/form?auth=ok", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("C5: expected 403 for non-admin GET /dashboard/admin/projects/*/sync/form, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestDashboardHomeStatCardsAreClickable (C6 RED) asserts that the stats partial
// renders stat cards wrapped in <a> elements (clickable links to filtered views).
func TestDashboardHomeStatCardsAreClickable(t *testing.T) {
	stats := []cloudstore.DashboardProjectRow{
		{Project: "proj-a", Sessions: 5, Observations: 10, Prompts: 3},
		{Project: "proj-b", Sessions: 2, Observations: 4, Prompts: 1},
	}
	// Render DashboardStatsPartial directly by triggering the /dashboard/stats endpoint.
	store := parityStoreStub{
		projects:      stats,
		adminOverview: cloudstore.DashboardAdminOverview{Projects: 2, Contributors: 3, Chunks: 42},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/stats?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for /dashboard/stats, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Stat cards must be wrapped in <a> tags pointing to meaningful drill-down URLs.
	// The exact paths are defined in components.templ DashboardStatsPartial.
	for _, wantHref := range []string{
		`href="/dashboard/browser`,
		`href="/dashboard/projects`,
	} {
		if !strings.Contains(body, wantHref) {
			t.Errorf("C6: expected stat card link %q in /dashboard/stats body, got body=%q", wantHref, body)
		}
	}
	// Verify via HTML parse that at least one <a> element has the metric-card class.
	// (The <a> IS the metric-card — either as <a class="metric-card ..."> or as
	// an <a> wrapping a <div class="metric-card"> — both are acceptable.)
	doc, err := nethtml.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("C6: failed to parse HTML: %v", err)
	}
	foundLinkedCard := false
	var walkC6 func(*nethtml.Node)
	walkC6 = func(n *nethtml.Node) {
		if n.Type == nethtml.ElementNode && n.Data == "a" {
			// Check if <a> itself has metric-card class.
			for _, attr := range n.Attr {
				if attr.Key == "class" && strings.Contains(attr.Val, "metric-card") {
					foundLinkedCard = true
					return
				}
			}
			// Check if <a> wraps a metric-card child.
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == nethtml.ElementNode {
					for _, attr := range child.Attr {
						if attr.Key == "class" && strings.Contains(attr.Val, "metric-card") {
							foundLinkedCard = true
							return
						}
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walkC6(child)
		}
	}
	walkC6(doc)
	if !foundLinkedCard {
		t.Errorf("C6: expected at least one metric-card linked via <a> in /dashboard/stats body, got body=%q", body)
	}
}

// TestAdminSyncToggleRejectsInvalidEnabled (W2 RED) asserts that POST with
// empty/missing/garbage enabled value returns 400.
func TestAdminSyncToggleRejectsInvalidEnabled(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{})
	for _, tc := range []struct {
		name    string
		enabled string
	}{
		{"empty", ""},
		{"missing", ""},
		{"garbage", "yes"},
		{"True-capital", "True"},
		{"FALSE-capital", "FALSE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"enabled": {tc.enabled}, "reason": {"test"}}
			req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/projects/proj-a/sync?auth=ok", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("W2: expected 400 for enabled=%q, got %d body=%q", tc.enabled, rec.Code, rec.Body.String())
			}
		})
	}
}

// ─── Judgment Day Round 3 Hotfix Tests ──────────────────────────────────────

// R3-1: TestBrowserPartialRendersPaginationBar — GET /dashboard/browser/observations
// with 25 items and pageSize=10 must render pagination controls (data-page or page-btn).
func TestBrowserPartialRendersPaginationBar(t *testing.T) {
	obs := build25Observations()
	stub := &paginationCapturingStub{
		parityStoreStub: parityStoreStub{observations: obs},
		total:           25,
	}
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
		Store:   stub,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok&page=1&pageSize=10", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R3-1: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The HtmxPaginationBar must emit elements with class="page-btn".
	if !strings.Contains(body, "page-btn") {
		t.Errorf("R3-1: expected pagination controls (class=page-btn) in ObservationsPartial, got body=%q", body)
	}
	// Also must contain a next-page link (25 items, page=1, size=10 → page 2 exists).
	if !strings.Contains(body, "page=2") {
		t.Errorf("R3-1: expected page=2 link in pagination bar, got body=%q", body)
	}
}

// R3-1b: TestBrowserSessionsPartialRendersPaginationBar — same for sessions.
func TestBrowserSessionsPartialRendersPaginationBar(t *testing.T) {
	sessions := make([]cloudstore.DashboardSessionRow, 25)
	for i := range sessions {
		sessions[i] = cloudstore.DashboardSessionRow{
			Project: "proj-a", SessionID: "s" + strings.Repeat("0", i),
			StartedAt: "2026-04-23T10:00:00Z",
		}
	}
	stub := &paginationCapturingStub{
		parityStoreStub: parityStoreStub{sessions: sessions},
		total:           25,
	}
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
		Store:   stub,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/sessions?auth=ok&page=1&pageSize=10", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R3-1b: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "page-btn") {
		t.Errorf("R3-1b: expected pagination controls in SessionsPartial, got body=%q", body)
	}
}

// R3-1c: TestBrowserPromptsPartialRendersPaginationBar — same for prompts.
func TestBrowserPromptsPartialRendersPaginationBar(t *testing.T) {
	prompts := make([]cloudstore.DashboardPromptRow, 25)
	for i := range prompts {
		prompts[i] = cloudstore.DashboardPromptRow{
			Project: "proj-a", SessionID: "s1",
			SyncID: "p-" + strings.Repeat("0", i), Content: "prompt content",
			CreatedAt: "2026-04-23T10:00:00Z",
		}
	}
	stub := &paginationCapturingStub{
		parityStoreStub: parityStoreStub{prompts: prompts},
		total:           25,
	}
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
		Store:   stub,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/prompts?auth=ok&page=1&pageSize=10", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R3-1c: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "page-btn") {
		t.Errorf("R3-1c: expected pagination controls in PromptsPartial, got body=%q", body)
	}
}

// R3-2: TestContributorDetailChunksCardUsesQQueryParam — the CHUNKS stat card in
// ContributorDetailPage must link to /dashboard/browser?q=... not ?search=...
func TestContributorDetailChunksCardUsesQQueryParam(t *testing.T) {
	store := parityStoreStub{
		contributors: []cloudstore.DashboardContributorRow{
			{CreatedBy: "alice", Chunks: 42, Projects: 3, LastChunkAt: "2026-04-23T10:00:00Z"},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/contributors/alice?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("R3-2: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Must use ?q= not ?search= to match the handler's r.URL.Query().Get("q").
	if strings.Contains(body, "?search=") {
		t.Errorf("R3-2: CHUNKS card uses ?search= but handler reads ?q= — found ?search= in body")
	}
	if !strings.Contains(body, "?q=") {
		t.Errorf("R3-2: expected ?q= in CHUNKS card href, got body=%q", body)
	}
}

// contributorsPaginationStub is a store stub that returns a real total count
// independent of the actual rows slice, to exercise the R3-3 real-total fix.
type contributorsPaginationStub struct {
	parityStoreStub
	mu              sync.Mutex
	capturedOffsets []int
	capturedLimits  []int
	allContributors []cloudstore.DashboardContributorRow
	total           int
}

func (s *contributorsPaginationStub) ListContributorsPaginated(_ string, limit, offset int) ([]cloudstore.DashboardContributorRow, int, error) {
	s.mu.Lock()
	s.capturedOffsets = append(s.capturedOffsets, offset)
	s.capturedLimits = append(s.capturedLimits, limit)
	s.mu.Unlock()
	start := offset
	if start > len(s.allContributors) {
		start = len(s.allContributors)
	}
	end := offset + limit
	if end > len(s.allContributors) {
		end = len(s.allContributors)
	}
	return s.allContributors[start:end], s.total, nil
}

// R3-3: TestContributorsPaginationUsesRealTotal — seed 75 contributors,
// request page=1 on the /list endpoint. TotalItems in the rendered output must be 75, not 50.
// R6-1 update: pagination is now rendered by /dashboard/contributors/list (the partial endpoint),
// not the /dashboard/contributors shell page.
func TestContributorsPaginationUsesRealTotal(t *testing.T) {
	contributors := make([]cloudstore.DashboardContributorRow, 75)
	for i := range contributors {
		contributors[i] = cloudstore.DashboardContributorRow{
			CreatedBy: "user-" + strings.Repeat("0", i%10) + strings.Repeat("1", i%5),
			Chunks:    i + 1, Projects: 1,
		}
	}
	stub := &contributorsPaginationStub{
		allContributors: contributors,
		total:           75,
	}
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
		Store:   stub,
	})

	rec := httptest.NewRecorder()
	// R6-1: hit the list partial endpoint (pagination is there, not the shell).
	req := httptest.NewRequest(http.MethodGet, "/dashboard/contributors/list?auth=ok&page=1&pageSize=10", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R3-3: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// HtmxPaginationBar renders "1–10 of 75" — the total must be 75, not 50.
	if strings.Contains(body, "of 50") {
		t.Errorf("R3-3: pagination shows 'of 50' (capped), expected 'of 75' (real total)")
	}
	if !strings.Contains(body, "of 75") {
		t.Errorf("R3-3: expected 'of 75' in pagination output for contributors, got body=%q", body)
	}
}

// TestManagedUsersListRendersAllUsersWithoutPagination (cloud-user-token-management
// PR4) asserts /dashboard/admin/users/list renders every managed human user
// from ManagedUsersStore, distinct from contributor analytics, and does not
// apply the contributor-analytics pagination behavior (managed accounts are
// expected to stay small in the MVP, unlike synced contributor volume).
func TestManagedUsersListRendersAllUsersWithoutPagination(t *testing.T) {
	users := make([]cloudstore.HumanUser, 12)
	for i := range users {
		users[i] = cloudstore.HumanUser{
			PrincipalID: "p-user-" + strings.Repeat("x", i%3),
			Username:    "managed-user-" + strings.Repeat("z", i%4),
			Role:        cloudstore.PrincipalRoleMember,
			Enabled:     true,
		}
	}
	// A contributor fixture is also present to prove the managed-users list
	// does not fall back to (or mix in) contributor analytics.
	store := parityStoreStub{
		managedUsers: users,
		contributors: []cloudstore.DashboardContributorRow{{CreatedBy: "not-a-managed-user", Chunks: 5}},
	}
	mux := newAuthedAdminMux(store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/users/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "not-a-managed-user") {
		t.Errorf("managed users list must not render contributor analytics rows, body=%q", body)
	}
	for i := range users {
		if !strings.Contains(body, users[i].Username) {
			t.Errorf("expected managed user %q in list body, got %q", users[i].Username, body[:min(len(body), 800)])
		}
	}
	if strings.Contains(body, "pagination-bar") {
		t.Errorf("managed users list is not expected to paginate in the MVP, got pagination markup in body=%q", body[:min(len(body), 800)])
	}
}

// TestManagedUsersPageHasCreateUserForm (cloud-user-token-management PR4)
// asserts the managed-users shell contains a create-user form that posts to
// the cloudserver-owned mutation route. The dashboard package never decides
// authorization itself — it only renders the form; internal/cloud/cloudserver
// enforces managed-admin authorization and audit on the POST handler.
func TestManagedUsersPageHasCreateUserForm(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/users?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{
		`action="/dashboard/admin/users"`,
		`name="username"`,
		`name="role"`,
		"deny-by-default",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected %q in managed users create-user form, body=%q", marker, body[:min(len(body), 1200)])
		}
	}
}

// TestManagedUsersListPartialHasEnableDisableFormsAndEmptyState (cloud-user-token-management
// PR4) asserts the list partial renders per-user enable/disable forms wired to
// the cloudserver-owned mutation routes, and an empty-state that explains
// deny-by-default project grants when there are no managed users yet.
func TestManagedUsersListPartialHasEnableDisableFormsAndEmptyState(t *testing.T) {
	enabledStore := parityStoreStub{managedUsers: []cloudstore.HumanUser{
		{PrincipalID: "p-enabled", Username: "enabled-user", Role: cloudstore.PrincipalRoleMember, Enabled: true},
		{PrincipalID: "p-disabled", Username: "disabled-user", Role: cloudstore.PrincipalRoleMember, Enabled: false},
	}}
	mux := newAuthedAdminMux(enabledStore)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/users/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{
		`action="/dashboard/admin/users/p-enabled/disable"`,
		`action="/dashboard/admin/users/p-disabled/enable"`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected %q in managed users list, body=%q", marker, body[:min(len(body), 1200)])
		}
	}

	emptyMux := newAuthedAdminMux(parityStoreStub{})
	emptyRec := httptest.NewRecorder()
	emptyReq := httptest.NewRequest(http.MethodGet, "/dashboard/admin/users/list?auth=ok", nil)
	emptyReq.Header.Set("HX-Request", "true")
	emptyMux.ServeHTTP(emptyRec, emptyReq)
	if emptyRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty managed users list, got %d body=%q", emptyRec.Code, emptyRec.Body.String())
	}
	if !strings.Contains(emptyRec.Body.String(), "deny-by-default") {
		t.Errorf("expected empty managed users list to explain deny-by-default project grants, body=%q", emptyRec.Body.String())
	}
}

// ─── Judgment Day Round 4 Hotfix Tests ──────────────────────────────────────

// TestHtmxPaginationBarNoMalformedURL (R4-2 RED) asserts that HtmxPaginationBar
// generates URLs with "?page=N" not "?&page=N". The fix is to use endpoint without
// trailing "?" and format with "?page=" separator.
func TestHtmxPaginationBarNoMalformedURL(t *testing.T) {
	obs := build25Observations()
	stub := &paginationCapturingStub{
		parityStoreStub: parityStoreStub{observations: obs},
		total:           25,
	}
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
		Store:   stub,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok&page=1&pageSize=10", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R4-2: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Must NOT contain ?& pattern (malformed URL).
	if strings.Contains(body, "?&") {
		t.Errorf("R4-2: malformed URL pattern '?&' found in pagination bar output, body=%q", body)
	}
	// Must contain a valid ?page= pattern.
	if !strings.Contains(body, "?page=") {
		t.Errorf("R4-2: expected '?page=' in pagination bar hx-get attributes, got body=%q", body)
	}
}

// projectsListPaginationStub overrides ListProjectsPaginated to report a real total.
type projectsListPaginationStub struct {
	parityStoreStub
	mu           sync.Mutex
	allProjects  []cloudstore.DashboardProjectRow
	total        int
	capturedOffs []int
	capturedLims []int
}

func (s *projectsListPaginationStub) ListProjectsPaginated(_ string, limit, offset int) ([]cloudstore.DashboardProjectRow, int, error) {
	s.mu.Lock()
	s.capturedOffs = append(s.capturedOffs, offset)
	s.capturedLims = append(s.capturedLims, limit)
	s.mu.Unlock()
	start := offset
	if start > len(s.allProjects) {
		start = len(s.allProjects)
	}
	end := offset + limit
	if end > len(s.allProjects) {
		end = len(s.allProjects)
	}
	return s.allProjects[start:end], s.total, nil
}

// TestProjectsListPaginationUsesRealTotal (R4-3 RED) seeds 75 projects,
// requests ?page=1 and asserts TotalItems=75 is reflected in the rendered output
// (not hardcoded to the first 50).
func TestProjectsListPaginationUsesRealTotal(t *testing.T) {
	projects := make([]cloudstore.DashboardProjectRow, 75)
	for i := range projects {
		projects[i] = cloudstore.DashboardProjectRow{
			Project: "proj-" + strings.Repeat("a", i%5+1),
			Chunks:  i + 1,
		}
	}
	stub := &projectsListPaginationStub{
		allProjects: projects,
		total:       75,
	}
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
		Store:   stub,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/projects/list?auth=ok&page=1&pageSize=10", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R4-3: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// PaginationBar must render "of 75" not "of 50" (hardcoded).
	if strings.Contains(body, "of 50") && !strings.Contains(body, "of 75") {
		t.Errorf("R4-3: projects list pagination shows 'of 50' (hardcoded), expected 'of 75' (real total)")
	}
	if !strings.Contains(body, "of 75") {
		t.Errorf("R4-3: expected 'of 75' in projects list pagination output, got body=%q", body)
	}
}

// TestNavLinksDoNotLeakQueryParams (R4-6 RED) asserts that the shell nav links
// do NOT append the current page's query params to non-active tabs.
// E.g. navigating from /dashboard/browser?page=3&q=auth → clicking "Projects"
// should NOT produce href="/dashboard/projects?page=3&q=auth".
func TestNavLinksDoNotLeakQueryParams(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	rec := httptest.NewRecorder()
	// Request the browser page with query params that must NOT leak into nav hrefs.
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser?auth=ok&page=3&q=auth", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R4-6: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Projects nav link must not have ?page=3 appended.
	if strings.Contains(body, `href="/dashboard/projects?`) {
		t.Errorf("R4-6: nav link for Projects has query params leaked from current URL: body=%q", body)
	}
	// Contributors nav link must not have stale params.
	if strings.Contains(body, `href="/dashboard/contributors?`) {
		t.Errorf("R4-6: nav link for Contributors has query params leaked from current URL: body=%q", body)
	}
}

// TestContributorNotFoundReturns404WithContributorMessage (R4-7 RED) asserts that
// a missing contributor returns 404 with "Contributor not found" headline (not "Project not found").
func TestContributorNotFoundReturns404WithContributorMessage(t *testing.T) {
	// Store with no contributors so GetContributorDetail returns not-found.
	store := parityStoreStub{contributors: nil}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/contributors/nobody?auth=ok", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("R4-7: expected 404 for missing contributor, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Must NOT say "Project not found" — that's the wrong error.
	if strings.Contains(body, "Project not found") {
		t.Errorf("R4-7: contributor-not-found error says 'Project not found' — expected 'Contributor not found'")
	}
	// Must say something about contributor not found.
	if !strings.Contains(body, "Contributor not found") && !strings.Contains(body, "contributor") {
		t.Errorf("R4-7: expected 'Contributor not found' in 404 body for missing contributor, got body=%q", body)
	}
}

// ─── R5-1: Stats and Activity full-page layout uses templ Layout ─────────────

// TestDashboardStatsFullPageShowsStatusRibbon (R5-1 RED) asserts that a non-HTMX
// GET to /dashboard/stats returns the full templ Layout including status-ribbon,
// "CLOUD ACTIVE" pill, and the footer copy.
func TestDashboardStatsFullPageShowsStatusRibbon(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	rec := httptest.NewRecorder()
	// No HX-Request header => full-page navigation path.
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/stats?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("R5-1: expected 200 for /dashboard/stats, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{
		"status-ribbon",
		"CLOUD ACTIVE",
		"ENGRAM CLOUD / SHARED MEMORY INDEX / LIVE SYNC READY",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("R5-1: expected %q in /dashboard/stats full-page body, got body=%q", marker, body[:min(len(body), 500)])
		}
	}
}

// TestDashboardActivityFullPageShowsStatusRibbon (R5-1) asserts the same for /dashboard/activity.
func TestDashboardActivityFullPageShowsStatusRibbon(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/activity?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("R5-1: expected 200 for /dashboard/activity, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{
		"status-ribbon",
		"CLOUD ACTIVE",
		"ENGRAM CLOUD / SHARED MEMORY INDEX / LIVE SYNC READY",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("R5-1: expected %q in /dashboard/activity full-page body, got body=%q", marker, body[:min(len(body), 500)])
		}
	}
}

// ─── R5-4: Session/Observation/Prompt detail not-found handler tests ─────────

// TestSessionDetailNotFoundReturns404 (R5-4) asserts that GET /dashboard/sessions/{project}/{sessionID}
// for a missing session returns 404 with "Session not found" (not "Project not found").
func TestSessionDetailNotFoundReturns404(t *testing.T) {
	store := parityStoreStub{
		errGetSessionDetail: fmt.Errorf("%w: sess-missing", cloudstore.ErrDashboardSessionNotFound),
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/sessions/proj-valid/sess-missing?auth=ok", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("R5-4: expected 404 for missing session detail, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "Project not found") {
		t.Errorf("R5-4: session not-found handler says 'Project not found' — must say 'Session not found'")
	}
	if !strings.Contains(body, "Session not found") {
		t.Errorf("R5-4: expected 'Session not found' in 404 body, got body=%q", body[:min(len(body), 500)])
	}
}

// TestObservationDetailNotFoundReturns404 (R5-4) asserts that GET /dashboard/observations/{project}/{sessionID}/{syncID}
// for a missing observation returns 404 with "Observation not found".
func TestObservationDetailNotFoundReturns404(t *testing.T) {
	store := parityStoreStub{
		errGetObservationDetail: fmt.Errorf("%w: obs-missing", cloudstore.ErrDashboardObservationNotFound),
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/observations/proj-valid/sess-1/obs-missing?auth=ok", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("R5-4: expected 404 for missing observation detail, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "Project not found") {
		t.Errorf("R5-4: observation not-found handler says 'Project not found' — must say 'Observation not found'")
	}
	if !strings.Contains(body, "Observation not found") {
		t.Errorf("R5-4: expected 'Observation not found' in 404 body, got body=%q", body[:min(len(body), 500)])
	}
}

// TestPromptDetailNotFoundReturns404 (R5-4) asserts that GET /dashboard/prompts/{project}/{sessionID}/{syncID}
// for a missing prompt returns 404 with "Prompt not found".
func TestPromptDetailNotFoundReturns404(t *testing.T) {
	store := parityStoreStub{
		errGetPromptDetail: fmt.Errorf("%w: prompt-missing", cloudstore.ErrDashboardPromptNotFound),
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/prompts/proj-valid/sess-1/prompt-missing?auth=ok", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("R5-4: expected 404 for missing prompt detail, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "Project not found") {
		t.Errorf("R5-4: prompt not-found handler says 'Project not found' — must say 'Prompt not found'")
	}
	if !strings.Contains(body, "Prompt not found") {
		t.Errorf("R5-4: expected 'Prompt not found' in 404 body, got body=%q", body[:min(len(body), 500)])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── R5-2: Contributors pagination HTMX swaps content ────────────────────────

// TestContributorsPaginationHTMXSwapsContent (R5-2 RED) asserts that
// GET /dashboard/contributors/list returns an HTMX-targetable partial
// (not a full page wrapper) when called directly.
func TestContributorsPaginationHTMXSwapsContent(t *testing.T) {
	store := parityStoreStub{
		contributors: []cloudstore.DashboardContributorRow{
			{CreatedBy: "alice", Chunks: 5, Projects: 2, LastChunkAt: "2026-04-23T10:00:00Z"},
			{CreatedBy: "bob", Chunks: 3, Projects: 1, LastChunkAt: "2026-04-23T11:00:00Z"},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/contributors/list?auth=ok&page=1", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R5-2: expected 200 from /dashboard/contributors/list, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Must contain contributor rows.
	if !strings.Contains(body, "alice") {
		t.Errorf("R5-2: expected contributor 'alice' in list partial, got body=%q", body[:min(len(body), 500)])
	}
	// Must NOT contain the full layout wrapper (status-ribbon = full page).
	if strings.Contains(body, "status-ribbon") {
		t.Errorf("R5-2: /dashboard/contributors/list returned a full-page layout (contains status-ribbon), expected a partial only")
	}
}

// TestAdminUsersPaginationHTMXSwapsContent (R5-2 RED) asserts that
// GET /dashboard/admin/users/list returns the partial (no full layout wrapper).
// cloud-user-token-management PR4: uses a managed-user fixture, not a
// contributor fixture — this surface renders managed cloud principals.
func TestAdminUsersPaginationHTMXSwapsContent(t *testing.T) {
	store := parityStoreStub{
		managedUsers: []cloudstore.HumanUser{
			{PrincipalID: "p-alice", Username: "alice", Role: cloudstore.PrincipalRoleMember, Enabled: true},
		},
	}
	mux := newAuthedMux(store, true) // admin=true required.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/users/list?auth=ok&page=1", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R5-2: expected 200 from /dashboard/admin/users/list, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice") {
		t.Errorf("R5-2: expected managed user 'alice' in admin users list partial, got body=%q", body[:min(len(body), 500)])
	}
	// Must NOT contain the full layout wrapper.
	if strings.Contains(body, "status-ribbon") {
		t.Errorf("R5-2: /dashboard/admin/users/list returned a full-page layout, expected a partial only")
	}
}

// ─── Judgment Day Round 6 Hotfix Tests ───────────────────────────────────────

// TestContributorsPageLoadsListPartialViaHTMX (R6-1 RED) asserts that
// GET /dashboard/contributors (non-HTMX, full page) renders a shell with
// hx-get="/dashboard/contributors/list" and does NOT contain the contributor
// table rows directly (those come from the HTMX load).
func TestContributorsPageLoadsListPartialViaHTMX(t *testing.T) {
	store := parityStoreStub{
		contributors: []cloudstore.DashboardContributorRow{
			{CreatedBy: "alice", Chunks: 5, Projects: 2, LastChunkAt: "2026-04-23T10:00:00Z"},
			{CreatedBy: "bob", Chunks: 3, Projects: 1, LastChunkAt: "2026-04-23T11:00:00Z"},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	// Non-HTMX request to the shell page.
	req := httptest.NewRequest(http.MethodGet, "/dashboard/contributors?auth=ok", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R6-1: expected 200 from /dashboard/contributors, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Shell must contain the hx-get trigger so HTMX will load the list.
	if !strings.Contains(body, `hx-get="/dashboard/contributors/list"`) {
		t.Errorf("R6-1: /dashboard/contributors shell missing hx-get trigger for list partial, body=%q", body[:min(len(body), 800)])
	}
	// Shell must NOT contain contributor table rows directly.
	if strings.Contains(body, "alice") || strings.Contains(body, "bob") {
		t.Errorf("R6-1: /dashboard/contributors shell should not embed contributor rows directly (they come via HTMX load), body=%q", body[:min(len(body), 800)])
	}
}

// TestAdminUsersPageLoadsListPartialViaHTMX (R6-1 RED) asserts that
// GET /dashboard/admin/users (non-HTMX) renders a shell with
// hx-get="/dashboard/admin/users/list" and no inline managed-user rows.
// cloud-user-token-management PR4: uses a managed-user fixture, not a
// contributor fixture.
func TestAdminUsersPageLoadsListPartialViaHTMX(t *testing.T) {
	store := parityStoreStub{
		managedUsers: []cloudstore.HumanUser{
			{PrincipalID: "p-carol", Username: "carol", Role: cloudstore.PrincipalRoleMember, Enabled: true},
		},
	}
	mux := newAuthedMux(store, true) // admin required
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/users?auth=ok", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R6-1: expected 200 from /dashboard/admin/users, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-get="/dashboard/admin/users/list"`) {
		t.Errorf("R6-1: /dashboard/admin/users shell missing hx-get trigger for list partial, body=%q", body[:min(len(body), 800)])
	}
	if strings.Contains(body, "carol") {
		t.Errorf("R6-1: /dashboard/admin/users shell should not embed managed user rows directly, body=%q", body[:min(len(body), 800)])
	}
}

// TestContributorsListPartialHasNoOuterWrapper (R6-1 RED) asserts that
// GET /dashboard/contributors/list does NOT wrap its content in an outer
// <div id="contributors-content"> (which would cause duplicate IDs when
// HTMX swaps innerHTML into the existing div).
func TestContributorsListPartialHasNoOuterWrapper(t *testing.T) {
	store := parityStoreStub{
		contributors: []cloudstore.DashboardContributorRow{
			{CreatedBy: "alice", Chunks: 5, Projects: 2, LastChunkAt: "2026-04-23T10:00:00Z"},
		},
	}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/contributors/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R6-1: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	// The partial must not re-wrap itself in #contributors-content (duplicate ID bug).
	if strings.Contains(body, `id="contributors-content"`) {
		t.Errorf("R6-1: ContributorsListPartial must not emit outer #contributors-content wrapper (causes duplicate IDs on HTMX swap)")
	}
}

// TestAdminUsersListPartialHasNoOuterWrapper (R6-1 RED) same check for managed users.
func TestAdminUsersListPartialHasNoOuterWrapper(t *testing.T) {
	store := parityStoreStub{
		managedUsers: []cloudstore.HumanUser{
			{PrincipalID: "p-alice", Username: "alice", Role: cloudstore.PrincipalRoleMember, Enabled: true},
		},
	}
	mux := newAuthedMux(store, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/users/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("R6-1: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `id="managed-users-content"`) {
		t.Errorf("R6-1: ManagedUsersListPartial must not emit outer #managed-users-content wrapper (causes duplicate IDs on HTMX swap)")
	}
}

// TestPartialOnlyEndpointErrorIsFragmentNotLayout (R6-2 RED) asserts that when a
// store error occurs on a partial-only endpoint and the caller is NOT an HTMX
// request, the response is a fragment (no Layout shell), not a full page with
// status-ribbon. Tests /dashboard/contributors/list.
func TestPartialOnlyEndpointErrorIsFragmentNotLayout(t *testing.T) {
	storeErr := errors.New("db unavailable")
	store := parityStoreStub{errListContributors: storeErr}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	// Non-HTMX request — this is the case the fix targets.
	req := httptest.NewRequest(http.MethodGet, "/dashboard/contributors/list?auth=ok", nil)
	mux.ServeHTTP(rec, req)
	// Must not return a full Layout page.
	body := rec.Body.String()
	if strings.Contains(body, "status-ribbon") {
		t.Errorf("R6-2: partial-only endpoint /dashboard/contributors/list returned full Layout on non-HTMX error (contains status-ribbon)")
	}
	// Must still render an error signal (not an empty response).
	if body == "" {
		t.Errorf("R6-2: partial-only endpoint returned empty body on store error")
	}
}

// TestAdminUsersListPartialErrorIsFragmentNotLayout (R6-2 RED) same check for
// /dashboard/admin/users/list.
func TestAdminUsersListPartialErrorIsFragmentNotLayout(t *testing.T) {
	storeErr := errors.New("db unavailable")
	// Need a store that fails on ListHumanUsers but still passes admin check.
	store := parityStoreStub{errManagedUsers: storeErr}
	mux := newAuthedMux(store, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/users/list?auth=ok", nil)
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "status-ribbon") {
		t.Errorf("R6-2: partial-only endpoint /dashboard/admin/users/list returned full Layout on non-HTMX error (contains status-ribbon)")
	}
	if body == "" {
		t.Errorf("R6-2: partial-only endpoint returned empty body on store error")
	}
}

// TestProjectsListPartialErrorIsFragmentNotLayout (R6-2 RED) same check for
// /dashboard/projects/list.
func TestProjectsListPartialErrorIsFragmentNotLayout(t *testing.T) {
	storeErr := errors.New("db unavailable")
	store := parityStoreStub{errListProjects: storeErr}
	mux := newAuthedMux(store, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/projects/list?auth=ok", nil)
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "status-ribbon") {
		t.Errorf("R6-2: partial-only endpoint /dashboard/projects/list returned full Layout on non-HTMX error (contains status-ribbon)")
	}
	if body == "" {
		t.Errorf("R6-2: partial-only endpoint returned empty body on store error")
	}
}

// TestAdminUsersListRequires403ForNonAdmin (R6-3 RED) asserts that
// GET /dashboard/admin/users/list with a non-admin principal returns 403.
func TestAdminUsersListRequires403ForNonAdmin(t *testing.T) {
	store := parityStoreStub{
		contributors: []cloudstore.DashboardContributorRow{
			{CreatedBy: "alice", Chunks: 5, Projects: 2, LastChunkAt: "2026-04-23T10:00:00Z"},
		},
	}
	mux := newAuthedMux(store, false) // isAdmin=false
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/users/list?auth=ok", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("R6-3: expected 403 for non-admin on /dashboard/admin/users/list, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── REQ-408, REQ-409, REQ-410, REQ-411: Audit Log UI tests ──────────────────

// TestAdminAuditLogPageHTMXWiring verifies that AdminAuditLogPage renders with
// the HTMX container that triggers loading the list partial. REQ-408 scenario 1, 2.5.1.
func TestAdminAuditLogPageHTMXWiring(t *testing.T) {
	store := parityStoreStub{}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-get="/dashboard/admin/audit-log/list"`) {
		t.Errorf("expected hx-get attr in audit log shell, got body=%q", body)
	}
	if !strings.Contains(body, `hx-trigger="load"`) {
		t.Errorf("expected hx-trigger=load in audit log shell, got body=%q", body)
	}
}

// TestAdminAuditLogListPartialRendersFilterInputs verifies that the list partial
// renders filter inputs and a pagination bar. REQ-409 scenario 1, 2.5.2.
func TestAdminAuditLogListPartialRendersFilterInputs(t *testing.T) {
	store := parityStoreStub{
		auditRows: []cloudstore.DashboardAuditRow{
			{ID: 1, OccurredAt: "2026-04-24T00:00:00Z", Contributor: "alice", Project: "proj-a",
				Action: cloudstore.AuditActionMutationPush, Outcome: cloudstore.AuditOutcomeRejectedProjectPaused},
		},
	}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, expected := range []string{
		"contributor", "project", "outcome", "from", "to", "alice",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("expected %q in audit log list partial, body=%q", expected, body)
		}
	}
}

// TestAdminAuditLogListPartialOutcomeDropdown verifies that the outcome dropdown
// contains the exported constant value. REQ-410 scenario 2, 2.5.3.
func TestAdminAuditLogListPartialOutcomeDropdown(t *testing.T) {
	store := parityStoreStub{}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, cloudstore.AuditOutcomeRejectedProjectPaused) {
		t.Errorf("expected outcome constant %q in dropdown, body=%q", cloudstore.AuditOutcomeRejectedProjectPaused, body)
	}
}

// TestAdminNavAuditLogLinkInAllFourPages verifies that all four admin page shells
// contain the "Audit Log" link after the adminNav refactor. REQ-411 all scenarios, 2.5.4.
func TestAdminNavAuditLogLinkInAllFourPages(t *testing.T) {
	store := parityStoreStub{}
	mux := newAuthedAdminMux(store)

	pages := []struct {
		url  string
		name string
	}{
		{"/dashboard/admin?auth=ok", "AdminPage"},
		{"/dashboard/admin/projects?auth=ok", "AdminProjectsPage"},
		{"/dashboard/admin/users?auth=ok", "AdminUsersPage"},
		{"/dashboard/admin/health?auth=ok", "AdminHealthPage"},
	}

	for _, page := range pages {
		t.Run(page.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, page.url, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d body=%q", page.name, rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, `href="/dashboard/admin/audit-log"`) {
				t.Errorf("expected Audit Log link in %s nav, body=%q", page.name, body)
			}
			if !strings.Contains(body, "Audit Log") {
				t.Errorf("expected 'Audit Log' text in %s nav, body=%q", page.name, body)
			}
		})
	}
}

// ─── Phase 2.6: Handler and route tests ──────────────────────────────────────

// TestAdminAuditLogShellRouteAdminAccess verifies admin can access audit log shell. REQ-408 scenario 1, 2.6.1.
func TestAdminAuditLogShellRouteAdminAccess(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin audit log, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminAuditLogShellRouteNonAdminDenied verifies non-admin gets 403. REQ-408 scenario 2, 2.6.2.
func TestAdminAuditLogShellRouteNonAdminDenied(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false) // isAdmin=false
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log?auth=ok", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin audit log, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminAuditLogListPartialAdminAccess verifies admin gets 200 with rows. REQ-409 scenario 1, 2.6.3.
func TestAdminAuditLogListPartialAdminAccess(t *testing.T) {
	store := parityStoreStub{
		auditRows: []cloudstore.DashboardAuditRow{
			{ID: 1, OccurredAt: "2026-04-24T00:00:00Z", Contributor: "alice",
				Project: "proj-a", Action: "mutation_push", Outcome: "rejected_project_paused"},
		},
	}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin audit list, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice") {
		t.Errorf("expected contributor 'alice' in audit list partial, body=%q", body)
	}
}

// TestAdminAuditLogListFilterByContributor verifies contributor filter narrows rows. REQ-409 scenario 2, 2.6.4.
func TestAdminAuditLogListFilterByContributor(t *testing.T) {
	store := parityStoreStub{
		auditRows: []cloudstore.DashboardAuditRow{
			{ID: 1, OccurredAt: "2026-04-24T00:00:00Z", Contributor: "alice", Project: "proj-a", Action: "mutation_push", Outcome: "rejected_project_paused"},
		},
	}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/list?auth=ok&contributor=alice", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice") {
		t.Errorf("expected alice in filtered audit list, body=%q", body)
	}
}

// TestAdminAuditLogListPartialNonAdminDenied verifies non-admin gets 403 on list. REQ-409 scenario 3, 2.6.5.
func TestAdminAuditLogListPartialNonAdminDenied(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/list?auth=ok", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin audit list, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── JW2: Deep-link filter propagation to initial hx-get ─────────────────────

// TestAdminAuditLogShellPropagatesFiltersToInitialHtmx verifies JW2: when the admin
// audit log shell is loaded with filter query params (e.g. contributor=alice), the
// initial hx-get URL must embed those same params so deep-linking preserves filters.
func TestAdminAuditLogShellPropagatesFiltersToInitialHtmx(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log?auth=ok&contributor=alice&outcome=rejected_project_paused", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The initial hx-get URL must include the contributor and outcome params.
	if !strings.Contains(body, "contributor=alice") {
		t.Errorf("expected contributor=alice forwarded in hx-get URL; body=%q", body)
	}
	if !strings.Contains(body, "outcome=rejected_project_paused") {
		t.Errorf("expected outcome=rejected_project_paused forwarded in hx-get URL; body=%q", body)
	}
}

// ─── JW6: parseAuditFilter date-only format + invalid format rejection ────────

// TestAdminAuditLogListParsesDateOnlyFilter verifies JW6 part A: a date-only
// value (YYYY-MM-DD) in the "from" param must be accepted and parsed correctly
// (falling back from RFC3339 parse failure). Handler must return 200.
func TestAdminAuditLogListParsesDateOnlyFilter(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/list?auth=ok&from=2026-04-24", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for date-only from param, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminAuditLogListRejectsInvalidTimeFormat verifies JW6 part B: a malformed
// time value in the "from" param must result in HTTP 400 with error code
// "invalid_time_format" rather than silently dropping the filter.
func TestAdminAuditLogListRejectsInvalidTimeFormat(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/list?auth=ok&from=not-a-date", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid from param, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_time_format") {
		t.Errorf("expected invalid_time_format in body, got %q", rec.Body.String())
	}
}

// ─── N7: partial-only contract for handleAdminAuditLogList ───────────────────

// TestAdminAuditLogListIsPartialOnlyNoLayoutWrapper verifies N7: the audit log list
// endpoint must render a fragment (no full <html> layout) even when the request lacks
// an HX-Request header. This is the partial-only contract (R6-2 equivalent).
func TestAdminAuditLogListIsPartialOnlyNoLayoutWrapper(t *testing.T) {
	mux := newAuthedAdminMux(parityStoreStub{})
	rec := httptest.NewRecorder()
	// Deliberately no HX-Request header — must still get fragment, not full layout.
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/list?auth=ok", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Partial-only: must NOT contain <html> tag (that would be a full Layout wrapper).
	if strings.Contains(body, "<html") {
		t.Errorf("handleAdminAuditLogList returned full Layout wrapper for non-HTMX request; got <html> in body")
	}
}
