package dashboard

//go:generate go tool templ generate

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/constants"
	"github.com/a-h/templ"
)

type SyncStatus struct {
	Phase                string
	ReasonCode           string
	ReasonMessage        string
	UpgradeStage         string
	UpgradeReasonCode    string
	UpgradeReasonMessage string
}

type SyncStatusProvider interface {
	Status() SyncStatus
}

type staticSyncStatusProvider struct {
	status SyncStatus
}

func (s staticSyncStatusProvider) Status() SyncStatus { return s.status }

type MountConfig struct {
	RequireSession      func(r *http.Request) error
	ValidateLoginToken  func(token string) error
	CreateSessionCookie func(w http.ResponseWriter, r *http.Request, token string) error
	ClearSessionCookie  func(w http.ResponseWriter, r *http.Request)
	IsAdmin             func(r *http.Request) bool
	GetDisplayName      func(r *http.Request) string
	Store               DashboardStore
	ManagedUsers        ManagedUsersStore
	MaxLoginBodyBytes   int64
	StatusProvider      SyncStatusProvider
}

type DashboardStore interface {
	// Existing methods (from cloud-dashboard-parity).
	ListProjects(query string) ([]cloudstore.DashboardProjectRow, error)
	ProjectDetail(project string) (cloudstore.DashboardProjectDetail, error)
	ListContributors(query string) ([]cloudstore.DashboardContributorRow, error)
	ListRecentSessions(project string, query string, limit int) ([]cloudstore.DashboardSessionRow, error)
	ListRecentObservations(project string, query string, limit int) ([]cloudstore.DashboardObservationRow, error)
	ListRecentPrompts(project string, query string, limit int) ([]cloudstore.DashboardPromptRow, error)
	AdminOverview() (cloudstore.DashboardAdminOverview, error)

	// Paginated list methods (from cloud-dashboard-visual-parity).
	ListProjectsPaginated(query string, limit, offset int) ([]cloudstore.DashboardProjectRow, int, error)
	ListRecentObservationsPaginated(project, query, obsType string, limit, offset int) ([]cloudstore.DashboardObservationRow, int, error)
	ListRecentSessionsPaginated(project, query string, limit, offset int) ([]cloudstore.DashboardSessionRow, int, error)
	ListRecentPromptsPaginated(project, query string, limit, offset int) ([]cloudstore.DashboardPromptRow, int, error)
	ListContributorsPaginated(query string, limit, offset int) ([]cloudstore.DashboardContributorRow, int, error)

	// Detail methods.
	GetSessionDetail(project, sessionID string) (cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, []cloudstore.DashboardPromptRow, error)
	GetObservationDetail(project, sessionID, syncID string) (cloudstore.DashboardObservationRow, cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, error)
	GetPromptDetail(project, sessionID, syncID string) (cloudstore.DashboardPromptRow, cloudstore.DashboardSessionRow, []cloudstore.DashboardPromptRow, error)

	// SystemHealth.
	SystemHealth() (cloudstore.DashboardSystemHealth, error)

	// Sync control methods.
	ListProjectSyncControls() ([]cloudstore.ProjectSyncControl, error)
	GetProjectSyncControl(project string) (*cloudstore.ProjectSyncControl, error)
	SetProjectSyncEnabled(project string, enabled bool, updatedBy, reason string) error
	IsProjectSyncEnabled(project string) (bool, error)

	// Batch 6: Connected navigation methods.
	GetContributorDetail(name string) (cloudstore.DashboardContributorRow, []cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, []cloudstore.DashboardPromptRow, error)
	ListDistinctTypes() ([]string, error)

	// Audit log (REQ-409).
	ListAuditEntriesPaginated(ctx context.Context, filter cloudstore.AuditFilter, limit, offset int) ([]cloudstore.DashboardAuditRow, int, error)
}

// ManagedUsersStore is the read-only boundary the dashboard package uses to
// render the Managed Users surface. It is intentionally separate from
// DashboardStore (contributor/project/session analytics) so managed human
// users stay distinct from contributor analytics (cloud-user-token-management
// PR4). Mutations (create/enable/disable/tokens/grants) are owned and
// policy-gated by internal/cloud/cloudserver, which already enforces managed
// admin authorization and audit for these operations; the dashboard package
// only renders outcomes and links to those cloudserver-owned routes.
type ManagedUsersStore interface {
	ListHumanUsers(ctx context.Context) ([]cloudstore.HumanUser, error)
}

type handlers struct {
	cfg MountConfig
}

