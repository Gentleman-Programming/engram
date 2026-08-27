package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudserver"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
)

type runtimeOptionTestStore struct{}

func (runtimeOptionTestStore) ReadManifest(context.Context, string) (*engramsync.Manifest, error) {
	return &engramsync.Manifest{}, nil
}

func (runtimeOptionTestStore) WriteChunk(context.Context, string, string, string, string, []byte) error {
	return nil
}

func (runtimeOptionTestStore) ReadChunk(context.Context, string, string) ([]byte, error) {
	return nil, nil
}

func (runtimeOptionTestStore) KnownSessionIDs(context.Context, string) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

type runtimeOptionResolvingAuthenticator struct {
	principal cloudauth.Principal
}

func (runtimeOptionResolvingAuthenticator) Authorize(*http.Request) error { return nil }

func (a runtimeOptionResolvingAuthenticator) ResolveBearerToken(_ context.Context, token string) (cloudauth.Principal, error) {
	if token != "managed-token" {
		return cloudauth.Principal{}, cloudauth.ErrUnknownToken
	}
	return a.principal, nil
}

type runtimeOptionDenyingGrantStore struct{}

func (runtimeOptionDenyingGrantStore) ListProjectGrants(context.Context, string) ([]cloudstore.ProjectGrant, error) {
	return nil, nil
}

func TestCloudRuntimeServerOptionsInsecureModeEnforcesProjectAllowlist(t *testing.T) {
	server := cloudserver.New(
		runtimeOptionTestStore{},
		nil,
		0,
		cloudRuntimeServerOptions(cloud.Config{AllowedProjects: []string{"alpha"}}, nil, []string{"alpha"}, nil, nil, nil)...,
	)

	cases := []struct {
		name       string
		project    string
		wantStatus int
	}{
		{name: "allowed project succeeds without authentication", project: "alpha", wantStatus: http.StatusOK},
		{name: "disallowed project remains forbidden without authentication", project: "beta", wantStatus: http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sync/pull?project="+tc.project, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d body=%q", tc.wantStatus, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCloudRuntimeServerOptionsSecureModeFailsClosedWithoutPrincipal(t *testing.T) {
	authenticator, managedHasher, err := buildRuntimeAuthenticator(
		cloud.Config{JWTSecret: "test-jwt-secret-at-least-32-bytes-long"},
		nil,
		[]string{"alpha"},
		"sync-token",
		false,
	)
	if err != nil {
		t.Fatalf("buildRuntimeAuthenticator: %v", err)
	}
	server := cloudserver.New(
		runtimeOptionTestStore{},
		authenticator,
		0,
		cloudRuntimeServerOptions(cloud.Config{AllowedProjects: []string{"alpha"}}, nil, []string{"alpha"}, authenticator, managedHasher, nil)...,
	)

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sync/pull?project=alpha", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected secure mode without a principal to fail closed with 401, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestCloudRuntimeServerOptionsSecureModeAppliesPrincipalProjectGrants(t *testing.T) {
	authenticator := runtimeOptionResolvingAuthenticator{principal: cloudauth.Principal{
		ID:      "managed-principal",
		Kind:    cloudauth.PrincipalKindHuman,
		Role:    cloudauth.RoleMember,
		Source:  cloudauth.PrincipalSourceManagedToken,
		Enabled: true,
	}}
	server := cloudserver.New(
		runtimeOptionTestStore{},
		authenticator,
		0,
		cloudRuntimeServerOptions(
			cloud.Config{AllowedProjects: []string{"alpha"}},
			nil,
			[]string{"alpha"},
			authenticator,
			nil,
			runtimeOptionDenyingGrantStore{},
		)...,
	)

	req := httptest.NewRequest(http.MethodGet, "/sync/pull?project=alpha", nil)
	req.Header.Set("Authorization", "Bearer managed-token")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected authenticated managed principal without an alpha grant to be forbidden, got %d body=%q", rec.Code, rec.Body.String())
	}
}
