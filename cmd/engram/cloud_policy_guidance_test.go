package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v2/internal/cloud/remote"
	"github.com/Gentleman-Programming/engram/v2/internal/cloud/syncguidance"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v2/internal/sync"
)

func TestCloudStatusPolicyFailureAddsGuidanceWithoutMutatingState(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.EnrollProject("policy-project"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if _, err := s.GetSyncState(cloudTargetKeyForProject("policy-project")); err != nil {
		t.Fatalf("initialize sync state: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	statusErr := &remote.HTTPStatusError{Operation: "status", StatusCode: 403, Body: `sync denied for project "policy-project"`}
	originalStatus := syncStatus
	syncStatus = func(*engramsync.Syncer) (int, int, int, error) { return 0, 0, 0, statusErr }
	t.Cleanup(func() { syncStatus = originalStatus })
	withArgs(t, "engram", "sync", "--cloud", "--status", "--project", "policy-project")

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected status failure exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "ENGRAM_CLOUD_ALLOWED_PROJECTS") {
		t.Fatalf("status error lacks policy guidance: %q", stderr)
	}

	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s.Close()
	state, err := s.GetSyncState(cloudTargetKeyForProject("policy-project"))
	if err != nil {
		t.Fatalf("read sync state: %v", err)
	}
	if state.ReasonCode != nil || state.LastError != nil || state.ConsecutiveFailures != 0 {
		t.Fatalf("--status mutated sync state: %+v", state)
	}
}

func TestCloudStatusDiagnosticUsesProjectScopedPolicyGuidance(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	message := `cloud: push: status 403: sync denied for project "policy-project"` + "\n\n" + syncguidance.PolicyGuidance("policy-project")
	if err := s.MarkSyncFailureWithReason("cloud:policy-project", "policy_forbidden", message, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("mark project policy failure: %v", err)
	}
	if err := s.MarkSyncFailure("cloud", "legacy global failure", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("mark legacy failure: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	stdout, _ := captureOutput(t, func() { printCloudStatusSyncDiagnostic(cfg) })
	if !strings.Contains(stdout, "project-scoped cloud state") || !strings.Contains(stdout, message) {
		t.Fatalf("project-scoped diagnostic = %q", stdout)
	}
	if !strings.Contains(stdout, "reason_code: policy_forbidden") {
		t.Fatalf("project-scoped diagnostic lacks reason code: %q", stdout)
	}
}

func TestCloudStatusDiagnosticSuppressesLegacyGlobalState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reason  string
		message string
	}{
		{name: "policy forbidden", reason: constants.ReasonPolicyForbidden, message: "stale policy diagnostic"},
		{name: "transport failure", reason: constants.ReasonTransportFailed, message: "stale transport diagnostic"},
		{name: "uncoded", reason: "", message: "stale uncoded diagnostic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			s, err := store.New(cfg)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			if err := s.MarkSyncBlocked("cloud", tc.reason, tc.message); err != nil {
				t.Fatalf("mark legacy state: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("close store: %v", err)
			}

			stdout, _ := captureOutput(t, func() { printCloudStatusSyncDiagnostic(cfg) })
			if strings.TrimSpace(stdout) != "" {
				t.Fatalf("legacy diagnostic leaked into cloud status: %q", stdout)
			}
		})
	}
}