func Mount(mux *http.ServeMux, cfg MountConfig) {
	h := &handlers{cfg: cfg}

	staticSub, err := fs.Sub(StaticFS, "static")
	if err != nil {
		log.Fatalf("dashboard: failed to create static sub FS: %v", err)
	}
	mux.Handle("GET /dashboard/static/", http.StripPrefix("/dashboard/static/", http.FileServer(http.FS(staticSub))))

	mux.HandleFunc("GET /dashboard/health", h.handleHealth)
	mux.HandleFunc("GET /dashboard/login", h.handleLoginPage)
	mux.HandleFunc("POST /dashboard/login", h.handleLoginSubmit)
	mux.HandleFunc("POST /dashboard/logout", h.handleLogout)

	mux.HandleFunc("GET /dashboard", h.requireSession(h.handleDashboardHome))
	mux.HandleFunc("GET /dashboard/", h.requireSession(h.handleDashboardHome))
	mux.HandleFunc("GET /dashboard/stats", h.requireSession(h.handleDashboardStats))
	mux.HandleFunc("GET /dashboard/activity", h.requireSession(h.handleDashboardActivity))
	mux.HandleFunc("GET /dashboard/browser", h.requireSession(h.handleBrowser))
	mux.HandleFunc("GET /dashboard/browser/observations", h.requireSession(h.handleBrowserObservations))
	mux.HandleFunc("GET /dashboard/browser/sessions", h.requireSession(h.handleBrowserSessions))
	mux.HandleFunc("GET /dashboard/browser/sessions/{sessionID}", h.requireSession(h.handleBrowserSessionDetail))
	mux.HandleFunc("GET /dashboard/browser/prompts", h.requireSession(h.handleBrowserPrompts))
	mux.HandleFunc("GET /dashboard/projects", h.requireSession(h.handleProjects))
	mux.HandleFunc("GET /dashboard/projects/{project}", h.requireSession(h.handleProjectDetail))
	mux.HandleFunc("GET /dashboard/contributors", h.requireSession(h.handleContributors))
	mux.HandleFunc("GET /dashboard/contributors/list", h.requireSession(h.handleContributorsList))
	mux.HandleFunc("GET /dashboard/contributors/{contributor}", h.requireSession(h.handleContributorDetail))
	mux.HandleFunc("GET /dashboard/admin", h.requireSession(h.handleAdmin))
	mux.HandleFunc("GET /dashboard/admin/projects", h.requireSession(h.handleAdminProjectControls))
	// R4-10: /dashboard/admin/contributors was a dead route (duplicate of /dashboard/contributors
	// behind an extra admin gate). Removed to avoid confusion.

	// 11 new routes — visual parity + composite-ID detail pages (REQ-106, Design Decision 3).
	mux.HandleFunc("GET /dashboard/projects/list", h.requireSession(h.handleProjectsList))
	mux.HandleFunc("GET /dashboard/projects/{name}/observations", h.requireSession(h.handleProjectObservationsPartial))
	mux.HandleFunc("GET /dashboard/projects/{name}/sessions", h.requireSession(h.handleProjectSessionsPartial))
	mux.HandleFunc("GET /dashboard/projects/{name}/prompts", h.requireSession(h.handleProjectPromptsPartial))
	mux.HandleFunc("GET /dashboard/admin/users", h.requireSession(h.handleAdminUsers))
	mux.HandleFunc("GET /dashboard/admin/users/list", h.requireSession(h.handleAdminUsersList))
	mux.HandleFunc("GET /dashboard/admin/health", h.requireSession(h.handleAdminHealth))
	mux.HandleFunc("POST /dashboard/admin/projects/{name}/sync", h.requireSession(h.handleAdminSyncTogglePost))
	mux.HandleFunc("GET /dashboard/admin/projects/{name}/sync/form", h.requireSession(h.handleAdminSyncToggleForm))
	mux.HandleFunc("GET /dashboard/sessions/{project}/{sessionID}", h.requireSession(h.handleSessionDetail))
	mux.HandleFunc("GET /dashboard/observations/{project}/{sessionID}/{syncID}", h.requireSession(h.handleObservationDetail))
	mux.HandleFunc("GET /dashboard/prompts/{project}/{sessionID}/{syncID}", h.requireSession(h.handlePromptDetail))

	// Audit log routes — admin-gated (REQ-408, REQ-409).
	mux.HandleFunc("GET /dashboard/admin/audit-log", h.requireSession(h.handleAdminAuditLog))
	mux.HandleFunc("GET /dashboard/admin/audit-log/list", h.requireSession(h.handleAdminAuditLogList))
}

func Handler() http.Handler {
	return HandlerWithStatus(staticSyncStatusProvider{status: SyncStatus{Phase: "idle"}})
}

func HandlerWithStatus(provider SyncStatusProvider) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		status := provider.Status()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(renderSyncStatusPage(status)))
	})
	return mux
}

func renderSyncStatusPage(status SyncStatus) string {
	code := status.ReasonCode
	message := status.ReasonMessage
	headline := reasonHeadline(status.ReasonCode)
	phase := status.Phase
	phase = html.EscapeString(phase)
	headline = html.EscapeString(headline)
	code = html.EscapeString(code)
	message = html.EscapeString(message)

	return fmt.Sprintf(`<html>
<head><title>Engram Cloud Dashboard</title></head>
<body>
  <main>
    <h1>Engram Cloud Dashboard</h1>
    <p>phase: %s</p>
    <section>
      <h2>%s</h2>
      <p>reason_code: %s</p>
      <p>reason_message: %s</p>
      <p>upgrade_stage: %s</p>
      <p>upgrade_reason_code: %s</p>
      <p>upgrade_reason_message: %s</p>
    </section>
  </main>
</body>
</html>`, phase, headline, code, message, html.EscapeString(status.UpgradeStage), html.EscapeString(status.UpgradeReasonCode), html.EscapeString(status.UpgradeReasonMessage))
}

func reasonHeadline(code string) string {
	switch code {
	case constants.ReasonBlockedUnenrolled:
		return "Blocked — project unenrolled"
	case constants.ReasonPaused:
		return "Paused"
	case constants.ReasonAuthRequired:
		return "Authentication required"
	case constants.ReasonTransportFailed:
		return "Transport failure"
	default:
		if code == "" {
			return "Healthy"
		}
		return "Sync issue"
	}
}

func (h *handlers) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","subsystem":"dashboard"}`))
}

// renderComponent renders a templ component to the HTTP response.
func renderComponent(w http.ResponseWriter, r *http.Request, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := component.Render(r.Context(), w); err != nil {
		log.Printf("dashboard: templ render error: %v", err)
	}
}

// renderComponentStatus renders a templ component with a specific HTTP status code.
func renderComponentStatus(w http.ResponseWriter, r *http.Request, status int, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := component.Render(r.Context(), w); err != nil {
		log.Printf("dashboard: templ render error: %v", err)
	}
}

