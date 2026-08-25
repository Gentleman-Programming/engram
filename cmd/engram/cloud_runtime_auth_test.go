package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// fakeManagedTokenHashStore is an in-memory stand-in for the narrow storage
// seam cloudstoreManagedTokenLookup depends on (cloudManagedTokenLookupStore),
// so the adapter's field-mapping/error-mapping logic can be proven without a
// real Postgres connection. This is the "non-gated test of the
// resolver/adapter wiring that runs in CI" companion to the Postgres-gated
// end-to-end test in cloud_runtime_e2e_test.go.
type fakeManagedTokenHashStore struct {
	byHash map[string]struct {
		token     cloudstore.PrincipalToken
		principal cloudstore.Principal
	}
	// forceErr, when non-nil, is returned by FindPrincipalTokenByHash instead
	// of consulting byHash, so tests can prove a generic (non-not-found)
	// storage error is propagated rather than swallowed or misreported.
	forceErr error
}

func (f *fakeManagedTokenHashStore) FindPrincipalTokenByHash(_ context.Context, hash string) (cloudstore.PrincipalToken, cloudstore.Principal, error) {
	if f.forceErr != nil {
		return cloudstore.PrincipalToken{}, cloudstore.Principal{}, f.forceErr
	}
	entry, ok := f.byHash[hash]
	if !ok {
		return cloudstore.PrincipalToken{}, cloudstore.Principal{}, cloudstore.ErrPrincipalTokenNotFound
	}
	return entry.token, entry.principal, nil
}

// TestCloudstoreManagedTokenLookupResolvesActiveManagedPrincipal is RED
// before cloudstoreManagedTokenLookup exists (undefined type). It proves the
// adapter maps a resolved cloudstore.Principal/PrincipalToken pair into the
// auth.Principal/auth.ManagedTokenRecord shape that
// auth.PrincipalResolver.ResolveBearerToken expects, including the
// admin-role and service-account-kind mapping paths.
func TestCloudstoreManagedTokenLookupResolvesActiveManagedPrincipal(t *testing.T) {
	store := &fakeManagedTokenHashStore{byHash: map[string]struct {
		token     cloudstore.PrincipalToken
		principal cloudstore.Principal
	}{
		"hash-admin": {
			token:     cloudstore.PrincipalToken{ID: "tok-1", PrincipalID: "p-1"},
			principal: cloudstore.Principal{ID: "p-1", Kind: cloudstore.PrincipalKindHuman, DisplayName: "Alice", Role: cloudstore.PrincipalRoleAdmin, Enabled: true},
		},
		"hash-service": {
			token:     cloudstore.PrincipalToken{ID: "tok-2", PrincipalID: "p-2"},
			principal: cloudstore.Principal{ID: "p-2", Kind: cloudstore.PrincipalKindServiceAccount, DisplayName: "CI Bot", Role: cloudstore.PrincipalRoleMember, Enabled: true},
		},
	}}
	lookup := cloudstoreManagedTokenLookup{store: store}

	record, principal, err := lookup.FindManagedTokenByHash(context.Background(), "hash-admin")
	if err != nil {
		t.Fatalf("FindManagedTokenByHash admin: %v", err)
	}
	if record.ID != "tok-1" || record.PrincipalID != "p-1" || record.RevokedAt != nil {
		t.Fatalf("unexpected managed token record: %+v", record)
	}
	if principal.ID != "p-1" || principal.Kind != auth.PrincipalKindHuman || principal.Role != auth.RoleAdmin || principal.Source != auth.PrincipalSourceManagedToken || !principal.Enabled {
		t.Fatalf("unexpected mapped admin principal: %+v", principal)
	}

	_, principal, err = lookup.FindManagedTokenByHash(context.Background(), "hash-service")
	if err != nil {
		t.Fatalf("FindManagedTokenByHash service account: %v", err)
	}
	if principal.Kind != auth.PrincipalKindServiceAccount || principal.Role != auth.RoleMember {
		t.Fatalf("unexpected mapped service-account principal: %+v", principal)
	}
}

