package main

import (
	"strings"
	"testing"

	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// runCloudUpgradeDoctor is the shared body for the
// TestCloudUpgradeDoctor* family. It:
//
//  1. stubs exit/runtime hooks so the command's fatal() and
//     subprocesses don't crash the test process;
//  2. pins the env vars to empty so resolveCloudRuntimeConfig
//     uses the on-disk cloud.json (not the parent process env);
//  3. opens a fresh store, enrolls "my-project", and closes
//     the store so the upgrade doctor finds the project
//     enrolled (otherwise the "not enrolled" branch masks
//     the cloudConfigured signal we're testing);
//  4. writes a cloud.json with the supplied serverURL;
//  5. invokes cmdCloudUpgradeDoctor and captures stdout/stderr.
//
// The function returns the captured stdout. The caller asserts
// the report content (the "cloud configuration is required"
// sentinel from the legacy rejection MUST NOT appear, and
// "ready for cloud bootstrap" MUST appear when cloud is
// configured and the project is enrolled).
func runCloudUpgradeDoctor(t *testing.T, serverURL string) string {
	t.Helper()

	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)

	// Pin the runtime config to the on-disk cloud.json (do not
	// let the parent process's env vars override it). With
	// these set to empty strings, resolveCloudRuntimeConfig's
	// env-override branch is skipped and the file's server_url
	// wins.
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	// Enroll the project so the only blocking reason is
	// cloudConfigured. Without enrollment, the report would be
	// "not enrolled" regardless of cloudConfigured, masking
	// the change we want to observe.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("my-project"); err != nil {
		_ = s.Close()
		t.Fatalf("EnrollProject: %v", err)
	}
	_ = s.Close()

	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: serverURL,
		Token:     "t",
	}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	withArgs(t, "engram", "cloud", "upgrade", "doctor", "--project", "my-project")

	stdout, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdCloudUpgradeDoctor(cfg)
	})
	if recovered != nil || stderr != "" {
		t.Fatalf("doctor should complete without panic, panic=%v stderr=%q", recovered, stderr)
	}
	return stdout
}

// assertCloudUpgradeDoctorReady pins the contract that the
// upgrade doctor must report "ready" when cloud is configured
// (i.e., the URL was accepted by the validator) and the project
// is enrolled. The single sentinel we forbid is the
// "cloud configuration is required" message that the legacy
// validator produced by rejecting ?q=1/#frag — its absence
// is the proof the URL was accepted and cleared.
func assertCloudUpgradeDoctorReady(t *testing.T, url, stdout string) {
	t.Helper()
	if strings.Contains(stdout, "cloud configuration is required") {
		t.Fatalf("URL %q should be accepted and cleared, but doctor reported cloud as unconfigured: %q", url, stdout)
	}
	if !strings.Contains(stdout, "ready for cloud bootstrap") {
		t.Fatalf("URL %q: expected ready status, got: %q", url, stdout)
	}
	if !strings.Contains(stdout, "status: ready") {
		t.Fatalf("URL %q: expected 'status: ready' in doctor output, got: %q", url, stdout)
	}
}

// TestCloudUpgradeDoctorAcceptsQueryAndFragment pins the spec
// REQ-1 behavior change at the call site
// cmd/engram/cloud.go:342: a cloud.json URL containing a query
// (?q=1) and/or fragment (#frag) must be ACCEPTED and cleared by
// the URL validator, not REJECTED as the legacy
// validateCloudServerURL did.
//
// The contract under test:
//
//	validateCloudServerURL("https://cloud.example.test/?q=1#frag")
//	  OLD: returns ("", error("query is not allowed"))  -> cloudConfigured = false
//	  NEW: returns ("https://cloud.example.test/", nil) -> cloudConfigured = true
//
// DiagnoseCloudUpgrade receives cloudConfigured=true (with the
// project enrolled) and reports the doctor as "ready for cloud
// bootstrap". The legacy rejection produced "cloud configuration
// is required before upgrade bootstrap". The test asserts the
// NEW behavior (the legacy message MUST NOT appear) and pins the
// full chain: cloud.json -> resolveCloudRuntimeConfig -> line 342
// -> DiagnoseCloudUpgrade -> report status.
func TestCloudUpgradeDoctorAcceptsQueryAndFragment(t *testing.T) {
	stdout := runCloudUpgradeDoctor(t, "https://cloud.example.test/?q=1#frag")
	assertCloudUpgradeDoctorReady(t, "https://cloud.example.test/?q=1#frag", stdout)
}