func (h *handlers) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	next := sanitizeDashboardNext(r.URL.Query().Get("next"))
	if h.cfg.RequireSession != nil {
		if err := h.cfg.RequireSession(r); err == nil {
			http.Redirect(w, r, dashboardPostLoginPath(next), http.StatusSeeOther)
			return
		}
	}
	renderComponent(w, r, LoginPage("", next))
}

func (h *handlers) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if h.cfg.MaxLoginBodyBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxLoginBodyBytes)
	}
	if err := r.ParseForm(); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, fmt.Sprintf("login payload too large (max %d bytes)", h.cfg.MaxLoginBodyBytes), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid form payload", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(r.PostForm.Get("token"))
	next := sanitizeDashboardNext(r.PostForm.Get("next"))
	if next == "" {
		next = sanitizeDashboardNext(r.URL.Query().Get("next"))
	}
	if h.cfg.RequireSession != nil {
		if err := h.cfg.RequireSession(r); err == nil {
			http.Redirect(w, r, dashboardPostLoginPath(next), http.StatusSeeOther)
			return
		}
	}
	if token == "" {
		renderComponent(w, r, LoginPage("token is required", next))
		return
	}
	if h.cfg.ValidateLoginToken != nil {
		if err := h.cfg.ValidateLoginToken(token); err != nil {
			renderComponent(w, r, LoginPage("invalid token", next))
			return
		}
	}
	if h.cfg.CreateSessionCookie != nil {
		if err := h.cfg.CreateSessionCookie(w, r, token); err != nil {
			http.Error(w, "unable to create dashboard session", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, dashboardPostLoginPath(next), http.StatusSeeOther)
}

func (h *handlers) handleLogout(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ClearSessionCookie != nil {
		h.cfg.ClearSessionCookie(w, r)
	}
	http.Redirect(w, r, "/dashboard/login", http.StatusSeeOther)
}

func (h *handlers) handleDashboardHome(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if isHTMXRequest(r) {
		renderComponent(w, r, DashboardHome(p.DisplayName()))
		return
	}
	renderComponent(w, r, Layout("Dashboard", p.DisplayName(), "dashboard", p.IsAdmin(), DashboardHome(p.DisplayName())))
}

func (h *handlers) handleDashboardStats(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	overview := cloudstore.DashboardAdminOverview{}
	if h.cfg.Store != nil {
		loaded, err := h.cfg.Store.AdminOverview()
		if err != nil {
			h.renderStoreError(w, r, "dashboard", "Stats", err)
			return
		}
		overview = loaded
	}
	// R5-1: Build the stats body as raw HTML then wrap in templ Layout so the
	// status-ribbon, shell-footer, and "CLOUD ACTIVE" pill are always present on
	// full-page navigation (non-HTMX). Previously renderPageOrHTMX called the
	// string-based renderLayout which lacked those elements.
	body := fmt.Sprintf(`<section class="frame-section"><p class="section-kicker">STATS</p><h2>Cloud Stats</h2><div class="metric-strip"><a href="/dashboard/projects" class="metric-card stat-card-link"><span class="metric-value">%d</span><span class="metric-label">Projects</span></a><a href="/dashboard/contributors" class="metric-card stat-card-link"><span class="metric-value">%d</span><span class="metric-label">Contributors</span></a><a href="/dashboard/browser" class="metric-card stat-card-link"><span class="metric-value">%d</span><span class="metric-label">Chunks</span></a></div></section>`, overview.Projects, overview.Contributors, overview.Chunks)
	if isHTMXRequest(r) {
		renderHTML(w, body)
		return
	}
	renderComponent(w, r, Layout("Stats", p.DisplayName(), "dashboard", p.IsAdmin(), templ.Raw(body)))
}

func (h *handlers) handleDashboardActivity(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	rows := make([]cloudstore.DashboardObservationRow, 0)
	if h.cfg.Store != nil {
		loaded, err := h.cfg.Store.ListRecentObservations(project, query, 25)
		if err != nil {
			h.renderStoreError(w, r, "dashboard", "Activity", err)
			return
		}
		rows = loaded
	}
	b := strings.Builder{}
	b.WriteString(`<section class="frame-section"><p class="section-kicker">ACTIVITY</p><h2>Recent Observation Activity</h2>`)
	if len(rows) == 0 {
		b.WriteString(`<div class="empty-state"><h3>No Activity</h3><p>No recent observations are available.</p></div>`)
	} else {
		b.WriteString(`<table class="data-table"><thead><tr><th>Project</th><th>Type</th><th>Title</th><th>Session</th><th>Created</th></tr></thead><tbody>`)
		for _, row := range rows {
			b.WriteString(fmt.Sprintf(`<tr><td>%s</td><td>%s</td><td>%s</td><td><a href="%s">%s</a></td><td>%s</td></tr>`, html.EscapeString(row.Project), html.EscapeString(row.Type), html.EscapeString(row.Title), safeQuery("/dashboard/browser/sessions/"+url.PathEscape(row.SessionID), preserveQuery(r.URL.RawQuery, "project", row.Project)), html.EscapeString(row.SessionID), html.EscapeString(row.CreatedAt)))
		}
		b.WriteString(`</tbody></table>`)
	}
	b.WriteString(`</section>`)
	// R5-1: Use templ Layout for non-HTMX so status-ribbon and shell-footer are present.
	if isHTMXRequest(r) {
		renderHTML(w, b.String())
		return
	}
	renderComponent(w, r, Layout("Activity", p.DisplayName(), "dashboard", p.IsAdmin(), templ.Raw(b.String())))
}

func (h *handlers) handleBrowser(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	obsType := strings.TrimSpace(r.URL.Query().Get("type"))
	var projectNames []string
	var obsTypes []string
	if h.cfg.Store != nil {
		if projs, err := h.cfg.Store.ListProjects(""); err == nil {
			for _, pr := range projs {
				projectNames = append(projectNames, pr.Project)
			}
		}
		// Batch 6: source type pills from store (degrade gracefully on error).
		if types, err := h.cfg.Store.ListDistinctTypes(); err == nil {
			obsTypes = types
		}
	}
	component := BrowserPage(projectNames, obsTypes, project, query, obsType)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Browser", p.DisplayName(), "browser", p.IsAdmin(), component))
}

func (h *handlers) handleBrowserObservations(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	obsType := strings.TrimSpace(r.URL.Query().Get("type"))
	// R2-1: parse page/pageSize without pre-clamping (total not known yet).
	reqPage, pageSize := parsePaginationRaw(r)
	rows := make([]cloudstore.DashboardObservationRow, 0)
	total := 0
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListRecentObservationsPaginated(project, query, obsType, pageSize, (reqPage-1)*pageSize)
		if err != nil {
			h.renderStoreError(w, r, "browser", "Observations", err)
			return
		}
	}
	// R2-1: re-clamp page to real totalPages; re-fetch if the requested page was beyond the end.
	// R3-7: on re-fetch error, log and keep previous rows rather than returning an error page.
	// R4-9: when re-fetch fails and rows are empty, attempt one additional fetch at page 1.
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListRecentObservationsPaginated(project, query, obsType, pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch observations page %d: %v (using first-page rows)", pg.Page, err)
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListRecentObservationsPaginated(project, query, obsType, pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback observations page 1: %v", fallbackErr)
				}
			}
		}
	}
	partial := ObservationsPartial(rows, pg)
	if isHTMXRequest(r) {
		renderComponent(w, r, partial)
		return
	}
	renderComponent(w, r, Layout("Browser", p.DisplayName(), "browser", p.IsAdmin(), BrowserPage(nil, nil, project, query, obsType)))
}

