package cloudserver

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/dashboard"
)

// This file implements the dashboard-rendered Managed Users surface
// (cloud-user-token-management PR4): server-rendered forms and HTMX-friendly
// redirects for managed human user create/enable/disable, token
// create/revoke, and project grant create/revoke. It intentionally reuses
// the SAME storage boundary (AdminIdentityStore), SAME authorization gate
// (requireManagedAdmin), and SAME audit path (recordAdminAudit) already
// proven by admin_handlers.go for the JSON /admin/* API — this file only
// adds an HTML-rendering/redirect layer on top of that existing,
// policy-gated behavior. It does not re-decide authorization.
//
// These routes are registered directly on CloudServer's mux (see routes()),
// the same way /dashboard/bootstrap is, rather than through
// dashboard.Mount — because they need the admin identity store, managed
// token hasher, and audit helpers that already live on CloudServer.

// requireDashboardSession redirects unauthenticated dashboard requests to the
// login page (or, for HTMX requests, returns 401 with an HX-Redirect header),
// mirroring internal/cloud/dashboard's own requireSession middleware exactly
// so these cloudserver-owned routes behave identically to dashboard.Mount
// routes from a browser's perspective.
func (s *CloudServer) requireDashboardSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.authorizeDashboardRequest(r); err != nil {
			loginPath := dashboardLoginPathWithNextLocal(r.URL.RequestURI())
			if isDashboardHTMXRequest(r) {
				w.Header().Set("HX-Redirect", loginPath)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, loginPath, http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// isDashboardHTMXRequest reports whether r was made by HTMX (HX-Request
// header), mirroring internal/cloud/dashboard's unexported isHTMXRequest.
func isDashboardHTMXRequest(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("HX-Request")), "true")
}

// redirectDashboardAdmin redirects to path after a successful mutation,
// using HX-Redirect for HTMX requests (matching handleAdminSyncTogglePost's
// existing dual plain/HTMX redirect convention) and a normal 303 otherwise.
func redirectDashboardAdmin(w http.ResponseWriter, r *http.Request, path string) {
	if isDashboardHTMXRequest(r) {
		w.Header().Set("HX-Redirect", path)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// renderDashboardAdminComponent renders a templ component directly as the
// HTTP response body (never a redirect). Used for the show-once token page,
// where the raw token must appear only in this direct response body and
// never in a URL, Location header, or browser history entry.
func renderDashboardAdminComponent(w http.ResponseWriter, r *http.Request, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := component.Render(r.Context(), w); err != nil {
		http.Error(w, fmt.Sprintf("render dashboard component: %v", err), http.StatusInternalServerError)
	}
}

// renderDashboardAdminComponentStatus is renderDashboardAdminComponent but
// writes an explicit status code before rendering the body (e.g. 404 for a
// not-found page). PR4 review remediation (FIX A): the plain
// renderDashboardAdminComponent never sets a status code, so callers that
// render an error/not-found body without it silently default to 200.
func renderDashboardAdminComponentStatus(w http.ResponseWriter, r *http.Request, status int, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := component.Render(r.Context(), w); err != nil {
		// The status code is already written at this point, so this can only
		// log — it mirrors internal/cloud/dashboard's renderComponentStatus
		// convention for the same reason.
		fmt.Printf("dashboard: templ render error after status %d: %v\n", status, err)
	}
}

// parseDashboardMutationForm caps the POST body at maxDashboardLoginBodyBytes
// (the same limit already applied to the dashboard login and bootstrap POST
// forms) before calling r.ParseForm(), and reports 413 for an oversized
// payload instead of the generic 400 "invalid form" response. PR4 review
// remediation (FIX D): every dashboard-owned mutation POST handler must cap
// its body the same way handleDashboardBootstrapSubmit already does.
func parseDashboardMutationForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxDashboardLoginBodyBytes)
	if err := r.ParseForm(); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, fmt.Sprintf("payload too large (max %d bytes)", maxDashboardLoginBodyBytes), http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "invalid form", http.StatusBadRequest)
		return false
	}
	return true
}