// TestCloudUpgradeDoctorAcceptsCleanURL guards the existing happy
// path: a clean URL (no query, no fragment) must still be
// accepted after the migration. This is the regression test for
// the pre-existing call-site behavior; it would pass with BOTH
// the legacy and the new validator, but it stays in the slice
// to document the contract.
func TestCloudUpgradeDoctorAcceptsCleanURL(t *testing.T) {
	stdout := runCloudUpgradeDoctor(t, "https://cloud.example.test/")
	assertCloudUpgradeDoctorReady(t, "https://cloud.example.test/", stdout)
}

// TestCloudUpgradeDoctorURLAcceptanceMatrix is a TRIANGULATE
// table that exercises the call site cmd/engram/cloud.go:342
// against the broader URL matrix the spec REQ-1 promises: only
// a query, only a fragment, both, a path+query, and a port. All
// of these are accepted by cloudconfig.ValidateServerURL and
// would have been REJECTED by the legacy validateCloudServerURL.
//
// The table reads each row's URL, writes it to cloud.json, runs
// cmdCloudUpgradeDoctor, and asserts the doctor reports
// "ready" (because cloudConfigured=true and the project is
// enrolled). The legacy rejection produced "cloud configuration
// is required"; the migrated function must NOT produce that
// message for any of these rows.
func TestCloudUpgradeDoctorURLAcceptanceMatrix(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{name: "query only", url: "https://cloud.example.test/?q=1"},
		{name: "fragment only", url: "https://cloud.example.test/#frag"},
		{name: "query and fragment", url: "https://cloud.example.test/?q=1#frag"},
		{name: "path with query", url: "https://cloud.example.test/api/v1?token=abc"},
		{name: "port with query", url: "https://cloud.example.test:8443/?q=1"},
		{name: "path with fragment", url: "https://cloud.example.test/api/v1#section"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			stdout := runCloudUpgradeDoctor(t, tc.url)
			assertCloudUpgradeDoctorReady(t, tc.url, stdout)
		})
	}
}

// runCloudUpgradeBootstrap is the shared body for the
// TestCloudUpgradeBootstrap* family. It:
//
//  1. stubs exit/runtime hooks so the command's fatal() and
//     subprocesses don't crash the test process;
//  2. pins the env vars to empty so resolveCloudRuntimeConfig
//     uses the on-disk cloud.json (not the parent process env);
//  3. opens a fresh store, enrolls "my-project", and closes
//     the store so the snapshot/legacy-mutation checks pass
//     (otherwise the "not enrolled" branch masks the
//     cloudConfigured signal we're testing);
//  4. writes a cloud.json with the supplied serverURL;
//  5. stubs runUpgradeBootstrap (a package-level var seam) to
//     a deterministic no-op result so the test does not make
//     real HTTP calls;
//  6. invokes cmdCloudUpgradeBootstrap and captures stdout.
//
// The function returns the captured stdout. The caller asserts
// the URL was accepted (no panic from the exit stub) and the
// function reached the print stage (stdout contains
// "project: my-project" and "stage: test-stage").
func runCloudUpgradeBootstrap(t *testing.T, serverURL string) string {
	t.Helper()

	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)

	// Pin the runtime config to the on-disk cloud.json (do not
	// let the parent process's env vars override it). With
	// these set to empty strings, resolveCloudRuntimeConfig's
	// env-override branch is skipped and the file's server_url
	// wins.
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	// Enroll the project so the snapshot/legacy-mutation checks
	// pass. Without enrollment, the snapshot function would
	// still succeed, but the legacy-mutation diagnosis would be
	// the next blocking branch we don't want to exercise here.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("my-project"); err != nil {
		_ = s.Close()
		t.Fatalf("EnrollProject: %v", err)
	}
	_ = s.Close()

	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: serverURL,
		Token:     "t",
	}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	// Stub the HTTP bootstrap call so the test does not hit a
	// real cloud server. The var is restored in the cleanup.
	origRunUpgradeBootstrap := runUpgradeBootstrap
	t.Cleanup(func() { runUpgradeBootstrap = origRunUpgradeBootstrap })
	runUpgradeBootstrap = func(_ *store.Store, _ string, _ *cloudConfig) (*engramsync.UpgradeBootstrapResult, error) {
		return &engramsync.UpgradeBootstrapResult{
			Project: "my-project",
			Stage:   "test-stage",
			Resumed: false,
			NoOp:    true,
		}, nil
	}

	withArgs(t, "engram", "cloud", "upgrade", "bootstrap", "--project", "my-project")

	stdout, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdCloudUpgradeBootstrap(cfg)
	})
	if recovered != nil || stderr != "" {
		t.Fatalf("bootstrap should complete without panic, panic=%v stderr=%q", recovered, stderr)
	}
	return stdout
}