func (h *handlers) handleBrowserSessions(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	// R2-1: parse page/pageSize without pre-clamping.
	reqPage, pageSize := parsePaginationRaw(r)
	rows := make([]cloudstore.DashboardSessionRow, 0)
	total := 0
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListRecentSessionsPaginated(project, query, pageSize, (reqPage-1)*pageSize)
		if err != nil {
			h.renderStoreError(w, r, "browser", "Sessions", err)
			return
		}
	}
	// R2-1: re-clamp and re-fetch if needed.
	// R3-7: on re-fetch error, log and keep previous rows (graceful degradation).
	// R4-9: when re-fetch fails and rows are empty, attempt one additional fetch at page 1.
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListRecentSessionsPaginated(project, query, pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch sessions page %d: %v (using first-page rows)", pg.Page, err)
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListRecentSessionsPaginated(project, query, pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback sessions page 1: %v", fallbackErr)
				}
			}
		}
	}
	partial := SessionsPartial(rows, pg)
	if isHTMXRequest(r) {
		renderComponent(w, r, partial)
		return
	}
	renderComponent(w, r, Layout("Browser", p.DisplayName(), "browser", p.IsAdmin(), BrowserPage(nil, nil, project, query, "")))
}

func (h *handlers) handleBrowserPrompts(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	// R2-1: parse page/pageSize without pre-clamping.
	reqPage, pageSize := parsePaginationRaw(r)
	rows := make([]cloudstore.DashboardPromptRow, 0)
	total := 0
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListRecentPromptsPaginated(project, query, pageSize, (reqPage-1)*pageSize)
		if err != nil {
			h.renderStoreError(w, r, "browser", "Prompts", err)
			return
		}
	}
	// R2-1: re-clamp and re-fetch if needed.
	// R3-7: on re-fetch error, log and keep previous rows (graceful degradation).
	// R4-9: when re-fetch fails and rows are empty, attempt one additional fetch at page 1.
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListRecentPromptsPaginated(project, query, pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch prompts page %d: %v (using first-page rows)", pg.Page, err)
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListRecentPromptsPaginated(project, query, pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback prompts page 1: %v", fallbackErr)
				}
			}
		}
	}
	partial := PromptsPartial(rows, pg)
	if isHTMXRequest(r) {
		renderComponent(w, r, partial)
		return
	}
	renderComponent(w, r, Layout("Browser", p.DisplayName(), "browser", p.IsAdmin(), BrowserPage(nil, nil, project, query, "")))
}

// handleBrowserSessionDetail handles GET /dashboard/browser/sessions/{sessionID}.
// R4-5: migrated to use principalFromRequest and renderComponentStatus for empty state.
// R5-6: use r.Clone to avoid mutating shared request state when delegating to handleBrowserSessions.
func (h *handlers) handleBrowserSessionDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if sessionID == "" {
		renderComponentStatus(w, r, http.StatusNotFound, Layout("Session Detail", p.DisplayName(), "browser", p.IsAdmin(), EmptyState("Session Not Found", "No dashboard data exists for that session identifier.")))
		return
	}
	// Clone the request before mutating URL so the original request is not modified.
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = preserveQuery(r.URL.RawQuery, "q", sessionID)
	h.handleBrowserSessions(w, r2)
}

func (h *handlers) handleProjects(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	component := ProjectsPage(query)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Projects", p.DisplayName(), "projects", p.IsAdmin(), component))
}

func (h *handlers) handleProjectDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.PathValue("project"))
	if project == "" {
		renderComponentStatus(w, r, http.StatusNotFound, Layout("Project Detail", p.DisplayName(), "projects", p.IsAdmin(), EmptyState("Project Not Found", "No replicated dashboard data exists for that project.")))
		return
	}
	var stats *cloudstore.DashboardProjectRow
	var ctrl *cloudstore.ProjectSyncControl
	if h.cfg.Store != nil {
		detail, err := h.cfg.Store.ProjectDetail(project)
		if err != nil {
			h.renderStoreError(w, r, "projects", "Project detail", err)
			return
		}
		statsRow := detail.Stats
		stats = &statsRow
		// Degrade gracefully: if sync control lookup fails, render without pause audit.
		if c, err := h.cfg.Store.GetProjectSyncControl(project); err == nil {
			ctrl = c
		}
	}
	component := ProjectDetailPage(project, stats, ctrl)
	renderComponent(w, r, Layout("Project Detail", p.DisplayName(), "projects", p.IsAdmin(), component))
}

