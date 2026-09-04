package dashboard

import (
	"net/http"
	"strings"
)

// Principal represents the authenticated dashboard user. It is a read-only
// view over MountConfig closures — no context reads for identity are performed
// inside handlers. Satisfies Design Decision 6.
type Principal struct {
	displayName           string
	isAdmin               bool
	canManageManagedUsers bool
}

// DisplayName returns the display name for this principal.
// An empty or whitespace-only name falls back to "OPERATOR".
func (p Principal) DisplayName() string {
	if strings.TrimSpace(p.displayName) == "" {
		return "OPERATOR"
	}
	return p.displayName
}

// IsAdmin returns whether this principal has admin privileges.
func (p Principal) IsAdmin() bool { return p.isAdmin }

// CanManageManagedUsers reports whether this dashboard principal may mutate
// managed users, tokens, and project grants.
func (p Principal) CanManageManagedUsers() bool { return p.canManageManagedUsers }

// principalFromRequest derives a Principal from the current request using the
// MountConfig closures. Handlers call p := h.principalFromRequest(r) and never
// read r.Context() for identity.
func (h *handlers) principalFromRequest(r *http.Request) Principal {
	name := ""
	if h.cfg.GetDisplayName != nil {
		name = strings.TrimSpace(h.cfg.GetDisplayName(r))
	}
	admin := false
	if h.cfg.IsAdmin != nil {
		admin = h.cfg.IsAdmin(r)
	}
	canManageManagedUsers := false
	if h.cfg.CanManageManagedUsers != nil {
		canManageManagedUsers = h.cfg.CanManageManagedUsers(r)
	}
	return Principal{displayName: name, isAdmin: admin, canManageManagedUsers: canManageManagedUsers}
}