// assertCloudUpgradeBootstrapAccepted pins the contract that the
// bootstrap must reach the print stage when the URL was accepted
// by the validator. The print stage is the proof that the
// function passed URL validation, snapshot capture, legacy
// mutation diagnosis, and the stubbed runUpgradeBootstrap.
//
// Note: the legacy rejection path surfaces via the exit stub's
// panic, which is caught by captureOutputAndRecover and surfaces
// as the test's `recovered` value. The helper additionally
// guards against any future change that routes the URL error to
// stderr instead of the exit path.
func assertCloudUpgradeBootstrapAccepted(t *testing.T, url, stdout string) {
	t.Helper()
	if !strings.Contains(stdout, "project: my-project") {
		t.Fatalf("URL %q: expected 'project: my-project' in bootstrap output, got: %q", url, stdout)
	}
	if !strings.Contains(stdout, "stage: test-stage") {
		t.Fatalf("URL %q: expected 'stage: test-stage' in bootstrap output (stub ran), got: %q", url, stdout)
	}
	if !strings.Contains(stdout, "resumed: false") {
		t.Fatalf("URL %q: expected 'resumed: false' in bootstrap output, got: %q", url, stdout)
	}
	if !strings.Contains(stdout, "noop: true") {
		t.Fatalf("URL %q: expected 'noop: true' in bootstrap output, got: %q", url, stdout)
	}
}

// TestCloudUpgradeBootstrapAcceptsQueryAndFragment pins the spec
// REQ-1 behavior change at the call site
// cmd/engram/cloud.go:496: a cloud.json URL containing a query
// (?q=1) and/or fragment (#frag) must be ACCEPTED and cleared by
// the URL validator, not REJECTED as the legacy
// validateCloudServerURL did.
//
// The contract under test:
//
//	validateCloudServerURL("https://cloud.example.test/?q=1#frag")
//	  OLD: returns ("", error("query is not allowed"))  -> fatal("invalid cloud runtime server URL: query is not allowed")
//	  NEW: returns ("https://cloud.example.test/", nil) -> proceed to snapshot/legacy/bootstrap
//
// cmdCloudUpgradeBootstrap receives a clean URL and proceeds to
// the print stage, emitting "project: my-project" via the
// stubbed runUpgradeBootstrap. The legacy rejection produced a
// fatal panic before any output was printed. The test asserts
// the NEW behavior (the print stage was reached) and pins the
// full chain: cloud.json -> resolveCloudRuntimeConfig -> line 496
// -> snapshot/legacy/bootstrap -> print.
func TestCloudUpgradeBootstrapAcceptsQueryAndFragment(t *testing.T) {
	stdout := runCloudUpgradeBootstrap(t, "https://cloud.example.test/?q=1#frag")
	assertCloudUpgradeBootstrapAccepted(t, "https://cloud.example.test/?q=1#frag", stdout)
}