func (h *handlers) handleContributors(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	// R6-1: serve only the shell; the list is loaded via HTMX from /dashboard/contributors/list.
	// This mirrors the ProjectsPage pattern — no store call at the shell level.
	component := ContributorsPage(query)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Contributors", p.DisplayName(), "contributors", p.IsAdmin(), component))
}

// handleContributorsList handles GET /dashboard/contributors/list.
// R5-2: always returns ContributorsListPartial (no full page wrapper) so HTMX
// pagination targets can swap just the content div.
// R6-2: on store error, always renders a fragment (no Layout wrapper) — partial-only contract.
func (h *handlers) handleContributorsList(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	reqPage, pageSize := parsePaginationRaw(r)
	rows := make([]cloudstore.DashboardContributorRow, 0)
	total := 0
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListContributorsPaginated(query, pageSize, (reqPage-1)*pageSize)
		if err != nil {
			log.Printf("dashboard: contributors list store error: %v", err)
			renderComponentStatus(w, r, http.StatusBadGateway, EmptyState("Service Unavailable", "Dashboard data is temporarily unavailable."))
			return
		}
	}
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListContributorsPaginated(query, pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch contributors list page %d: %v", pg.Page, err)
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListContributorsPaginated(query, pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback contributors list page 1: %v", fallbackErr)
				}
			}
		}
	}
	renderComponent(w, r, ContributorsListPartial(rows, pg))
}

func (h *handlers) handleContributorDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	contributor := strings.TrimSpace(r.PathValue("contributor"))
	if contributor == "" {
		renderComponentStatus(w, r, http.StatusNotFound, Layout("Contributor Detail", p.DisplayName(), "contributors", p.IsAdmin(), EmptyState("Contributor Not Found", "No dashboard data exists for that contributor.")))
		return
	}
	if h.cfg.Store == nil {
		renderComponent(w, r, Layout("Contributor Detail", p.DisplayName(), "contributors", p.IsAdmin(), ContributorDetailPage(nil, nil, nil, nil)))
		return
	}
	row, sessions, observations, prompts, err := h.cfg.Store.GetContributorDetail(contributor)
	if err != nil {
		h.renderStoreError(w, r, "contributors", "Contributor detail", err)
		return
	}
	component := ContributorDetailPage(&row, sessions, observations, prompts)
	renderComponent(w, r, Layout("Contributor Detail", p.DisplayName(), "contributors", p.IsAdmin(), component))
}

func (h *handlers) handleAdmin(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var health *cloudstore.DashboardSystemHealth
	var controls []cloudstore.ProjectSyncControl
	if h.cfg.Store != nil {
		if sh, err := h.cfg.Store.SystemHealth(); err == nil {
			health = &sh
		}
		if ctrls, err := h.cfg.Store.ListProjectSyncControls(); err == nil {
			controls = ctrls
		}
	}
	component := AdminPage(health, controls)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Admin", p.DisplayName(), "admin", p.IsAdmin(), component))
}

// handleAdminProjectControls handles GET /dashboard/admin/projects.
// Batch 6: renders AdminProjectsPage templ with sync controls, replacing the
// previous delegation to handleProjects which had no toggle UI.
func (h *handlers) handleAdminProjectControls(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var controls []cloudstore.ProjectSyncControl
	if h.cfg.Store != nil {
		// Degrade gracefully: empty controls if store fails.
		if ctrls, err := h.cfg.Store.ListProjectSyncControls(); err == nil {
			controls = ctrls
		}
	}
	component := AdminProjectsPage(controls)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Admin Projects", p.DisplayName(), "admin", p.IsAdmin(), component))
}

// ─── 11 New Handler Implementations (visual parity batch) ────────────────────

// handleProjectsList handles GET /dashboard/projects/list (HTMX partial).
// Batch 6: passes sync controls map so Paused badge renders correctly.
// R4-3: uses parsePaginationRaw + reclampPagination so >50 projects are reachable.
func (h *handlers) handleProjectsList(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	// R4-3: parse raw page/pageSize (no pre-clamp) before first store call.
	reqPage, pageSize := parsePaginationRaw(r)
	rows := make([]cloudstore.DashboardProjectRow, 0)
	total := 0
	var controlsMap map[string]cloudstore.ProjectSyncControl
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListProjectsPaginated(query, pageSize, (reqPage-1)*pageSize)
		if err != nil {
			// R6-2: partial-only endpoint — always render fragment, never full Layout (even non-HTMX).
			log.Printf("dashboard: projects list store error: %v", err)
			renderComponentStatus(w, r, http.StatusBadGateway, EmptyState("Service Unavailable", "Dashboard data is temporarily unavailable."))
			return
		}
		// Degrade gracefully: if controls fail, render without badges.
		if ctrls, err := h.cfg.Store.ListProjectSyncControls(); err == nil {
			controlsMap = controlsByProject(ctrls)
		}
	}
	// R4-3: re-clamp to real total; re-fetch if requested page was beyond last page.
	// R5-3: add tier-3 fallback — if clamped re-fetch fails AND rows are empty, attempt page 1.
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListProjectsPaginated(query, pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch projects list page %d: %v (using first-page rows)", pg.Page, err)
			// R5-3: tier-3 fallback to page 1 when re-fetch fails and rows are empty.
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListProjectsPaginated(query, pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback projects list page 1: %v", fallbackErr)
				}
			}
		}
	}
	renderComponent(w, r, ProjectsListPartial(rows, controlsMap, pg))
}

