package cloudserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

type authorizeOnlyAuth struct{}

func (authorizeOnlyAuth) Authorize(*http.Request) error { return nil }

type resolvingAuth struct {
	principals map[string]cloudauth.Principal
	errors     map[string]error
}

func (a resolvingAuth) Authorize(*http.Request) error {
	return fmt.Errorf("legacy Authorize adapter should not be used when principal resolution is available")
}

func (a resolvingAuth) ResolveBearerToken(_ context.Context, token string) (cloudauth.Principal, error) {
	token = strings.TrimSpace(token)
	if err, ok := a.errors[token]; ok {
		return cloudauth.Principal{}, err
	}
	principal, ok := a.principals[token]
	if !ok {
		return cloudauth.Principal{}, cloudauth.ErrUnknownToken
	}
	return principal, nil
}

func TestSyncRoutesAuthenticateThroughPrincipalResolver(t *testing.T) {
	legacyPrincipal := cloudauth.Principal{ID: "legacy:sync", Kind: cloudauth.PrincipalKindLegacy, Role: cloudauth.RoleMember, Source: cloudauth.PrincipalSourceLegacyEnvSync, Enabled: true}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"legacy-token": legacyPrincipal}}

	ms := newFakeMutationStore()
	ms.chunks["chunk-1"] = []byte(`{"sessions":[]}`)
	srv := New(ms, authn, 0)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "pull manifest", method: http.MethodGet, path: "/sync/pull?project=proj-a"},
		{name: "pull chunk", method: http.MethodGet, path: "/sync/pull/chunk-1?project=proj-a"},
		{name: "push chunk", method: http.MethodPost, path: "/sync/push", body: `{"project":"proj-a","created_by":"tester","data":{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}]}}`},
		{name: "mutation push", method: http.MethodPost, path: "/sync/mutations/push", body: `{"entries":[{"project":"proj-a","entity":"observation","entity_key":"obs-1","op":"upsert","payload":{}}]}`},
		{name: "mutation pull", method: http.MethodGet, path: "/sync/mutations/pull?since_seq=0&limit=100"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			req.Header.Set("Authorization", "Bearer legacy-token")
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected valid legacy principal to access %s %s, got %d body=%q", tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPrincipalResolverStoresPrincipalInRequestContext(t *testing.T) {
	managedPrincipal := cloudauth.Principal{ID: "p-managed", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleMember, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"managed-token": managedPrincipal}}
	srv := New(&fakeStore{}, authn, 0)

	wrapped := srv.withAuth(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Fatalf("principal missing from request context")
		}
		if principal.ID != managedPrincipal.ID || principal.Source != cloudauth.PrincipalSourceManagedToken {
			t.Fatalf("unexpected principal in context: %+v", principal)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/pull?project=proj-a", nil)
	req.Header.Set("Authorization", "Bearer managed-token")
	wrapped(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected wrapped handler to run, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestLegacyAuthServiceResolvesSyncPrincipalIntoRequestContext(t *testing.T) {
	svc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	svc.SetBearerToken("legacy-token")
	srv := New(&fakeStore{}, svc, 0)

	wrapped := srv.withAuth(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Fatalf("legacy auth service did not store a principal in request context")
		}
		if principal.ID != "legacy:sync" || principal.Source != cloudauth.PrincipalSourceLegacyEnvSync || principal.Role != cloudauth.RoleMember {
			t.Fatalf("unexpected legacy sync principal: %+v", principal)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/pull?project=proj-a", nil)
	req.Header.Set("Authorization", "Bearer legacy-token")
	wrapped(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected wrapped handler to run, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestSyncRoutesRejectManagedTokenResolutionFailuresWithCurrentAuthStyle(t *testing.T) {
	revokedErr := cloudauth.ErrTokenRevoked
	authn := resolvingAuth{errors: map[string]error{
		"unknown-token": cloudauth.ErrUnknownToken,
		"revoked-token": revokedErr,
	}}
	srv := New(newFakeMutationStore(), authn, 0)

	cases := []struct {
		name   string
		header string
	}{
		{name: "malformed bearer", header: "Token managed-token"},
		{name: "unknown managed token", header: "Bearer unknown-token"},
		{name: "revoked managed token", header: "Bearer revoked-token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/sync/pull?project=proj-a", nil)
			req.Header.Set("Authorization", tc.header)
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d body=%q", rec.Code, rec.Body.String())
			}
			if !strings.HasPrefix(rec.Body.String(), "unauthorized:") {
				t.Fatalf("expected current auth error prefix, got body=%q", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "forbidden") {
				t.Fatalf("authn failures must not be reported as authorization failures: %q", rec.Body.String())
			}
		})
	}

	if !errors.Is(revokedErr, cloudauth.ErrTokenRevoked) {
		t.Fatalf("revoked test fixture must exercise ErrTokenRevoked")
	}
}

type managedGrantAuthorizer struct {
	grants map[string][]string
}

func (a managedGrantAuthorizer) AuthorizeProjectForPrincipal(_ context.Context, principal cloudauth.Principal, project string) error {
	for _, granted := range a.grants[principal.ID] {
		if granted == project {
			return nil
		}
	}
	return fmt.Errorf("principal %s has no grant for project %s", principal.ID, project)
}

func (a managedGrantAuthorizer) EnrolledProjectsForPrincipal(_ context.Context, principal cloudauth.Principal) ([]string, error) {
	projects := append([]string(nil), a.grants[principal.ID]...)
	return projects, nil
}

func TestManagedPrincipalProjectGrantsAuthorizeChunkPullAndPush(t *testing.T) {
	managedPrincipal := cloudauth.Principal{ID: "p-managed", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleMember, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"managed-token": managedPrincipal}}
	authz := managedGrantAuthorizer{grants: map[string][]string{"p-managed": {"alpha"}}}
	ms := newFakeMutationStore()
	srv := New(ms, authn, 0, WithPrincipalProjectAuthorizer(authz))

	pullGranted := httptest.NewRecorder()
	pullReq := httptest.NewRequest(http.MethodGet, "/sync/pull?project=alpha", nil)
	pullReq.Header.Set("Authorization", "Bearer managed-token")
	srv.Handler().ServeHTTP(pullGranted, pullReq)
	if pullGranted.Code != http.StatusOK {
		t.Fatalf("expected granted manifest pull 200, got %d body=%q", pullGranted.Code, pullGranted.Body.String())
	}

	ms.chunks["chunk-alpha"] = []byte(`{"sessions":[]}`)
	chunkGranted := httptest.NewRecorder()
	chunkGrantedReq := httptest.NewRequest(http.MethodGet, "/sync/pull/chunk-alpha?project=alpha", nil)
	chunkGrantedReq.Header.Set("Authorization", "Bearer managed-token")
	srv.Handler().ServeHTTP(chunkGranted, chunkGrantedReq)
	if chunkGranted.Code != http.StatusOK {
		t.Fatalf("expected granted chunk pull 200, got %d body=%q", chunkGranted.Code, chunkGranted.Body.String())
	}

	pullDenied := httptest.NewRecorder()
	pullDeniedReq := httptest.NewRequest(http.MethodGet, "/sync/pull?project=beta", nil)
	pullDeniedReq.Header.Set("Authorization", "Bearer managed-token")
	srv.Handler().ServeHTTP(pullDenied, pullDeniedReq)
	if pullDenied.Code != http.StatusForbidden {
		t.Fatalf("expected ungranted manifest pull 403, got %d body=%q", pullDenied.Code, pullDenied.Body.String())
	}

	chunkDenied := httptest.NewRecorder()
	chunkDeniedReq := httptest.NewRequest(http.MethodGet, "/sync/pull/chunk-alpha?project=beta", nil)
	chunkDeniedReq.Header.Set("Authorization", "Bearer managed-token")
	srv.Handler().ServeHTTP(chunkDenied, chunkDeniedReq)
	if chunkDenied.Code != http.StatusForbidden {
		t.Fatalf("expected ungranted chunk pull 403, got %d body=%q", chunkDenied.Code, chunkDenied.Body.String())
	}

	pushGranted := httptest.NewRecorder()
	pushGrantedBody := strings.NewReader(`{"project":"alpha","created_by":"tester","data":{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}]}}`)
	pushGrantedReq := httptest.NewRequest(http.MethodPost, "/sync/push", pushGrantedBody)
	pushGrantedReq.Header.Set("Authorization", "Bearer managed-token")
	pushGrantedReq.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(pushGranted, pushGrantedReq)
	if pushGranted.Code != http.StatusOK {
		t.Fatalf("expected granted chunk push 200, got %d body=%q", pushGranted.Code, pushGranted.Body.String())
	}
	if len(ms.chunks) == 0 {
		t.Fatal("granted chunk push should store a chunk")
	}

	chunkCountBeforeDeniedPush := len(ms.chunks)
	pushDenied := httptest.NewRecorder()
	pushBody := strings.NewReader(`{"project":"beta","created_by":"tester","data":{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}]}}`)
	pushReq := httptest.NewRequest(http.MethodPost, "/sync/push", pushBody)
	pushReq.Header.Set("Authorization", "Bearer managed-token")
	pushReq.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(pushDenied, pushReq)
	if pushDenied.Code != http.StatusForbidden {
		t.Fatalf("expected ungranted chunk push 403, got %d body=%q", pushDenied.Code, pushDenied.Body.String())
	}
	if len(ms.chunks) != chunkCountBeforeDeniedPush {
		t.Fatalf("ungranted push must not store chunks, before=%d after=%d", chunkCountBeforeDeniedPush, len(ms.chunks))
	}
}

func TestPrincipalProjectAuthorizerFailsClosedWithoutResolvedPrincipal(t *testing.T) {
	authz := managedGrantAuthorizer{grants: map[string][]string{"p-managed": {"alpha"}}}
	ms := newFakeMutationStore()
	_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
		{Project: "alpha", Entity: "observation", EntityKey: "obs-alpha", Op: "upsert", Payload: []byte(`{}`)},
		{Project: "beta", Entity: "observation", EntityKey: "obs-beta", Op: "upsert", Payload: []byte(`{}`)},
	})
	srv := New(ms, authorizeOnlyAuth{}, 0, WithPrincipalProjectAuthorizer(authz))

	manifestRec := httptest.NewRecorder()
	manifestReq := httptest.NewRequest(http.MethodGet, "/sync/pull?project=alpha", nil)
	manifestReq.Header.Set("Authorization", "Bearer accepted-by-compat-auth")
	srv.Handler().ServeHTTP(manifestRec, manifestReq)
	if manifestRec.Code != http.StatusForbidden {
		t.Fatalf("principal authorizer without resolved principal must fail closed for project routes, got %d body=%q", manifestRec.Code, manifestRec.Body.String())
	}

	mutationPullRec := httptest.NewRecorder()
	mutationPullReq := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	mutationPullReq.Header.Set("Authorization", "Bearer accepted-by-compat-auth")
	srv.Handler().ServeHTTP(mutationPullRec, mutationPullReq)
	if mutationPullRec.Code != http.StatusForbidden {
		t.Fatalf("principal authorizer without resolved principal must fail closed for mutation pull, got %d body=%q", mutationPullRec.Code, mutationPullRec.Body.String())
	}
	if strings.Contains(mutationPullRec.Body.String(), "obs-alpha") || strings.Contains(mutationPullRec.Body.String(), "obs-beta") {
		t.Fatalf("mutation pull fail-closed response must not leak project data: %q", mutationPullRec.Body.String())
	}
}