// dashboardAdminLayout wraps a managed-users component in the same Layout
// shell used by internal/cloud/dashboard, deriving display name/admin state
// from the same revalidated-principal helpers dashboard routes already use
// (s.dashboardDisplayName, s.isDashboardAdmin) so the two surfaces are
// visually consistent.
func (s *CloudServer) dashboardAdminLayout(r *http.Request, title string, component templ.Component) templ.Component {
	return dashboard.Layout(title, s.dashboardDisplayName(r), "admin", s.isDashboardAdmin(r), component)
}

// handleDashboardCreateManagedUser handles POST /dashboard/admin/users.
func (s *CloudServer) handleDashboardCreateManagedUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	if !parseDashboardMutationForm(w, r) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	if username == "" {
		http.Error(w, "username is required", http.StatusBadRequest)
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	if role == "" {
		role = string(cloudauth.RoleMember)
	}
	user, err := store.CreateHumanUser(r.Context(), cloudstore.CreateHumanUserParams{
		Username:    username,
		Email:       r.FormValue("email"),
		DisplayName: r.FormValue("display_name"),
		Role:        role,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("create user: %v", err), http.StatusBadRequest)
		return
	}
	if err := s.recordAdminAudit(r.Context(), store, actor, authAuditActionUserCreate, user.PrincipalID, "", map[string]any{"kind": string(cloudauth.PrincipalKindHuman), "role": role, "username": username}); err != nil {
		http.Error(w, fmt.Sprintf("record audit: %v", err), http.StatusInternalServerError)
		return
	}
	redirectDashboardAdmin(w, r, "/dashboard/admin/users")
}

// handleDashboardEnableManagedUser handles POST /dashboard/admin/users/{principalID}/enable.
func (s *CloudServer) handleDashboardEnableManagedUser(w http.ResponseWriter, r *http.Request) {
	s.handleDashboardSetManagedUserEnabled(w, r, true, authAuditActionUserEnable)
}

// handleDashboardDisableManagedUser handles POST /dashboard/admin/users/{principalID}/disable.
func (s *CloudServer) handleDashboardDisableManagedUser(w http.ResponseWriter, r *http.Request) {
	s.handleDashboardSetManagedUserEnabled(w, r, false, authAuditActionUserDisable)
}

func (s *CloudServer) handleDashboardSetManagedUserEnabled(w http.ResponseWriter, r *http.Request, enabled bool, action string) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	principalID := strings.TrimSpace(r.PathValue("principalID"))
	if principalID == "" {
		http.Error(w, "principal id is required", http.StatusBadRequest)
		return
	}
	if err := store.SetHumanUserEnabled(r.Context(), principalID, enabled); err != nil {
		http.Error(w, fmt.Sprintf("set user enabled: %v", err), http.StatusBadRequest)
		return
	}
	if err := s.recordAdminAudit(r.Context(), store, actor, action, principalID, "", map[string]any{"enabled": enabled}); err != nil {
		http.Error(w, fmt.Sprintf("record audit: %v", err), http.StatusInternalServerError)
		return
	}
	redirectDashboardAdmin(w, r, "/dashboard/admin/users")
}

// handleDashboardManagedUserDetail handles GET /dashboard/admin/users/{principalID}.
// Read access uses the same lenient dashboard admin check as other dashboard
// admin pages (s.isDashboardAdmin, which also allows legacy/bootstrap admin
// sessions to VIEW admin surfaces); only mutations require the stricter
// managed-token-admin policy enforced by requireManagedAdmin.
func (s *CloudServer) handleDashboardManagedUserDetail(w http.ResponseWriter, r *http.Request) {
	if !s.isDashboardAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	principalID := strings.TrimSpace(r.PathValue("principalID"))
	if principalID == "" {
		http.Error(w, "principal id is required", http.StatusBadRequest)
		return
	}
	users, err := store.ListHumanUsers(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("list users: %v", err), http.StatusInternalServerError)
		return
	}
	var user cloudstore.HumanUser
	found := false
	for _, u := range users {
		if u.PrincipalID == principalID {
			user = u
			found = true
			break
		}
	}
	if !found {
		renderDashboardAdminComponentStatus(w, r, http.StatusNotFound, s.dashboardAdminLayout(r, "Managed User", dashboard.EmptyState("Managed User Not Found", "No managed user exists with that principal id.")))
		return
	}
	tokens, err := store.ListPrincipalTokens(r.Context(), principalID)
	if err != nil {
		http.Error(w, fmt.Sprintf("list tokens: %v", err), http.StatusInternalServerError)
		return
	}
	grants, err := store.ListProjectGrants(r.Context(), principalID)
	if err != nil {
		http.Error(w, fmt.Sprintf("list grants: %v", err), http.StatusInternalServerError)
		return
	}
	renderDashboardAdminComponent(w, r, s.dashboardAdminLayout(r, "Managed User", dashboard.ManagedUserDetailPage(user, tokens, grants)))
}