// handleProjectObservationsPartial handles GET /dashboard/projects/{name}/observations.
func (h *handlers) handleProjectObservationsPartial(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	obsType := strings.TrimSpace(r.URL.Query().Get("type"))
	rows := make([]cloudstore.DashboardObservationRow, 0)
	if h.cfg.Store != nil {
		var err error
		rows, _, err = h.cfg.Store.ListRecentObservationsPaginated(name, query, obsType, 50, 0)
		if err != nil {
			h.renderStoreError(w, r, "projects", "Project observations", err)
			return
		}
	}
	renderComponent(w, r, ObservationsPartial(rows, Pagination{}))
}

// handleProjectSessionsPartial handles GET /dashboard/projects/{name}/sessions.
func (h *handlers) handleProjectSessionsPartial(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	rows := make([]cloudstore.DashboardSessionRow, 0)
	if h.cfg.Store != nil {
		var err error
		rows, _, err = h.cfg.Store.ListRecentSessionsPaginated(name, query, 50, 0)
		if err != nil {
			h.renderStoreError(w, r, "projects", "Project sessions", err)
			return
		}
	}
	renderComponent(w, r, SessionsPartial(rows, Pagination{}))
}

// handleProjectPromptsPartial handles GET /dashboard/projects/{name}/prompts.
func (h *handlers) handleProjectPromptsPartial(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	rows := make([]cloudstore.DashboardPromptRow, 0)
	if h.cfg.Store != nil {
		var err error
		rows, _, err = h.cfg.Store.ListRecentPromptsPaginated(name, query, 50, 0)
		if err != nil {
			h.renderStoreError(w, r, "projects", "Project prompts", err)
			return
		}
	}
	renderComponent(w, r, PromptsPartial(rows, Pagination{}))
}

// handleAdminUsers handles GET /dashboard/admin/users.
// R6-1: serves only the shell; the list is loaded via HTMX from /dashboard/admin/users/list.
// cloud-user-token-management PR4: this surface renders MANAGED USERS (cloud
// principals/human accounts), not contributor analytics. Contributor
// analytics keep their own separate surface at /dashboard/contributors.
func (h *handlers) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	component := ManagedUsersPage()
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Managed Users", p.DisplayName(), "admin", p.IsAdmin(), component))
}

// handleAdminUsersList handles GET /dashboard/admin/users/list.
// R5-2: always returns ManagedUsersListPartial (partial only, no full shell wrapper).
// R6-2: on store error, always renders a fragment (no Layout wrapper) — partial-only contract.
// Admin-gated. cloud-user-token-management PR4: renders managed human users,
// not contributor analytics (see handleAdminUsers).
func (h *handlers) handleAdminUsersList(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rows := make([]cloudstore.HumanUser, 0)
	if h.cfg.ManagedUsers != nil {
		var err error
		rows, err = h.cfg.ManagedUsers.ListHumanUsers(r.Context())
		if err != nil {
			log.Printf("dashboard: managed users list store error: %v", err)
			renderComponentStatus(w, r, http.StatusBadGateway, EmptyState("Service Unavailable", "Managed user data is temporarily unavailable."))
			return
		}
	}
	renderComponent(w, r, ManagedUsersListPartial(rows))
}

// handleAdminHealth handles GET /dashboard/admin/health.
func (h *handlers) handleAdminHealth(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var health *cloudstore.DashboardSystemHealth
	if h.cfg.Store != nil {
		if sh, err := h.cfg.Store.SystemHealth(); err == nil {
			health = &sh
		}
	}
	component := AdminHealthPage(health)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Admin Health", p.DisplayName(), "admin", p.IsAdmin(), component))
}

// handleAdminSyncTogglePost handles POST /dashboard/admin/projects/{name}/sync.
// Admin-gated. Sets sync enabled/disabled for the project. Satisfies REQ-112, AD-6.
func (h *handlers) handleAdminSyncTogglePost(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	enabledRaw := strings.TrimSpace(r.FormValue("enabled"))
	if enabledRaw != "true" && enabledRaw != "false" {
		http.Error(w, "invalid value for enabled: must be 'true' or 'false'", http.StatusBadRequest)
		return
	}
	enabled := enabledRaw == "true"
	reason := strings.TrimSpace(r.FormValue("reason"))
	if h.cfg.Store != nil {
		if err := h.cfg.Store.SetProjectSyncEnabled(name, enabled, p.DisplayName(), reason); err != nil {
			http.Error(w, "store error", http.StatusInternalServerError)
			return
		}
	}
	redirectURL := "/dashboard/admin/projects"
	// R2-3: For HTMX requests, return 200 + HX-Redirect only.
	// http.Redirect writes a 303 regardless; HTMX intercepts 303 and follows natively,
	// making HX-Redirect irrelevant. For plain browser forms, keep the 303.
	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

// handleAdminSyncToggleForm handles GET /dashboard/admin/projects/{name}/sync/form.
func (h *handlers) handleAdminSyncToggleForm(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	ctrl := cloudstore.ProjectSyncControl{Project: name, SyncEnabled: true}
	if h.cfg.Store != nil {
		if c, err := h.cfg.Store.GetProjectSyncControl(name); err == nil && c != nil {
			ctrl = *c
		}
	}
	renderComponent(w, r, AdminSyncToggleFormPartial(ctrl))
}

// handleSessionDetail handles GET /dashboard/sessions/{project}/{sessionID}.
// Satisfies REQ-106, Design Decision 3, Design Decision 5.
func (h *handlers) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.PathValue("project"))
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if project == "" || sessionID == "" || len(sessionID) > 128 {
		renderComponentStatus(w, r, http.StatusNotFound, Layout("Session", p.DisplayName(), "browser", p.IsAdmin(), EmptyState("Session Not Found", "Invalid session identifier.")))
		return
	}
	var sess *cloudstore.DashboardSessionRow
	var obs []cloudstore.DashboardObservationRow
	var prompts []cloudstore.DashboardPromptRow
	if h.cfg.Store != nil {
		s, o, pr, err := h.cfg.Store.GetSessionDetail(project, sessionID)
		if err != nil {
			h.renderStoreError(w, r, "browser", "Session detail", err)
			return
		}
		sess = &s
		obs = o
		prompts = pr
	}
	component := SessionDetailPage(sess, obs, prompts)
	renderComponent(w, r, Layout("Session Detail", p.DisplayName(), "browser", p.IsAdmin(), component))
}