// TestCloudUpgradeBootstrapAcceptsCleanURL guards the existing
// happy path: a clean URL (no query, no fragment) must still be
// accepted after the migration. This is the regression test for
// the pre-existing call-site behavior; it would pass with BOTH
// the legacy and the new validator, but it stays in the slice
// to document the contract.
func TestCloudUpgradeBootstrapAcceptsCleanURL(t *testing.T) {
	stdout := runCloudUpgradeBootstrap(t, "https://cloud.example.test/")
	assertCloudUpgradeBootstrapAccepted(t, "https://cloud.example.test/", stdout)
}

// TestCloudUpgradeBootstrapURLAcceptanceMatrix is a TRIANGULATE
// table that exercises the call site cmd/engram/cloud.go:496
// against the broader URL matrix the spec REQ-1 promises: only
// a query, only a fragment, both, a path+query, and a port. All
// of these are accepted by cloudconfig.ValidateServerURL and
// would have been REJECTED by the legacy validateCloudServerURL.
//
// The table reads each row's URL, writes it to cloud.json, runs
// cmdCloudUpgradeBootstrap, and asserts the bootstrap reached
// the print stage (project: my-project). The legacy rejection
// produced a fatal panic before any output; the migrated
// function must reach the print stage for all of these rows.
func TestCloudUpgradeBootstrapURLAcceptanceMatrix(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{name: "query only", url: "https://cloud.example.test/?q=1"},
		{name: "fragment only", url: "https://cloud.example.test/#frag"},
		{name: "query and fragment", url: "https://cloud.example.test/?q=1#frag"},
		{name: "path with query", url: "https://cloud.example.test/api/v1?token=abc"},
		{name: "port with query", url: "https://cloud.example.test:8443/?q=1"},
		{name: "path with fragment", url: "https://cloud.example.test/api/v1#section"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			stdout := runCloudUpgradeBootstrap(t, tc.url)
			assertCloudUpgradeBootstrapAccepted(t, tc.url, stdout)
		})
	}
}

// TestCloudUpgradeBootstrapEarlyReturnOnEmptyServerURL pins the
// early-return guard at cmd/engram/cloud.go:492: when
// cc == nil || cc.ServerURL == "", the function must return
// with a fatal "cloud upgrade bootstrap requires configured
// cloud server" error and MUST NOT call the URL validator.
//
// The migration from validateCloudServerURL to
// cloudconfig.ValidateServerURL must NOT change this guard.
// The test writes no cloud.json, so resolveCloudRuntimeConfig
// returns cc with ServerURL="" (from the new Load's zero-value
// behavior per T-608.1), and the guard fires before the
// validator is called.
//
// The test asserts:
//   - the function panics (via the exit stub) — the guard's
//     fatal() call triggers the exit stub, which panics;
//   - stderr contains the guard's error message
//     "cloud upgrade bootstrap requires configured cloud server";
//   - stderr does NOT contain "invalid cloud runtime server URL"
//     (which would mean the validator ran with an empty URL).
//
// The exit stub panics with the exit code (an int), so the
// recovered panic value is not the error message. The error
// message is printed to stderr by the fatal() helper before the
// exit call.
func TestCloudUpgradeBootstrapEarlyReturnOnEmptyServerURL(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)

	// Pin env to empty so resolveCloudRuntimeConfig uses the
	// file (which does not exist, so cc.ServerURL="" via the
	// new Load's zero-value).
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	// Do NOT write cloud.json — cc.ServerURL must be empty so
	// the guard fires.

	withArgs(t, "engram", "cloud", "upgrade", "bootstrap", "--project", "my-project")

	_, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdCloudUpgradeBootstrap(cfg)
	})
	if recovered == nil {
		t.Fatal("expected fatal panic from early-return guard, got no panic")
	}
	if !strings.Contains(stderr, "cloud upgrade bootstrap requires configured cloud server") {
		t.Fatalf("expected early-return guard message in stderr, got: %q", stderr)
	}
	if strings.Contains(stderr, "invalid cloud runtime server URL") {
		t.Fatalf("URL validator must not run when ServerURL is empty (got stderr: %q)", stderr)
	}
}
