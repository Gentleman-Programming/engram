package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud"
	"github.com/Gentleman-Programming/engram/v2/internal/cloud/cloudstore"
)

func TestCloudRuntimePersistsUnknownTokenRequestAuthAudit(t *testing.T) {
	testDSN := openIsolatedCloudRuntimeSchema(t)

	const legacySyncToken = "audit-integration-legacy-token"
	const unknownToken = "audit-integration-unknown-token"
	t.Setenv("ENGRAM_CLOUD_TOKEN", legacySyncToken)
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")

	runtime, err := newCloudRuntime(cloud.Config{
		DSN:              testDSN,
		JWTSecret:        "audit-integration-jwt-secret-32-bytes-plus",
		AllowedProjects:  []string{"audit-project"},
		TokenPepper:      "audit-integration-token-pepper-at-least-32-bytes",
		MaxPushBodyBytes: cloud.DefaultMaxPushBodyBytes,
	})
	if err != nil {
		t.Fatalf("newCloudRuntime: %v", err)
	}
	dcr, ok := runtime.(*defaultCloudRuntime)
	if !ok {
		t.Fatalf("expected *defaultCloudRuntime, got %T", runtime)
	}
	t.Cleanup(func() { _ = dcr.store.Close() })

	status, body := doBearerRequest(t, dcr.server.Handler(), http.MethodGet, "/sync/pull?project=audit-project", unknownToken)
	if status != http.StatusUnauthorized {
		t.Fatalf("unknown token status = %d, want %d body=%q", status, http.StatusUnauthorized, body)
	}
	if body != "unauthorized: unknown token\n" {
		t.Fatalf("unknown token 401 body = %q", body)
	}

	events, err := dcr.store.ListAuthAuditEvents(context.Background(), cloudstore.AuthAuditQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuthAuditEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("auth audit events = %d, want 1: %+v", len(events), events)
	}
	event := events[0]
	if event.ActorPrincipalID != "" || event.ActorSource != "request" || event.Project != "" || event.Action != "sync.auth" || event.Outcome != "denied" || event.ReasonCode != "unknown_token" {
		t.Fatalf("unexpected persisted unknown-token auth audit event: %+v", event)
	}
	if event.Metadata["source"] != "request" || strings.Contains(fmt.Sprintf("%+v", event), unknownToken) {
		t.Fatalf("unknown-token audit event must contain only safe metadata: %+v", event)
	}
}