// handleObservationDetail handles GET /dashboard/observations/{project}/{sessionID}/{syncID}.
func (h *handlers) handleObservationDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.PathValue("project"))
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	syncID := strings.TrimSpace(r.PathValue("syncID"))
	if project == "" || sessionID == "" || syncID == "" || len(syncID) > 128 {
		renderComponentStatus(w, r, http.StatusNotFound, Layout("Observation", p.DisplayName(), "browser", p.IsAdmin(), EmptyState("Observation Not Found", "Invalid observation identifier.")))
		return
	}
	var obs *cloudstore.DashboardObservationRow
	var sess *cloudstore.DashboardSessionRow
	var related []cloudstore.DashboardObservationRow
	if h.cfg.Store != nil {
		o, s, rel, err := h.cfg.Store.GetObservationDetail(project, sessionID, syncID)
		if err != nil {
			h.renderStoreError(w, r, "browser", "Observation detail", err)
			return
		}
		obs = &o
		sess = &s
		related = rel
	}
	component := ObservationDetailPage(obs, sess, related)
	renderComponent(w, r, Layout("Observation Detail", p.DisplayName(), "browser", p.IsAdmin(), component))
}

// handlePromptDetail handles GET /dashboard/prompts/{project}/{sessionID}/{syncID}.
func (h *handlers) handlePromptDetail(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	project := strings.TrimSpace(r.PathValue("project"))
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	syncID := strings.TrimSpace(r.PathValue("syncID"))
	if project == "" || sessionID == "" || syncID == "" || len(syncID) > 128 {
		renderComponentStatus(w, r, http.StatusNotFound, Layout("Prompt", p.DisplayName(), "browser", p.IsAdmin(), EmptyState("Prompt Not Found", "Invalid prompt identifier.")))
		return
	}
	var prompt *cloudstore.DashboardPromptRow
	var sess *cloudstore.DashboardSessionRow
	var related []cloudstore.DashboardPromptRow
	if h.cfg.Store != nil {
		pr, s, rel, err := h.cfg.Store.GetPromptDetail(project, sessionID, syncID)
		if err != nil {
			h.renderStoreError(w, r, "browser", "Prompt detail", err)
			return
		}
		prompt = &pr
		sess = &s
		related = rel
	}
	component := PromptDetailPage(prompt, sess, related)
	renderComponent(w, r, Layout("Prompt Detail", p.DisplayName(), "browser", p.IsAdmin(), component))
}

// renderObservationsTable removed in Batch 6 REFACTOR — replaced by ObservationsPartial templ component.

func (h *handlers) renderStoreError(w http.ResponseWriter, r *http.Request, activeTab string, contextLabel string, err error) {
	status, headline, message := classifyStoreError(contextLabel, err)
	log.Printf("dashboard: %s store error: %v", strings.ToLower(strings.TrimSpace(contextLabel)), err)
	fragment := fmt.Sprintf(`<div class="empty-state" role="alert"><h3>%s</h3><p>%s</p></div>`, html.EscapeString(headline), html.EscapeString(message))
	if isHTMXRequest(r) {
		renderHTMLStatus(w, status, fragment)
		return
	}
	// R5-1: Use templ Layout for non-HTMX error pages so status-ribbon and shell-footer
	// are always present regardless of which handler generated the error.
	p := h.principalFromRequest(r)
	body := fmt.Sprintf(`<section class="frame-section"><p class="section-kicker">DEGRADED</p><h2>%s</h2>%s</section>`, html.EscapeString(contextLabel), fragment)
	renderComponentStatus(w, r, status, Layout(contextLabel, p.DisplayName(), activeTab, p.IsAdmin(), templ.Raw(body)))
}

func classifyStoreError(contextLabel string, err error) (int, string, string) {
	switch {
	case errors.Is(err, cloudstore.ErrDashboardProjectInvalid):
		return http.StatusNotFound, "Project not found", "No replicated dashboard data exists for that project."
	case errors.Is(err, cloudstore.ErrDashboardProjectForbidden):
		return http.StatusForbidden, "Project access denied", "You are not allowed to access that project scope."
	case errors.Is(err, cloudstore.ErrDashboardProjectNotFound):
		return http.StatusNotFound, "Project not found", "No replicated dashboard data exists for that project."
	// R4-7: Contributor not found must produce a contributor-specific message, not "Project not found".
	case errors.Is(err, cloudstore.ErrDashboardContributorNotFound):
		return http.StatusNotFound, "Contributor not found", "No contributor with that name has been seen in this cloud workspace."
	// R5-4: Session/observation/prompt not found — entity-specific error pages.
	case errors.Is(err, cloudstore.ErrDashboardSessionNotFound):
		return http.StatusNotFound, "Session not found", "No session with that identifier exists in the specified project."
	case errors.Is(err, cloudstore.ErrDashboardObservationNotFound):
		return http.StatusNotFound, "Observation not found", "No observation with that identifier exists in the specified session."
	case errors.Is(err, cloudstore.ErrDashboardPromptNotFound):
		return http.StatusNotFound, "Prompt not found", "No prompt with that identifier exists in the specified session."
	default:
		heading := strings.TrimSpace(contextLabel)
		if heading == "" {
			heading = "Dashboard data"
		}
		return http.StatusServiceUnavailable, heading + " unavailable", "Cloud dashboard data is temporarily unavailable."
	}
}

