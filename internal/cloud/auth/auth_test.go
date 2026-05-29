package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

func TestNewServiceSecretValidation(t *testing.T) {
	_, err := NewService(&cloudstore.CloudStore{}, "short")
	if err == nil {
		t.Fatal("expected jwt secret validation error")
	}

	_, err = NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("expected valid secret, got %v", err)
	}
}

func TestAuthorizeBearerToken(t *testing.T) {
	svc, err := NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	tests := []struct {
		name    string
		token   string
		header  string
		wantErr bool
	}{
		{name: "no expected token is rejected", token: "", header: "", wantErr: true},
		{name: "missing header with expected token", token: "abc", header: "", wantErr: true},
		{name: "invalid bearer token", token: "abc", header: "Bearer nope", wantErr: true},
		{name: "valid bearer token", token: "abc", header: "Bearer abc", wantErr: false},
		{name: "valid lowercase bearer token", token: "abc", header: "bearer abc", wantErr: false},
		{name: "valid mixed-case bearer token", token: "abc", header: "BeArEr abc", wantErr: false},
		{name: "malformed bearer header with extra fields", token: "abc", header: "Bearer abc extra", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc.SetBearerToken(tt.token)
			req := httptest.NewRequest("GET", "/sync/pull", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			err := svc.Authorize(req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Authorize() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAuthorizeProjectScope(t *testing.T) {
	svc, err := NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	if err := svc.AuthorizeProject("proj-a"); err == nil {
		t.Fatal("expected failure when allowlist is not configured")
	}

	svc.SetAllowedProjects([]string{"proj-a", "proj-b"})
	if err := svc.AuthorizeProject("PROJ-A"); err != nil {
		t.Fatalf("expected normalized project to be allowed, got %v", err)
	}
	if err := svc.AuthorizeProject("proj-c"); err == nil {
		t.Fatal("expected disallowed project to be rejected")
	} else {
		if !errors.Is(err, ErrProjectNotAllowed) {
			t.Fatalf("expected ErrProjectNotAllowed, got %v", err)
		}
		if strings.Contains(err.Error(), "proj-a") || strings.Contains(err.Error(), "proj-b") {
			t.Fatalf("project scope error must not leak allowlist, got %q", err.Error())
		}
	}
}

func TestProjectScopeAuthorizerEnforcesAllowlistWithoutJWT(t *testing.T) {
	authorizer := NewProjectScopeAuthorizer([]string{"proj-a"})

	if err := authorizer.AuthorizeProject("PROJ-A"); err != nil {
		t.Fatalf("expected normalized project to be allowed, got %v", err)
	}
	if err := authorizer.AuthorizeProject("proj-b"); !errors.Is(err, ErrProjectNotAllowed) {
		t.Fatalf("expected ErrProjectNotAllowed for out-of-scope project, got %v", err)
	}

	authorizer = NewProjectScopeAuthorizer(nil)
	if err := authorizer.AuthorizeProject("proj-a"); err == nil {
		t.Fatal("expected empty allowlist to be rejected")
	}
}

// TestProjectScopeAuthorizerEnrolledProjects ensures the authorizer satisfies
// the cloudserver.EnrolledProjectsProvider contract so mutation pull filters
// to the allowlist instead of fail-closing to empty.
//
// Matches the structural interface:
//
//	interface { EnrolledProjects() []string }
func TestProjectScopeAuthorizerEnrolledProjects(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  []string
	}{
		{name: "multiple normalized and sorted", input: []string{"PROJ-B", "proj-a", "PROJ-C"}, want: []string{"proj-a", "proj-b", "proj-c"}},
		{name: "empty input returns empty slice (not nil)", input: nil, want: []string{}},
		{name: "duplicates collapsed via normalize", input: []string{"proj-a", "PROJ-A", "proj-a"}, want: []string{"proj-a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authorizer := NewProjectScopeAuthorizer(tc.input)

			// Structural assertion matching cloudserver.EnrolledProjectsProvider.
			var ep interface{ EnrolledProjects() []string } = authorizer
			got := ep.EnrolledProjects()
			if got == nil {
				t.Fatal("EnrolledProjects() returned nil; expected empty slice for fail-open-safe callers")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("length mismatch: got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestServiceEnrolledProjects mirrors the same contract on *Service, which
// is the production type used by `engram cloud serve` auth wiring.
func TestServiceEnrolledProjects(t *testing.T) {
	svc, err := NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	svc.SetAllowedProjects([]string{"engram", "GENTLE-AI", "engram"})

	var ep interface{ EnrolledProjects() []string } = svc
	got := ep.EnrolledProjects()
	want := []string{"engram", "gentle-ai"}
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDashboardSessionTokenRoundTrip(t *testing.T) {
	svc, err := NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	svc.SetBearerToken("secret-token")

	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	signed, err := svc.MintDashboardSession("secret-token")
	if err != nil {
		t.Fatalf("mint dashboard session: %v", err)
	}
	if signed == "secret-token" {
		t.Fatal("expected signed dashboard session token, got raw bearer token")
	}
	if strings.Contains(signed, "secret-token") {
		t.Fatal("expected dashboard session token to never reveal raw bearer token")
	}

	parts := strings.Split(signed, ".")
	if len(parts) != 2 {
		t.Fatalf("expected signed token to have two parts, got %d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	if _, ok := claims["bearer"]; ok {
		t.Fatal("expected dashboard session claims to omit raw bearer")
	}

	bearer, err := svc.ParseDashboardSession(signed)
	if err != nil {
		t.Fatalf("parse dashboard session: %v", err)
	}
	if bearer != "secret-token" {
		t.Fatalf("expected bearer secret-token, got %q", bearer)
	}
}

func TestDashboardSessionTokenRejectsTamperingAndExpiry(t *testing.T) {
	svc, err := NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	svc.SetBearerToken("secret-token")

	issuedAt := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return issuedAt }

	token, err := svc.MintDashboardSession("secret-token")
	if err != nil {
		t.Fatalf("mint dashboard session: %v", err)
	}

	if _, err := svc.ParseDashboardSession(token + "tampered"); !errors.Is(err, ErrInvalidDashboardSessionToken) {
		t.Fatalf("expected tampered token to fail with ErrInvalidDashboardSessionToken, got %v", err)
	}

	svc.now = func() time.Time { return issuedAt.Add(9 * time.Hour) }
	if _, err := svc.ParseDashboardSession(token); !errors.Is(err, ErrInvalidDashboardSessionToken) {
		t.Fatalf("expected expired token to fail with ErrInvalidDashboardSessionToken, got %v", err)
	}
}

func TestDashboardSessionTokenRejectsWhenConfiguredBearerChanges(t *testing.T) {
	svc, err := NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	svc.SetBearerToken("secret-token")

	token, err := svc.MintDashboardSession("secret-token")
	if err != nil {
		t.Fatalf("mint dashboard session: %v", err)
	}

	svc.SetBearerToken("rotated-token")
	if _, err := svc.ParseDashboardSession(token); !errors.Is(err, ErrInvalidDashboardSessionToken) {
		t.Fatalf("expected token minted for old bearer to fail after rotation, got %v", err)
	}
}

// TestAuthorizeBearerTokenConstantTimeComparison is a correctness guard for the
// constant-time token comparison at auth.go:253. A correct token must be accepted
// and any wrong token must be rejected, preserving behavior after the
// timing-safe hmac.Equal replacement (security issue #350).
func TestAuthorizeBearerTokenConstantTimeComparison(t *testing.T) {
	svc, err := NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	svc.SetBearerToken("correct-token")

	correct := httptest.NewRequest("GET", "/sync/pull", nil)
	correct.Header.Set("Authorization", "Bearer correct-token")
	if err := svc.Authorize(correct); err != nil {
		t.Fatalf("correct token must be accepted, got %v", err)
	}

	wrong := httptest.NewRequest("GET", "/sync/pull", nil)
	wrong.Header.Set("Authorization", "Bearer wrong-token")
	if err := svc.Authorize(wrong); err == nil {
		t.Fatal("wrong token must be rejected")
	}

	// A token that is a prefix of the correct one must also be rejected.
	prefix := httptest.NewRequest("GET", "/sync/pull", nil)
	prefix.Header.Set("Authorization", "Bearer correct-toke")
	if err := svc.Authorize(prefix); err == nil {
		t.Fatal("prefix of correct token must be rejected")
	}

	// A token that is a superset of the correct one must also be rejected.
	super := httptest.NewRequest("GET", "/sync/pull", nil)
	super.Header.Set("Authorization", "Bearer correct-token-extra")
	if err := svc.Authorize(super); err == nil {
		t.Fatal("superset of correct token must be rejected")
	}
}

// TestAuthorizeProjectWildcard tests that a single "*" in the allowlist permits any project.
func TestAuthorizeProjectWildcard(t *testing.T) {
	svc, err := NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	// "*" alone must allow any project.
	svc.SetAllowedProjects([]string{"*"})
	if err := svc.AuthorizeProject("any-project"); err != nil {
		t.Fatalf("wildcard allowlist must permit any project, got %v", err)
	}
	if err := svc.AuthorizeProject("ANOTHER-ONE"); err != nil {
		t.Fatalf("wildcard allowlist must permit uppercased project, got %v", err)
	}
	if err := svc.AuthorizeProject("team-foo"); err != nil {
		t.Fatalf("wildcard allowlist must permit prefixed project, got %v", err)
	}
}

// TestAuthorizeProjectWildcardMixedWithExact tests that "*" in a mixed list still allows all.
func TestAuthorizeProjectWildcardMixedWithExact(t *testing.T) {
	svc, err := NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	svc.SetAllowedProjects([]string{"proj-a", "*"})
	if err := svc.AuthorizeProject("anything-at-all"); err != nil {
		t.Fatalf("wildcard in mixed list must still permit any project, got %v", err)
	}
}

// TestProjectScopeAuthorizerWildcard tests that NewProjectScopeAuthorizer also respects "*".
func TestProjectScopeAuthorizerWildcard(t *testing.T) {
	authorizer := NewProjectScopeAuthorizer([]string{"*"})
	if err := authorizer.AuthorizeProject("any-project"); err != nil {
		t.Fatalf("wildcard authorizer must permit any project, got %v", err)
	}
	if err := authorizer.AuthorizeProject("team-foo"); err != nil {
		t.Fatalf("wildcard authorizer must permit team-prefixed project, got %v", err)
	}
}

// TestAuthorizeProjectExactMatchStillWorksAfterWildcardChange verifies backward compatibility.
func TestAuthorizeProjectExactMatchStillWorksAfterWildcardChange(t *testing.T) {
	svc, err := NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	// Exact allowlist: only listed projects pass.
	svc.SetAllowedProjects([]string{"proj-a", "proj-b"})
	if err := svc.AuthorizeProject("proj-a"); err != nil {
		t.Fatalf("exact match must still be allowed, got %v", err)
	}
	if err := svc.AuthorizeProject("proj-c"); !errors.Is(err, ErrProjectNotAllowed) {
		t.Fatalf("unlisted project must be rejected, got %v", err)
	}
}

func TestDashboardSessionTokenSupportsAdditionalDashboardCredential(t *testing.T) {
	svc, err := NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	svc.SetBearerToken("sync-token")
	svc.SetDashboardSessionTokens([]string{"admin-token"})

	adminSession, err := svc.MintDashboardSession("admin-token")
	if err != nil {
		t.Fatalf("mint dashboard admin session: %v", err)
	}

	parsed, err := svc.ParseDashboardSession(adminSession)
	if err != nil {
		t.Fatalf("parse dashboard admin session: %v", err)
	}
	if parsed != "admin-token" {
		t.Fatalf("expected parsed dashboard credential to preserve admin token, got %q", parsed)
	}

	svc.SetDashboardSessionTokens(nil)
	if _, err := svc.ParseDashboardSession(adminSession); !errors.Is(err, ErrInvalidDashboardSessionToken) {
		t.Fatalf("expected admin session to become invalid when additional dashboard credential is removed, got %v", err)
	}
}