func TestManagedPrincipalMutationPullWithNoGrantsReturnsEmpty(t *testing.T) {
	managedPrincipal := cloudauth.Principal{ID: "p-no-grants", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleMember, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"managed-token": managedPrincipal}}
	authz := managedGrantAuthorizer{grants: map[string][]string{}}
	ms := newFakeMutationStore()
	_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
		{Project: "alpha", Entity: "observation", EntityKey: "obs-alpha", Op: "upsert", Payload: []byte(`{}`)},
		{Project: "beta", Entity: "observation", EntityKey: "obs-beta", Op: "upsert", Payload: []byte(`{}`)},
	})
	srv := New(ms, authn, 0, WithPrincipalProjectAuthorizer(authz))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	req.Header.Set("Authorization", "Bearer managed-token")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected no-grant managed mutation pull 200 empty, got %d body=%q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "obs-alpha") || strings.Contains(rec.Body.String(), "obs-beta") {
		t.Fatalf("no-grant managed mutation pull must not leak project data: %q", rec.Body.String())
	}
	var resp struct {
		Mutations []StoredMutation `json:"mutations"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode no-grant mutation pull: %v", err)
	}
	if len(resp.Mutations) != 0 {
		t.Fatalf("expected no-grant mutation pull to return no mutations, got %+v", resp.Mutations)
	}
}

func TestLegacyPrincipalKeepsAllowlistWhenPrincipalProjectAuthorizerConfigured(t *testing.T) {
	svc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	svc.SetBearerToken("legacy-token")
	svc.SetAllowedProjects([]string{"alpha"})
	authz := managedGrantAuthorizer{grants: map[string][]string{}}
	ms := newFakeMutationStore()
	_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
		{Project: "alpha", Entity: "observation", EntityKey: "obs-alpha", Op: "upsert", Payload: []byte(`{}`)},
		{Project: "beta", Entity: "observation", EntityKey: "obs-beta", Op: "upsert", Payload: []byte(`{}`)},
	})
	srv := New(ms, svc, 0, WithPrincipalProjectAuthorizer(authz))

	manifestGranted := httptest.NewRecorder()
	manifestGrantedReq := httptest.NewRequest(http.MethodGet, "/sync/pull?project=alpha", nil)
	manifestGrantedReq.Header.Set("Authorization", "Bearer legacy-token")
	srv.Handler().ServeHTTP(manifestGranted, manifestGrantedReq)
	if manifestGranted.Code != http.StatusOK {
		t.Fatalf("legacy allowlist project should remain authorized with principalProject configured, got %d body=%q", manifestGranted.Code, manifestGranted.Body.String())
	}

	manifestDenied := httptest.NewRecorder()
	manifestDeniedReq := httptest.NewRequest(http.MethodGet, "/sync/pull?project=beta", nil)
	manifestDeniedReq.Header.Set("Authorization", "Bearer legacy-token")
	srv.Handler().ServeHTTP(manifestDenied, manifestDeniedReq)
	if manifestDenied.Code != http.StatusForbidden {
		t.Fatalf("legacy allowlist should still deny unlisted project with principalProject configured, got %d body=%q", manifestDenied.Code, manifestDenied.Body.String())
	}

	pullRec := httptest.NewRecorder()
	pullReq := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	pullReq.Header.Set("Authorization", "Bearer legacy-token")
	srv.Handler().ServeHTTP(pullRec, pullReq)
	if pullRec.Code != http.StatusOK {
		t.Fatalf("legacy mutation pull should use legacy allowlist with principalProject configured, got %d body=%q", pullRec.Code, pullRec.Body.String())
	}
	if !strings.Contains(pullRec.Body.String(), "obs-alpha") || strings.Contains(pullRec.Body.String(), "obs-beta") {
		t.Fatalf("legacy mutation pull should include only allowed project data, got %q", pullRec.Body.String())
	}
}

func TestManagedPrincipalMutationPushRejectsMixedGrantBatchAllOrNothing(t *testing.T) {
	managedPrincipal := cloudauth.Principal{ID: "p-managed", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleMember, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"managed-token": managedPrincipal}}
	authz := managedGrantAuthorizer{grants: map[string][]string{"p-managed": {"alpha"}}}
	ms := newFakeMutationStore()
	srv := New(ms, authn, 0, WithPrincipalProjectAuthorizer(authz))

	body := strings.NewReader(`{"entries":[{"project":"alpha","entity":"observation","entity_key":"obs-1","op":"upsert","payload":{}},{"project":"beta","entity":"observation","entity_key":"obs-2","op":"upsert","payload":{}}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer managed-token")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected mixed grant batch 403, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.mutations) != 0 {
		t.Fatalf("mixed grant batch must reject all-or-nothing, stored %d mutations", len(ms.mutations))
	}
}

func TestManagedPrincipalMutationPullFiltersToGrantedProjects(t *testing.T) {
	managedPrincipal := cloudauth.Principal{ID: "p-managed", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleMember, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"managed-token": managedPrincipal}}
	authz := managedGrantAuthorizer{grants: map[string][]string{"p-managed": {"alpha"}}}
	ms := newFakeMutationStore()
	_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
		{Project: "alpha", Entity: "observation", EntityKey: "obs-alpha", Op: "upsert", Payload: []byte(`{}`)},
		{Project: "beta", Entity: "observation", EntityKey: "obs-beta", Op: "upsert", Payload: []byte(`{}`)},
	})
	srv := New(ms, authn, 0, WithPrincipalProjectAuthorizer(authz))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	req.Header.Set("Authorization", "Bearer managed-token")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected managed mutation pull 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		Mutations []struct {
			Project   string `json:"project"`
			EntityKey string `json:"entity_key"`
		} `json:"mutations"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode mutation pull: %v", err)
	}
	if len(resp.Mutations) != 1 || resp.Mutations[0].Project != "alpha" || resp.Mutations[0].EntityKey != "obs-alpha" {
		t.Fatalf("expected only granted alpha mutation, got %+v", resp.Mutations)
	}
}

func TestLegacySyncPrincipalPreservesAllowedProjectsSemantics(t *testing.T) {
	cases := []struct {
		name            string
		allowedProjects []string
		project         string
		wantStatus      int
	}{
		{name: "wildcard allows any normalized project", allowedProjects: []string{"*"}, project: "any-team-project", wantStatus: http.StatusOK},
		{name: "explicit list uses normalized project", allowedProjects: []string{"ALPHA"}, project: "alpha", wantStatus: http.StatusOK},
		{name: "empty allowlist denies", allowedProjects: []string{}, project: "alpha-project", wantStatus: http.StatusForbidden},
		{name: "missing allowlist denies", allowedProjects: nil, project: "alpha-project", wantStatus: http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
			if err != nil {
				t.Fatalf("new auth service: %v", err)
			}
			svc.SetBearerToken("legacy-token")
			if tc.allowedProjects != nil {
				svc.SetAllowedProjects(tc.allowedProjects)
			}
			srv := New(newFakeMutationStore(), svc, 0)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/sync/pull?project="+tc.project, nil)
			req.Header.Set("Authorization", "Bearer legacy-token")
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("expected %d, got %d body=%q", tc.wantStatus, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestLegacySyncPrincipalWildcardMutationPullKeepsNoFilterSemantics(t *testing.T) {
	svc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	svc.SetBearerToken("legacy-token")
	svc.SetAllowedProjects([]string{"*"})
	ms := newFakeMutationStore()
	_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
		{Project: "alpha", Entity: "observation", EntityKey: "obs-alpha", Op: "upsert", Payload: []byte(`{}`)},
		{Project: "beta", Entity: "observation", EntityKey: "obs-beta", Op: "upsert", Payload: []byte(`{}`)},
	})
	srv := New(ms, svc, 0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	req.Header.Set("Authorization", "Bearer legacy-token")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected wildcard mutation pull 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		Mutations []struct{ Project string } `json:"mutations"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode mutation pull: %v", err)
	}
	if len(resp.Mutations) != 2 {
		t.Fatalf("expected wildcard pull to return both projects, got %+v", resp.Mutations)
	}
}