// TestCloudstoreManagedTokenLookupMapsNotFoundToUnknownToken is RED before
// cloudstoreManagedTokenLookup exists. It proves an unresolved hash surfaces
// auth.ErrUnknownToken (the sentinel auth.PrincipalResolver already checks
// via errors.Is), not the storage-layer cloudstore.ErrPrincipalTokenNotFound,
// so cloudserver's "unauthorized: %v" error text never leaks a
// cloudstore-specific error string to API callers.
func TestCloudstoreManagedTokenLookupMapsNotFoundToUnknownToken(t *testing.T) {
	lookup := cloudstoreManagedTokenLookup{store: &fakeManagedTokenHashStore{byHash: map[string]struct {
		token     cloudstore.PrincipalToken
		principal cloudstore.Principal
	}{}}}

	_, _, err := lookup.FindManagedTokenByHash(context.Background(), "unknown-hash")
	if !errors.Is(err, auth.ErrUnknownToken) {
		t.Fatalf("expected auth.ErrUnknownToken, got %v", err)
	}
}

// errBoom is a sentinel used by generic-store-error propagation tests below;
// it is deliberately NOT cloudstore.ErrPrincipalTokenNotFound, so it proves
// the adapter distinguishes "unknown token" from "the store failed" instead
// of collapsing every error into ErrUnknownToken.
var errBoom = errors.New("boom: storage layer unavailable")

// TestCloudstoreManagedTokenLookupPropagatesGenericStoreError is RED before
// this coverage existed: a generic (non-ErrPrincipalTokenNotFound) storage
// error from FindPrincipalTokenByHash — e.g. a dropped connection — must be
// returned to the caller as-is, not silently mapped to auth.ErrUnknownToken.
// Collapsing every storage failure into "unknown token" would make a
// transient DB outage look identical to "this bearer token was never
// issued," which is both a misleading 401 and a debugging dead end. This
// test would catch a regression that widened the errors.Is(err,
// cloudstore.ErrPrincipalTokenNotFound) check (or removed it) so generic
// errors got remapped too.
func TestCloudstoreManagedTokenLookupPropagatesGenericStoreError(t *testing.T) {
	lookup := cloudstoreManagedTokenLookup{store: &fakeManagedTokenHashStore{forceErr: errBoom}}

	_, _, err := lookup.FindManagedTokenByHash(context.Background(), "any-hash")
	if !errors.Is(err, errBoom) {
		t.Fatalf("expected the generic store error to be propagated as-is, got %v", err)
	}
	if errors.Is(err, auth.ErrUnknownToken) {
		t.Fatal("a generic store error must not be reported as ErrUnknownToken")
	}
}

// fakeProjectGrantStore is an in-memory stand-in for cloudProjectGrantStore.
type fakeProjectGrantStore struct {
	grantsByPrincipal map[string][]cloudstore.ProjectGrant
	// forceErr, when non-nil, is returned by ListProjectGrants instead of
	// consulting grantsByPrincipal, so tests can prove a store error is
	// propagated (deny-by-default via an explicit error) rather than
	// silently treated as "no grants" or, worse, "all projects allowed".
	forceErr error
}

func (f *fakeProjectGrantStore) ListProjectGrants(_ context.Context, principalID string) ([]cloudstore.ProjectGrant, error) {
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	return f.grantsByPrincipal[principalID], nil
}

// TestCloudPrincipalProjectAuthorizerDenyByDefaultAndGrantedProject is RED
// before cloudPrincipalProjectAuthorizer exists. It proves the runtime
// managed-principal project authorizer is deny-by-default: a principal with
// no grants is denied every project and enrolls in zero projects (not "all"
// via a nil slice), while a granted project authorizes and appears in the
// enrolled list, matching cloud_project_grants' deny-by-default design.
func TestCloudPrincipalProjectAuthorizerDenyByDefaultAndGrantedProject(t *testing.T) {
	store := &fakeProjectGrantStore{grantsByPrincipal: map[string][]cloudstore.ProjectGrant{
		"p-1": {{PrincipalID: "p-1", Project: "alpha-project"}},
	}}
	authorizer := cloudPrincipalProjectAuthorizer{store: store}

	if err := authorizer.AuthorizeProjectForPrincipal(context.Background(), auth.Principal{ID: "p-1"}, "Alpha Project"); err != nil {
		t.Fatalf("expected granted project (after normalization) to authorize, got %v", err)
	}
	if err := authorizer.AuthorizeProjectForPrincipal(context.Background(), auth.Principal{ID: "p-1"}, "beta-project"); err == nil {
		t.Fatal("expected ungranted project to be denied")
	}
	if err := authorizer.AuthorizeProjectForPrincipal(context.Background(), auth.Principal{ID: "p-no-grants"}, "alpha-project"); err == nil {
		t.Fatal("expected principal with zero grants to be denied (deny-by-default)")
	}

	enrolled, err := authorizer.EnrolledProjectsForPrincipal(context.Background(), auth.Principal{ID: "p-1"})
	if err != nil {
		t.Fatalf("EnrolledProjectsForPrincipal: %v", err)
	}
	if len(enrolled) != 1 || enrolled[0] != "alpha-project" {
		t.Fatalf("expected [alpha-project], got %v", enrolled)
	}

	noGrants, err := authorizer.EnrolledProjectsForPrincipal(context.Background(), auth.Principal{ID: "p-no-grants"})
	if err != nil {
		t.Fatalf("EnrolledProjectsForPrincipal no grants: %v", err)
	}
	if len(noGrants) != 0 {
		t.Fatalf("expected empty (not nil-as-all) enrolled projects for a principal with no grants, got %v", noGrants)
	}
}