// handleDashboardCreateManagedToken handles POST /dashboard/admin/users/{principalID}/tokens.
// It renders the show-once raw token page directly as the response body —
// it never redirects, so the raw token is never placed in a URL, Location
// header, or browser history entry.
func (s *CloudServer) handleDashboardCreateManagedToken(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	if s.managedHasher == nil {
		http.Error(w, "managed token hasher is not configured", http.StatusInternalServerError)
		return
	}
	principalID := strings.TrimSpace(r.PathValue("principalID"))
	if principalID == "" {
		http.Error(w, "principal id is required", http.StatusBadRequest)
		return
	}
	if !parseDashboardMutationForm(w, r) {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	// PR4 review remediation (FIX B): verify the target principal exists
	// BEFORE minting/persisting a token. Minting first and only checking
	// existence afterward (to render the page) let a request for an
	// unknown/stale principalID silently mint and persist a real token
	// against a principal that does not exist, then render a blank-username
	// show-once page for it.
	user, found, err := findManagedUserByPrincipalID(r.Context(), store, principalID)
	if err != nil {
		http.Error(w, fmt.Sprintf("list users: %v", err), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "managed user not found", http.StatusNotFound)
		return
	}
	if !user.Enabled {
		s.renderDashboardManagedTokenDisabled(w, r, user)
		return
	}
	managedToken, err := cloudauth.GenerateManagedToken("live")
	if err != nil {
		http.Error(w, fmt.Sprintf("generate token: %v", err), http.StatusInternalServerError)
		return
	}
	tokenHash, err := s.managedHasher.Hash(managedToken.Raw)
	if err != nil {
		http.Error(w, fmt.Sprintf("hash token: %v", err), http.StatusInternalServerError)
		return
	}
	token, err := store.CreatePrincipalTokenWithAudit(r.Context(),
		cloudstore.CreatePrincipalTokenParams{
			PrincipalID:          principalID,
			TokenPrefix:          managedToken.Prefix,
			TokenHash:            tokenHash,
			Name:                 name,
			CreatedByPrincipalID: actor.ID,
		},
		adminAuditEvent(actor, authAuditActionTokenCreate, principalID, "", map[string]any{"token_prefix": managedToken.Prefix, "name": name}),
	)
	if err != nil {
		if errors.Is(err, cloudstore.ErrPrincipalDisabled) {
			s.renderDashboardManagedTokenDisabled(w, r, user)
			return
		}
		if errors.Is(err, cloudstore.ErrPrincipalNotFound) {
			http.Error(w, "managed user not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, cloudstore.ErrAuthAuditInsertFailed) {
			http.Error(w, fmt.Sprintf("record audit: %v", err), http.StatusInternalServerError)
			return
		}
		http.Error(w, fmt.Sprintf("create token: %v", err), http.StatusBadRequest)
		return
	}
	renderDashboardAdminComponent(w, r, s.dashboardAdminLayout(r, "Token Created", dashboard.ManagedUserTokenCreatedPage(user, managedToken.Raw, sanitizeTokenForDisplay(token))))
}

func (s *CloudServer) renderDashboardManagedTokenDisabled(w http.ResponseWriter, r *http.Request, user cloudstore.HumanUser) {
	message := "Enable this managed user before creating a managed token."
	if strings.TrimSpace(user.Username) != "" {
		message = fmt.Sprintf("%s User %s is currently disabled.", message, user.Username)
	}
	renderDashboardAdminComponentStatus(w, r, http.StatusConflict, s.dashboardAdminLayout(r, "Cannot Create Token", dashboard.EmptyState("Cannot Create Token", message)))
}

// sanitizeTokenForDisplay clears the token hash before handing a
// cloudstore.PrincipalToken to a templ component, so no rendering path can
// ever accidentally emit the stored hash even if a future template edit adds
// a field reference by mistake. The raw token itself is a separate value
// entirely (see handleDashboardCreateManagedToken) and is never derived from
// the stored token record.
func sanitizeTokenForDisplay(token cloudstore.PrincipalToken) cloudstore.PrincipalToken {
	token.TokenHash = ""
	return token
}

// handleDashboardRevokeManagedToken handles POST /dashboard/admin/tokens/{tokenID}/revoke.
func (s *CloudServer) handleDashboardRevokeManagedToken(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	tokenID := strings.TrimSpace(r.PathValue("tokenID"))
	if tokenID == "" {
		http.Error(w, "token id is required", http.StatusBadRequest)
		return
	}
	if !parseDashboardMutationForm(w, r) {
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	principalID := strings.TrimSpace(r.FormValue("principal_id"))
	if err := store.RevokePrincipalToken(r.Context(), tokenID, actor.ID, reason); err != nil {
		http.Error(w, fmt.Sprintf("revoke token: %v", err), http.StatusBadRequest)
		return
	}
	if err := s.recordAdminAudit(r.Context(), store, actor, authAuditActionTokenRevoke, "", "", map[string]any{"id": tokenID, "reason": reason}); err != nil {
		http.Error(w, fmt.Sprintf("record audit: %v", err), http.StatusInternalServerError)
		return
	}
	redirectPath := "/dashboard/admin/users"
	if principalID != "" {
		redirectPath = "/dashboard/admin/users/" + principalID
	}
	redirectDashboardAdmin(w, r, redirectPath)
}

// handleDashboardCreateManagedGrant handles POST /dashboard/admin/users/{principalID}/grants.
func (s *CloudServer) handleDashboardCreateManagedGrant(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	principalID := strings.TrimSpace(r.PathValue("principalID"))
	if principalID == "" {
		http.Error(w, "principal id is required", http.StatusBadRequest)
		return
	}
	if !parseDashboardMutationForm(w, r) {
		return
	}
	project := strings.TrimSpace(r.FormValue("project"))
	if project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	if _, err := store.CreateProjectGrant(r.Context(), cloudstore.CreateProjectGrantParams{PrincipalID: principalID, Project: project, GrantedByPrincipalID: actor.ID}); err != nil {
		http.Error(w, fmt.Sprintf("create grant: %v", err), http.StatusBadRequest)
		return
	}
	if err := s.recordAdminAudit(r.Context(), store, actor, authAuditActionGrantCreate, principalID, project, map[string]any{"project": project}); err != nil {
		http.Error(w, fmt.Sprintf("record audit: %v", err), http.StatusInternalServerError)
		return
	}
	redirectDashboardAdmin(w, r, "/dashboard/admin/users/"+principalID)
}

// handleDashboardRevokeManagedGrant handles POST /dashboard/admin/users/{principalID}/grants/{project}/revoke.
func (s *CloudServer) handleDashboardRevokeManagedGrant(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	principalID := strings.TrimSpace(r.PathValue("principalID"))
	project := strings.TrimSpace(r.PathValue("project"))
	if principalID == "" || project == "" {
		http.Error(w, "principal id and project are required", http.StatusBadRequest)
		return
	}
	// This handler does not read any form values, but the POST body is still
	// capped for defense-in-depth consistency with the other dashboard
	// mutation POST handlers (FIX D).
	if !parseDashboardMutationForm(w, r) {
		return
	}
	if err := store.RevokeProjectGrant(r.Context(), principalID, project); err != nil {
		http.Error(w, fmt.Sprintf("revoke grant: %v", err), http.StatusBadRequest)
		return
	}
	if err := s.recordAdminAudit(r.Context(), store, actor, authAuditActionGrantRevoke, principalID, project, map[string]any{"project": project}); err != nil {
		http.Error(w, fmt.Sprintf("record audit: %v", err), http.StatusInternalServerError)
		return
	}
	redirectDashboardAdmin(w, r, "/dashboard/admin/users/"+principalID)
}
