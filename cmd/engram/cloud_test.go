package main

import (
	"strings"
	"testing"

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