// TestCloudPrincipalProjectAuthorizerRejectsEmptyOrWhitespaceProject is RED
// before this coverage existed: an empty or whitespace-only project name
// must be rejected with a "project is required" error before ever calling
// ListProjectGrants, not silently normalized to "" and then denied (or,
// worse, matched against a stored empty-string grant). This would catch a
// regression that removed the early-return guard and let a blank project
// fall through to the grant-list comparison.
func TestCloudPrincipalProjectAuthorizerRejectsEmptyOrWhitespaceProject(t *testing.T) {
	store := &fakeProjectGrantStore{grantsByPrincipal: map[string][]cloudstore.ProjectGrant{}}
	authorizer := cloudPrincipalProjectAuthorizer{store: store}

	for _, project := range []string{"", "   ", "\t\n"} {
		err := authorizer.AuthorizeProjectForPrincipal(context.Background(), auth.Principal{ID: "p-1"}, project)
		if err == nil {
			t.Fatalf("expected empty/whitespace project %q to be rejected", project)
		}
		if !strings.Contains(err.Error(), "project is required") {
			t.Fatalf("expected %q error, got %v", "project is required", err)
		}
	}
}

// TestCloudPrincipalProjectAuthorizerPropagatesListGrantsError is RED before
// this coverage existed: if the underlying store's ListProjectGrants call
// fails, AuthorizeProjectForPrincipal and EnrolledProjectsForPrincipal must
// both return that error, never silently authorize (treating "store error"
// as "no grants found, but let it through anyway") or silently deny in a way
// that swallows the underlying cause. This test would catch a regression
// that ignored the store error and fell through to the "not granted" /
// empty-slice-with-nil-error return path.
func TestCloudPrincipalProjectAuthorizerPropagatesListGrantsError(t *testing.T) {
	store := &fakeProjectGrantStore{forceErr: errBoom}
	authorizer := cloudPrincipalProjectAuthorizer{store: store}

	if err := authorizer.AuthorizeProjectForPrincipal(context.Background(), auth.Principal{ID: "p-1"}, "alpha-project"); err == nil || !errors.Is(err, errBoom) {
		t.Fatalf("expected AuthorizeProjectForPrincipal to propagate the store error, got %v", err)
	}
	if _, err := authorizer.EnrolledProjectsForPrincipal(context.Background(), auth.Principal{ID: "p-1"}); err == nil || !errors.Is(err, errBoom) {
		t.Fatalf("expected EnrolledProjectsForPrincipal to propagate the store error, got %v", err)
	}
}