func isHTMXRequest(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("HX-Request")), "true")
}

// renderBrowserBody, renderProjectSessions, renderProjectObservations, renderProjectPrompts
// removed in Batch 6 REFACTOR — replaced by templ partials (BrowserPage, SessionsPartial,
// ObservationsPartial, PromptsPartial, ProjectDetailPage).
// renderPageOrHTMX removed in R5-1 REFACTOR — callers now use renderComponent(w, r, Layout(...)).

// renderLoginPage removed in Batch 6 REFACTOR — replaced by LoginPage templ component.
// renderLayout, shellNavLink removed in R5-1 REFACTOR — all handlers now use the templ Layout component
// which includes status-ribbon, shell-footer, and CLOUD ACTIVE pill.

func renderHTML(w http.ResponseWriter, body string) {
	renderHTMLStatus(w, http.StatusOK, body)
}

func renderHTMLStatus(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// ─── Audit Log Handlers ───────────────────────────────────────────────────────

// handleAdminAuditLog handles GET /dashboard/admin/audit-log (shell, admin-gated).
// REQ-408: renders the AdminAuditLogPage templ component.
// JW2: filter is parsed and forwarded to the initial hx-get URL for deep-linking.
// JW6: invalid time formats yield 400.
func (h *handlers) handleAdminAuditLog(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	filter, filterErr := parseAuditFilter(r)
	if filterErr != "" {
		http.Error(w, filterErr, http.StatusBadRequest)
		return
	}
	component := AdminAuditLogPage(p.DisplayName(), filter)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Audit Log", p.DisplayName(), "admin", p.IsAdmin(), component))
}

// handleAdminAuditLogList handles GET /dashboard/admin/audit-log/list (partial, admin-gated, HTMX).
// REQ-409: renders AdminAuditLogListPartial with filter and pagination from query params.
// JW6: invalid time format in from/to params yields 400 instead of silent drop.
// N7: partial-only endpoint — always renders fragment, never a full Layout wrapper
// (even for non-HTMX requests). Consistent with R6-2 partial-only contract.
func (h *handlers) handleAdminAuditLogList(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	filter, filterErr := parseAuditFilter(r)
	if filterErr != "" {
		http.Error(w, filterErr, http.StatusBadRequest)
		return
	}
	reqPage, pageSize := parsePaginationRaw(r)

	var rows []cloudstore.DashboardAuditRow
	var total int
	if h.cfg.Store != nil {
		var err error
		rows, total, err = h.cfg.Store.ListAuditEntriesPaginated(r.Context(), filter, pageSize, (reqPage-1)*pageSize)
		if err != nil {
			log.Printf("dashboard: audit log list store error: %v", err)
			renderComponentStatus(w, r, http.StatusBadGateway, EmptyState("Service Unavailable", "Audit log data is temporarily unavailable."))
			return
		}
	}

	// JW3: three-tier fallback pattern — consistent with other paginated handlers.
	// Tier 1: initial fetch (above). Tier 2: clamped re-fetch on page-out-of-range.
	// Tier 3: page-1 fallback when re-fetch fails and rows are empty.
	pg, needsRefetch := reclampPagination(reqPage, pageSize, total)
	if needsRefetch && h.cfg.Store != nil {
		if refetched, _, err := h.cfg.Store.ListAuditEntriesPaginated(r.Context(), filter, pageSize, pg.Offset()); err == nil {
			rows = refetched
		} else {
			log.Printf("dashboard: re-fetch audit log list page %d: %v (using first-page rows)", pg.Page, err)
			if len(rows) == 0 {
				if fallback, _, fallbackErr := h.cfg.Store.ListAuditEntriesPaginated(r.Context(), filter, pageSize, 0); fallbackErr == nil {
					rows = fallback
				} else {
					log.Printf("dashboard: fallback audit log list page 1: %v", fallbackErr)
				}
			}
		}
	}

	renderComponent(w, r, AdminAuditLogListPartial(rows, pg, filter))
}

// parseAuditTime tries RFC3339 then date-only (2006-01-02) formats.
// Returns an error only when the value is non-empty and unparseable in either format.
// JW6: accepting date-only prevents confusing silent drops while still being lenient.
func parseAuditTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	// Try RFC3339 first (most specific).
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	// Fall back to date-only YYYY-MM-DD (midnight UTC).
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid_time_format: %q is not RFC3339 or YYYY-MM-DD", value)
}

// parseAuditFilter extracts AuditFilter fields from the request query params.
// Text filters are trimmed; time filters accept RFC3339 or date-only (YYYY-MM-DD). REQ-410.
// Returns the filter and an error string; on error, error is non-empty and the caller
// should return a 400 response. JW6 fix.
func parseAuditFilter(r *http.Request) (cloudstore.AuditFilter, string) {
	q := r.URL.Query()
	filter := cloudstore.AuditFilter{
		Contributor: strings.TrimSpace(q.Get("contributor")),
		Project:     strings.TrimSpace(q.Get("project")),
		Outcome:     strings.TrimSpace(q.Get("outcome")),
	}
	if from := strings.TrimSpace(q.Get("from")); from != "" {
		t, err := parseAuditTime(from)
		if err != nil {
			return cloudstore.AuditFilter{}, err.Error()
		}
		filter.OccurredAtFrom = t
	}
	if to := strings.TrimSpace(q.Get("to")); to != "" {
		t, err := parseAuditTime(to)
		if err != nil {
			return cloudstore.AuditFilter{}, err.Error()
		}
		filter.OccurredAtTo = t
	}
	return filter, ""
}
