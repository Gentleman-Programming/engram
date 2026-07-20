package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud"
	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
)

// These tests cover buildRuntimeAuthenticator's three startup-decision
// branches without a live Postgres connection (cs is nil throughout — see
// the seam's doc comment in cloud.go for why that is safe here). Before this
// seam was extracted, all three branches were only reachable through
// newCloudRuntime, which requires cloudstore.New to succeed against a real
// DSN, so none of them ran in CI.

// TestBuildRuntimeAuthenticatorInsecureNoAuthReturnsNilAuthenticator proves
// the insecureNoAuth branch: no authenticator, no hasher, no error. This is
// the "ENGRAM_CLOUD_INSECURE_NO_AUTH=1" startup path; a regression here
// (e.g. accidentally constructing an authenticator anyway) would silently
// re-enable an auth check the operator explicitly disabled, or vice versa.
func TestBuildRuntimeAuthenticatorInsecureNoAuthReturnsNilAuthenticator(t *testing.T) {
	authenticator, hasher, err := buildRuntimeAuthenticator(cloud.Config{}, nil, nil, "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authenticator != nil {
		t.Fatalf("expected nil authenticator in insecure-no-auth mode, got %#v", authenticator)
	}
	if hasher != nil {
		t.Fatalf("expected nil managed token hasher in insecure-no-auth mode, got %#v", hasher)
	}
}

// TestBuildRuntimeAuthenticatorRejectsTooShortPepperWithoutLoggingValue
// proves the "pepper present but too short" startup failure branch: it
// returns a clear error wrapping auth.ErrTokenPepperTooShort, and the error
// text never contains the raw pepper value (which would otherwise leak a
// secret into logs on every failed startup). A regression that swallowed
// NewManagedTokenHasher's error, or one that interpolated the raw pepper
// into the wrapping error message, would be caught here.
func TestBuildRuntimeAuthenticatorRejectsTooShortPepperWithoutLoggingValue(t *testing.T) {
	const tooShortPepper = "too-short-secret-value"
	cfg := cloud.Config{
		JWTSecret:   "test-jwt-secret-at-least-32-bytes-long",
		TokenPepper: tooShortPepper,
	}
	authenticator, hasher, err := buildRuntimeAuthenticator(cfg, nil, nil, "legacy-token", false)
	if err == nil {
		t.Fatal("expected an error for a too-short dedicated token pepper")
	}
	if !errors.Is(err, auth.ErrTokenPepperTooShort) {
		t.Fatalf("expected error to wrap auth.ErrTokenPepperTooShort, got %v", err)
	}
	if strings.Contains(err.Error(), tooShortPepper) {
		t.Fatalf("startup error must not leak the raw pepper value, got %q", err.Error())
	}
	if authenticator != nil {
		t.Fatalf("expected nil authenticator on pepper validation failure, got %#v", authenticator)
	}
	if hasher != nil {
		t.Fatalf("expected nil managed token hasher on pepper validation failure, got %#v", hasher)
	}
}

// TestBuildRuntimeAuthenticatorNoPepperDisablesManagedAuthButKeepsLegacy
// proves the graceful-degradation branch: with no ENGRAM_CLOUD_TOKEN_PEPPER
// configured, managed-token auth is disabled (nil hasher) but the legacy
// sync token still authenticates through the constructed authenticator. A
// regression that failed startup instead of degrading gracefully, or one
// that left managed-token auth silently half-wired (e.g. a non-nil resolver
// that mismaps an arbitrary bearer token to a managed principal), would be
// caught here.
func TestBuildRuntimeAuthenticatorNoPepperDisablesManagedAuthButKeepsLegacy(t *testing.T) {
	cfg := cloud.Config{
		JWTSecret:  "test-jwt-secret-at-least-32-bytes-long",
		AdminToken: "legacy-admin-token",
	}
	authenticator, hasher, err := buildRuntimeAuthenticator(cfg, nil, nil, "legacy-sync-token", false)
	if err != nil {
		t.Fatalf("unexpected error with no token pepper configured: %v", err)
	}
	if hasher != nil {
		t.Fatalf("expected nil managed token hasher with no pepper configured, got %#v", hasher)
	}
	if authenticator == nil {
		t.Fatal("expected a non-nil authenticator even with managed-token auth disabled")
	}
	runtimeAuth, ok := authenticator.(*cloudRuntimeAuthenticator)
	if !ok {
		t.Fatalf("expected *cloudRuntimeAuthenticator, got %T", authenticator)
	}

	principal, err := runtimeAuth.ResolveBearerToken(context.Background(), "legacy-sync-token")
	if err != nil {
		t.Fatalf("expected legacy sync token to still authenticate with no pepper configured: %v", err)
	}
	if principal.Source != auth.PrincipalSourceLegacyEnvSync {
		t.Fatalf("expected legacy sync principal, got %+v", principal)
	}

	if _, err := runtimeAuth.ResolveBearerToken(context.Background(), "egc_live_deadbeef_some-managed-looking-token"); !errors.Is(err, auth.ErrUnknownToken) {
		t.Fatalf("expected a managed-looking token to be rejected as unknown (not mis-resolved) when managed auth is disabled, got %v", err)
	}
}