// TestCloudRuntimeAuthenticatorResolvesManagedTokenBeforeLegacyFallback is RED
// before cloudRuntimeAuthenticator exists. It proves the exact composite
// authenticator wired into cloudserver.New by newCloudRuntime resolves a
// managed token to a PrincipalSourceManagedToken principal, still resolves
// the legacy ENGRAM_CLOUD_TOKEN sync credential (compatibility), and rejects
// an unknown bearer token — using the real auth.PrincipalResolver and a real
// auth.ManagedTokenHasher, not a test-only fake resolver.
func TestCloudRuntimeAuthenticatorResolvesManagedTokenBeforeLegacyFallback(t *testing.T) {
	const pepper = "dedicated-cloud-token-pepper-for-cmd-engram-tests"
	hasher, err := auth.NewManagedTokenHasher([]byte(pepper))
	if err != nil {
		t.Fatalf("NewManagedTokenHasher: %v", err)
	}
	const rawManagedToken = "egc_live_deadbeef_managed-secret-value"
	hash, err := hasher.Hash(rawManagedToken)
	if err != nil {
		t.Fatalf("hash managed token: %v", err)
	}
	lookup := &fakeManagedTokenHashStore{byHash: map[string]struct {
		token     cloudstore.PrincipalToken
		principal cloudstore.Principal
	}{
		hash: {
			token:     cloudstore.PrincipalToken{ID: "tok-1", PrincipalID: "p-1"},
			principal: cloudstore.Principal{ID: "p-1", Kind: cloudstore.PrincipalKindHuman, DisplayName: "Alice", Role: cloudstore.PrincipalRoleAdmin, Enabled: true},
		},
	}}

	authSvc, err := auth.NewService(nil, "test-jwt-secret-at-least-32-bytes-long")
	if err != nil {
		t.Fatalf("auth.NewService: %v", err)
	}
	authSvc.SetBearerToken("legacy-sync-token")

	resolver := auth.NewPrincipalResolver(auth.ResolverConfig{
		Hasher:        hasher,
		ManagedTokens: cloudstoreManagedTokenLookup{store: lookup},
		Legacy:        auth.LegacyCredentials{SyncToken: "legacy-sync-token", AdminToken: "legacy-admin-token"},
	})
	runtimeAuth := &cloudRuntimeAuthenticator{Service: authSvc, resolver: resolver}

	principal, err := runtimeAuth.ResolveBearerToken(context.Background(), rawManagedToken)
	if err != nil {
		t.Fatalf("resolve managed token: %v", err)
	}
	if principal.Source != auth.PrincipalSourceManagedToken || principal.Role != auth.RoleAdmin {
		t.Fatalf("expected managed admin principal, got %+v", principal)
	}

	legacyPrincipal, err := runtimeAuth.ResolveBearerToken(context.Background(), "legacy-sync-token")
	if err != nil {
		t.Fatalf("resolve legacy sync token: %v", err)
	}
	if legacyPrincipal.Source != auth.PrincipalSourceLegacyEnvSync {
		t.Fatalf("expected legacy sync principal, got %+v", legacyPrincipal)
	}

	if _, err := runtimeAuth.ResolveBearerToken(context.Background(), "totally-unknown-token"); err == nil {
		t.Fatal("expected unknown token to be rejected")
	}

	// Authorize (the Authenticator interface method, promoted from the
	// embedded *auth.Service) must still work for the legacy sync token, so
	// runtimeAuth satisfies cloudserver.Authenticator exactly as before.
	req, err := http.NewRequest(http.MethodGet, "/sync/pull", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer legacy-sync-token")
	if err := runtimeAuth.Authorize(req); err != nil {
		t.Fatalf("Authorize legacy sync token: %v", err)
	}
}

// newTestRuntimeAuthenticatorForHash builds a *cloudRuntimeAuthenticator
// wired to a single managed token entry, hashed with a real
// auth.ManagedTokenHasher, exactly like production wiring (see
// buildRuntimeAuthenticator). It returns the authenticator and the raw
// (unhashed) token string tests should present as the bearer token.
func newTestRuntimeAuthenticatorForHash(t *testing.T, entry struct {
	token     cloudstore.PrincipalToken
	principal cloudstore.Principal
}) (*cloudRuntimeAuthenticator, string) {
	t.Helper()
	const pepper = "dedicated-cloud-token-pepper-for-cmd-engram-tests"
	hasher, err := auth.NewManagedTokenHasher([]byte(pepper))
	if err != nil {
		t.Fatalf("NewManagedTokenHasher: %v", err)
	}
	const rawManagedToken = "egc_live_deadbeef_managed-secret-value"
	hash, err := hasher.Hash(rawManagedToken)
	if err != nil {
		t.Fatalf("hash managed token: %v", err)
	}
	lookup := &fakeManagedTokenHashStore{byHash: map[string]struct {
		token     cloudstore.PrincipalToken
		principal cloudstore.Principal
	}{hash: entry}}

	authSvc, err := auth.NewService(nil, "test-jwt-secret-at-least-32-bytes-long")
	if err != nil {
		t.Fatalf("auth.NewService: %v", err)
	}
	authSvc.SetBearerToken("legacy-sync-token")
	resolver := auth.NewPrincipalResolver(auth.ResolverConfig{
		Hasher:        hasher,
		ManagedTokens: cloudstoreManagedTokenLookup{store: lookup},
		Legacy:        auth.LegacyCredentials{SyncToken: "legacy-sync-token", AdminToken: "legacy-admin-token"},
	})
	return &cloudRuntimeAuthenticator{Service: authSvc, resolver: resolver}, rawManagedToken
}

// TestCloudRuntimeAuthenticatorRejectsRevokedManagedToken is RED before this
// coverage existed: today this rejection path is only proven by the
// Postgres-gated cloud_runtime_e2e_test.go, which skips in CI without a
// CLOUDSTORE_TEST_DSN. This test proves — with no DB required — that a
// managed token whose ManagedTokenRecord.RevokedAt is set is rejected with
// auth.ErrTokenRevoked through the real cloudRuntimeAuthenticator ->
// PrincipalResolver seam, not resolved to a usable principal. It would catch
// a regression that dropped or reordered the RevokedAt check in
// foundation.go's ResolveBearerToken.
func TestCloudRuntimeAuthenticatorRejectsRevokedManagedToken(t *testing.T) {
	revokedAt := time.Now().Add(-time.Hour)
	runtimeAuth, rawToken := newTestRuntimeAuthenticatorForHash(t, struct {
		token     cloudstore.PrincipalToken
		principal cloudstore.Principal
	}{
		token:     cloudstore.PrincipalToken{ID: "tok-1", PrincipalID: "p-1", RevokedAt: &revokedAt},
		principal: cloudstore.Principal{ID: "p-1", Kind: cloudstore.PrincipalKindHuman, DisplayName: "Alice", Role: cloudstore.PrincipalRoleAdmin, Enabled: true},
	})

	if _, err := runtimeAuth.ResolveBearerToken(context.Background(), rawToken); !errors.Is(err, auth.ErrTokenRevoked) {
		t.Fatalf("expected a revoked managed token to be rejected with auth.ErrTokenRevoked, got %v", err)
	}
}

// TestCloudRuntimeAuthenticatorRejectsDisabledPrincipal is RED before this
// coverage existed: today only the Postgres-gated e2e test exercises
// anything adjacent to this path (and it does not cover disabled principals
// at all). This proves a managed token owned by a disabled principal
// (Enabled=false) is rejected with auth.ErrPrincipalDisabled, not resolved
// to a usable principal — catching a regression that dropped or reordered
// the Enabled check in foundation.go's ResolveBearerToken.
func TestCloudRuntimeAuthenticatorRejectsDisabledPrincipal(t *testing.T) {
	runtimeAuth, rawToken := newTestRuntimeAuthenticatorForHash(t, struct {
		token     cloudstore.PrincipalToken
		principal cloudstore.Principal
	}{
		token:     cloudstore.PrincipalToken{ID: "tok-1", PrincipalID: "p-1"},
		principal: cloudstore.Principal{ID: "p-1", Kind: cloudstore.PrincipalKindHuman, DisplayName: "Alice", Role: cloudstore.PrincipalRoleAdmin, Enabled: false},
	})

	if _, err := runtimeAuth.ResolveBearerToken(context.Background(), rawToken); !errors.Is(err, auth.ErrPrincipalDisabled) {
		t.Fatalf("expected a disabled principal's managed token to be rejected with auth.ErrPrincipalDisabled, got %v", err)
	}
}

// TestCloudRuntimeAuthenticatorRejectsPrincipalTokenMismatch is RED before
// this coverage existed. It proves that if a managed-token lookup ever
// returns a ManagedTokenRecord.PrincipalID that does not match the resolved
// Principal.ID (a storage-layer invariant violation — e.g. a join bug),
// ResolveBearerToken rejects the request instead of trusting a token record
// for a different principal than the one it authenticates as. This is a
// defense-in-depth check reachable through the real adapter/resolver seam;
// it would catch a regression that removed or weakened that consistency
// check in foundation.go.
func TestCloudRuntimeAuthenticatorRejectsPrincipalTokenMismatch(t *testing.T) {
	runtimeAuth, rawToken := newTestRuntimeAuthenticatorForHash(t, struct {
		token     cloudstore.PrincipalToken
		principal cloudstore.Principal
	}{
		token:     cloudstore.PrincipalToken{ID: "tok-1", PrincipalID: "p-1"},
		principal: cloudstore.Principal{ID: "p-mismatched", Kind: cloudstore.PrincipalKindHuman, DisplayName: "Alice", Role: cloudstore.PrincipalRoleAdmin, Enabled: true},
	})

	if _, err := runtimeAuth.ResolveBearerToken(context.Background(), rawToken); !errors.Is(err, auth.ErrInvalidPrincipal) {
		t.Fatalf("expected a token/principal ID mismatch to be rejected with auth.ErrInvalidPrincipal, got %v", err)
	}
}
