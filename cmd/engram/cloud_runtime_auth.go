package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// cloudRuntimeAuthenticator is the single Authenticator wired into
// cloudserver.New by newCloudRuntime. It embeds *auth.Service so Authorize
// (the Authenticator interface method) and the legacy dashboardSessionCodec
// pair (MintDashboardSession/ParseDashboardSession, used for the
// non-managed-principal dashboard cookie flow) keep behaving exactly as
// before, while overriding ResolveBearerToken to delegate to a
// cloudauth.PrincipalResolver that checks managed token storage (when a
// dedicated token pepper is configured) in addition to the legacy
// ENGRAM_CLOUD_TOKEN/ENGRAM_CLOUD_ADMIN env credentials. The resolver
// actually checks the legacy credentials FIRST and only falls back to
// managed token storage for non-legacy bearer tokens — see
// buildRuntimeAuthenticator's doc comment in cloud.go for why that order is
// a deliberate, safe deviation from design.md migration step 3's
// "managed-first" wording.
//
// This is the fix for the runtime gap flagged in PR5's apply-progress
// Discovery section: newCloudRuntime previously only ever constructed
// *auth.Service directly as the Authenticator, and auth.Service's own
// ResolveBearerToken only ever resolves the legacy sync token — so a
// managed token minted via `engram cloud bootstrap admin --issue-token` (or
// the admin/dashboard token-create routes) could never authenticate against
// a real `engram cloud serve` process. cloudserver's authenticateRequest
// picks whichever ResolveBearerToken implementation the Authenticator value
// carries (see cloudserver.go's `s.principalAuth` wiring in New()), so this
// wrapper is the entire fix: no cloudserver changes were required.
//
// NOTE: Authorize is NOT overridden. It is promoted, unmodified, from
// *auth.Service, so it only ever resolves the legacy sync token. That is
// safe because cloudserver.authenticateRequest only falls back to
// s.auth.Authorize when s.auth does NOT implement ResolveBearerToken; since
// cloudRuntimeAuthenticator always implements ResolveBearerToken,
// authenticateRequest for /sync/* and /admin/* routes always goes through
// the managed-token-aware path below, and Authorize is only ever reached via
// authorizeDashboardRequest's older legacy-cookie-revalidation branch, which
// intentionally only ever needs to revalidate a legacy (non-managed) session.
type cloudRuntimeAuthenticator struct {
	*auth.Service
	resolver *auth.PrincipalResolver
}

// ResolveBearerToken overrides the embedded *auth.Service method (Go method
// promotion does not provide virtual dispatch: auth.Service.Authorize's own
// internal call to s.ResolveBearerToken is unaffected by this override and
// continues to use the legacy-only behavior described above).
func (a *cloudRuntimeAuthenticator) ResolveBearerToken(ctx context.Context, token string) (auth.Principal, error) {
	return a.resolver.ResolveBearerToken(ctx, token)
}

// cloudManagedTokenHashStore is the narrow storage seam
// cloudstoreManagedTokenLookup depends on. *cloudstore.CloudStore satisfies
// it structurally; tests substitute an in-memory fake so the adapter's own
// field-mapping/error-mapping logic can be proven without a Postgres
// connection (see cloud_runtime_auth_test.go).
type cloudManagedTokenHashStore interface {
	FindPrincipalTokenByHash(ctx context.Context, tokenHash string) (cloudstore.PrincipalToken, cloudstore.Principal, error)
}

// cloudstoreManagedTokenLookup adapts cloudstore's storage-only
// FindPrincipalTokenByHash method to cloudauth.ManagedTokenLookup. It lives
// in cmd/engram (not cloudstore or auth) because internal/cloud/auth already
// imports internal/cloud/cloudstore, so cloudstore cannot import auth's
// cloudauth.Principal type without creating an import cycle; cmd/engram
// already imports both packages, matching how WithAdminIdentityStore and
// WithManagedTokenHasher are already wired here.
type cloudstoreManagedTokenLookup struct {
	store cloudManagedTokenHashStore
}

func (l cloudstoreManagedTokenLookup) FindManagedTokenByHash(ctx context.Context, hash string) (auth.ManagedTokenRecord, auth.Principal, error) {
	token, principal, err := l.store.FindPrincipalTokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, cloudstore.ErrPrincipalTokenNotFound) {
			return auth.ManagedTokenRecord{}, auth.Principal{}, auth.ErrUnknownToken
		}
		return auth.ManagedTokenRecord{}, auth.Principal{}, err
	}

	kind := auth.PrincipalKindHuman
	if principal.Kind == cloudstore.PrincipalKindServiceAccount {
		kind = auth.PrincipalKindServiceAccount
	}
	role := auth.RoleMember
	if principal.Role == cloudstore.PrincipalRoleAdmin {
		role = auth.RoleAdmin
	}

	resolvedPrincipal := auth.Principal{
		ID:          principal.ID,
		Kind:        kind,
		DisplayName: principal.DisplayName,
		Role:        role,
		Enabled:     principal.Enabled,
		Source:      auth.PrincipalSourceManagedToken,
	}
	record := auth.ManagedTokenRecord{
		ID:          token.ID,
		PrincipalID: token.PrincipalID,
		Hash:        token.TokenHash,
		RevokedAt:   token.RevokedAt,
	}
	return record, resolvedPrincipal, nil
}

// cloudProjectGrantStore is the narrow storage seam
// cloudPrincipalProjectAuthorizer depends on. *cloudstore.CloudStore
// satisfies it structurally; tests substitute an in-memory fake.
type cloudProjectGrantStore interface {
	ListProjectGrants(ctx context.Context, principalID string) ([]cloudstore.ProjectGrant, error)
}

// cloudPrincipalProjectAuthorizer implements cloudserver.PrincipalProjectAuthorizer
// for managed principals, backed by cloud_project_grants. It is
// deny-by-default: a principal with zero grants authorizes no project and
// enrolls in zero projects (never nil-as-all), matching the storage
// foundation's grant model. It lives in cmd/engram for the same
// import-boundary reason as cloudstoreManagedTokenLookup above.
type cloudPrincipalProjectAuthorizer struct {
	store cloudProjectGrantStore
}

func (a cloudPrincipalProjectAuthorizer) AuthorizeProjectForPrincipal(ctx context.Context, principal auth.Principal, project string) error {
	normalized := cloudstore.NormalizeProjectGrant(project)
	if normalized == "" {
		return fmt.Errorf("project is required")
	}
	grants, err := a.store.ListProjectGrants(ctx, principal.ID)
	if err != nil {
		return fmt.Errorf("list project grants: %w", err)
	}
	for _, grant := range grants {
		if grant.Project == normalized {
			return nil
		}
	}
	return fmt.Errorf("project is not granted")
}

func (a cloudPrincipalProjectAuthorizer) EnrolledProjectsForPrincipal(ctx context.Context, principal auth.Principal) ([]string, error) {
	grants, err := a.store.ListProjectGrants(ctx, principal.ID)
	if err != nil {
		return nil, fmt.Errorf("list project grants: %w", err)
	}
	projects := make([]string, 0, len(grants))
	for _, grant := range grants {
		projects = append(projects, grant.Project)
	}
	return projects, nil
}
