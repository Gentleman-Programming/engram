package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloudconfig"
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

// ----------------------------------------------------------------------------
// T-608.8: cmdCloudConfig migration to cloudconfig
// ----------------------------------------------------------------------------

// runCloudConfig is the shared body for the TestCloudConfig* family.
// It:
//
//  1. stubs the exit hook so the command's exitFunc() calls do not
//     crash the test process (the exit stub panics with the exit
//     code, and captureOutputAndRecover catches it);
//  2. pins ENGRAM_CLOUD_SERVER and ENGRAM_CLOUD_TOKEN to empty so
//     resolveCloudRuntimeConfig is not on the call path (this test
//     only exercises the --server flag path);
//  3. sets up os.Args to "engram cloud config --server <url>";
//  4. invokes cmdCloudConfig with the supplied config and
//     captures stdout/stderr.
//
// The function returns the captured stdout. The caller asserts
// the URL was accepted (no panic), the success message contains
// the cleaned URL, and the cloud.json file is at the expected
// location with the expected mode.
func runCloudConfig(t *testing.T, serverURL string) (string, store.Config) {
	t.Helper()

	stubExitWithPanic(t)

	cfg := testConfig(t)

	// Pin env to empty so the command does not pick up
	// ENGRAM_CLOUD_SERVER / ENGRAM_CLOUD_TOKEN from the parent
	// test process's environment.
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	withArgs(t, "engram", "cloud", "config", "--server", serverURL)

	stdout, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdCloudConfig(cfg)
	})
	if recovered != nil || stderr != "" {
		t.Fatalf("config should complete without panic, panic=%v stderr=%q", recovered, stderr)
	}
	return stdout, cfg
}

// assertCloudConfigSaved pins the contract that cmdCloudConfig
// must, when the URL is accepted by the validator, write a
// cloud.json at cloudconfig.Path(cfg.DataDir) with mode 0o644,
// inside a directory with mode 0o755, and print the expected
// success line. The single sentinel we forbid is the legacy
// "error: invalid server URL" message — its absence is the
// proof the URL was accepted by the new validator.
func assertCloudConfigSaved(t *testing.T, cfg store.Config, inputURL, cleanedURL, stdout string) {
	t.Helper()
	if strings.Contains(stdout, "error: invalid server URL") {
		t.Fatalf("URL %q should be accepted and cleared, but config reported invalid URL: %q", inputURL, stdout)
	}
	expected := "✓ Cloud server set to " + cleanedURL
	if !strings.Contains(stdout, expected) {
		t.Fatalf("URL %q: expected %q in config output, got: %q", inputURL, expected, stdout)
	}

	path := cloudconfig.Path(cfg.DataDir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("URL %q: expected cloud.json at %s, stat: %v", inputURL, path, err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("URL %q: expected cloud.json mode 0o644, got %#o", inputURL, got)
	}

	dirInfo, err := os.Stat(cfg.DataDir)
	if err != nil {
		t.Fatalf("URL %q: expected DataDir at %s, stat: %v", inputURL, cfg.DataDir, err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o755 {
		t.Fatalf("URL %q: expected DataDir mode 0o755, got %#o", inputURL, got)
	}
}

// TestCloudConfigAcceptsQueryAndFragment pins the spec REQ-1
// behavior change at the call site cmd/engram/cloud.go:772
// (the validateCloudServerURL call inside cmdCloudConfig):
// a server URL containing a query (?q=1) and/or fragment
// (#frag) must be ACCEPTED and cleared by the URL validator,
// not REJECTED as the legacy validateCloudServerURL did.
//
// The contract under test:
//
//	validateCloudServerURL("https://cloud.example.test/?q=1#frag")
//	  OLD: returns ("", error("query is not allowed"))
//	       -> cmdCloudConfig calls exitFunc(1) (panics under test)
//	  NEW: returns ("https://cloud.example.test/", nil)
//	       -> cmdCloudConfig saves and prints the success line
//
// The test pins the full chain: os.Args -> cmdCloudConfig ->
// validator -> save -> print. The legacy rejection surfaced as
// a panic from the exit stub; the migrated function must reach
// the save+print stage.
func TestCloudConfigAcceptsQueryAndFragment(t *testing.T) {
	stdout, cfg := runCloudConfig(t, "https://cloud.example.test/?q=1#frag")
	assertCloudConfigSaved(t, cfg, "https://cloud.example.test/?q=1#frag", "https://cloud.example.test/", stdout)
}

// TestCloudConfigAcceptsCleanURL guards the existing happy
// path: a clean URL (no query, no fragment) must still be
// accepted after the migration. This is the regression test for
// the pre-existing call-site behavior; it would pass with BOTH
// the legacy and the new validator, but it stays in the slice
// to document the contract.
func TestCloudConfigAcceptsCleanURL(t *testing.T) {
	stdout, cfg := runCloudConfig(t, "https://cloud.example.test/")
	assertCloudConfigSaved(t, cfg, "https://cloud.example.test/", "https://cloud.example.test/", stdout)
}

// TestCloudConfigURLAcceptanceMatrix is a TRIANGULATE table
// that exercises the call site cmd/engram/cloud.go:772 against
// the broader URL matrix the spec REQ-1 promises: only a query,
// only a fragment, both, a path+query, a port+query, and a
// path+fragment. All of these are accepted by
// cloudconfig.ValidateServerURL and would have been REJECTED
// by the legacy validateCloudServerURL (the query cases, at
// least — the legacy fragment check is dead code because
// url.ParseRequestURI strips the fragment).
//
// The table reads each row's URL, runs cmdCloudConfig, and
// asserts the save+print contract: cloud.json exists at
// cloudconfig.Path with mode 0o644, and stdout contains the
// "✓ Cloud server set to <cleaned-url>" line.
func TestCloudConfigURLAcceptanceMatrix(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		cleaned string
	}{
		{name: "query only", input: "https://cloud.example.test/?q=1", cleaned: "https://cloud.example.test/"},
		{name: "fragment only", input: "https://cloud.example.test/#frag", cleaned: "https://cloud.example.test/"},
		{name: "query and fragment", input: "https://cloud.example.test/?q=1#frag", cleaned: "https://cloud.example.test/"},
		{name: "path with query", input: "https://cloud.example.test/api/v1?token=abc", cleaned: "https://cloud.example.test/api/v1"},
		{name: "port with query", input: "https://cloud.example.test:8443/?q=1", cleaned: "https://cloud.example.test:8443/"},
		{name: "path with fragment", input: "https://cloud.example.test/api/v1#section", cleaned: "https://cloud.example.test/api/v1"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			stdout, cfg := runCloudConfig(t, tc.input)
			assertCloudConfigSaved(t, cfg, tc.input, tc.cleaned, stdout)
		})
	}
}

// TestCloudConfigSavesToCloudconfigPath pins the migration
// shape: the save call inside cmdCloudConfig must produce a
// cloud.json at cloudconfig.Path(cfg.DataDir). This is
// functionally equivalent to the legacy cloudConfigPath(cfg),
// but the migration must use the new package's path function
// (per spec REQ-2). The test verifies the file is at the
// expected location and the mode is 0o644 per the T-608.1
// spec's file-mode guarantee.
func TestCloudConfigSavesToCloudconfigPath(t *testing.T) {
	stdout, cfg := runCloudConfig(t, "https://cloud.example.test/")

	path := cloudconfig.Path(cfg.DataDir)
	if !strings.HasSuffix(path, filepath.Join("cloud.json")) {
		t.Fatalf("expected path to end with cloud.json, got %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected cloud.json at %s, stat: %v", path, err)
	}
	if !strings.Contains(stdout, "✓ Cloud server set to https://cloud.example.test/") {
		t.Fatalf("expected success line, got: %q", stdout)
	}
}

// TestCloudConfigLoadAfterSaveRoundTrip is a TRIANGULATE test
// that verifies the save path produces JSON that is compatible
// with cloudconfig.Load. The local cmdCloudConfig writes a
// cloud.json via cloudconfig.Save (which uses
// json.Marshal of cloudconfig.Config). A subsequent
// cloudconfig.Load must read back the same fields.
//
// This guards against a regression where the migration
// accidentally passes a wrong type to cloudconfig.Save (e.g.,
// a non-pointer or a struct missing the JSON tags) and the
// file is written with an unexpected schema. If the schema
// drifts, the load would either fail to decode or read back
// empty fields.
func TestCloudConfigLoadAfterSaveRoundTrip(t *testing.T) {
	_, cfg := runCloudConfig(t, "https://cloud.example.test/")

	loaded, err := cloudconfig.Load(cfg.DataDir)
	if err != nil {
		t.Fatalf("cloudconfig.Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("cloudconfig.Load returned nil Config")
	}
	if loaded.ServerURL != "https://cloud.example.test/" {
		t.Fatalf("expected ServerURL to round-trip, got %q", loaded.ServerURL)
	}
	if loaded.Token != "" {
		t.Fatalf("expected empty Token, got %q", loaded.Token)
	}
}

// TestCloudConfigCreatesMissingDirectory is a TRIANGULATE
// test that pins the MkdirAll contract: when the data
// directory does not exist yet, cloudconfig.Save (called
// from cmdCloudConfig) must create it via os.MkdirAll(dataDir,
// 0o755). The legacy saveCloudConfig also did this; the
// migration must preserve it.
//
// The test sets cfg.DataDir to a subdirectory of t.TempDir()
// that does not exist yet, runs cmdCloudConfig, and asserts
// the directory was created with mode 0o755.
func TestCloudConfigCreatesMissingDirectory(t *testing.T) {
	stubExitWithPanic(t)

	parent := t.TempDir()
	cfg := testConfig(t)
	cfg.DataDir = filepath.Join(parent, "subdir", "data")

	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	withArgs(t, "engram", "cloud", "config", "--server", "https://cloud.example.test/")

	_, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdCloudConfig(cfg)
	})
	if recovered != nil || stderr != "" {
		t.Fatalf("config should complete without panic, panic=%v stderr=%q", recovered, stderr)
	}

	dirInfo, err := os.Stat(cfg.DataDir)
	if err != nil {
		t.Fatalf("expected DataDir to be created at %s, stat: %v", cfg.DataDir, err)
	}
	if !dirInfo.IsDir() {
		t.Fatalf("expected %s to be a directory", cfg.DataDir)
	}
	if got := dirInfo.Mode().Perm(); got != 0o755 {
		t.Fatalf("expected DataDir mode 0o755, got %#o", got)
	}

	path := cloudconfig.Path(cfg.DataDir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected cloud.json at %s, stat: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("expected cloud.json mode 0o644, got %#o", got)
	}
}

// TestCloudConfigNormalizesFileMode is a TRIANGULATE test
// that pins the os.Chmod contract from T-608.1: when
// cloudconfig.Save writes to a pre-existing file, it must
// chmod the file to 0o644 regardless of the prior mode. The
// legacy saveCloudConfig did NOT do this — it only set the
// mode on creation, leaving existing files at whatever mode
// they had.
//
// The test pre-creates cloud.json with mode 0o600 (intentionally
// wrong), runs cmdCloudConfig, and asserts the on-disk mode is
// 0o644 after the save.
func TestCloudConfigNormalizesFileMode(t *testing.T) {
	stubExitWithPanic(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	// Pre-create the data directory and a cloud.json with the
	// wrong mode (0o600 instead of 0o644). This is the state
	// a user would have if they saved via a buggy external
	// tool or a permission-resetting chmod.
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := cloudconfig.Path(cfg.DataDir)
	if err := os.WriteFile(path, []byte(`{"server_url":"https://old.example.test/","token":"old"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	withArgs(t, "engram", "cloud", "config", "--server", "https://cloud.example.test/")

	_, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdCloudConfig(cfg)
	})
	if recovered != nil || stderr != "" {
		t.Fatalf("config should complete without panic, panic=%v stderr=%q", recovered, stderr)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected cloud.json at %s, stat: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("expected cloud.json mode 0o644 (chmod normalized), got %#o", got)
	}

	// Verify the new URL was written, not the old one.
	loaded, err := cloudconfig.Load(cfg.DataDir)
	if err != nil {
		t.Fatalf("cloudconfig.Load: %v", err)
	}
	if loaded.ServerURL != "https://cloud.example.test/" {
		t.Fatalf("expected new ServerURL after save, got %q", loaded.ServerURL)
	}
}

// ----------------------------------------------------------------------------
// T-608.9: snapshot writeback migration to cloudconfig.Path
// ----------------------------------------------------------------------------

// runCloudUpgradeBootstrapWithCustomCloudJSON is the
// T-608.9-specific helper that builds on the T-608.7
// runCloudUpgradeBootstrap flow but writes a custom
// cloud.json (with potentially-sentinel data) instead of
// using saveCloudConfig to write a standard one. This
// lets the snapshot writeback test verify the raw-bytes
// contract: the snapshot.CloudConfigJSON must equal the
// exact bytes of the cloud.json file, including any
// sentinel data that json.Marshal would strip.
//
// The function returns the captured stdout, the test
// config (so callers can read the post-bootstrap store
// state), and the raw bytes that were written to
// cloud.json (so callers can compare the snapshot against
// them).
func runCloudUpgradeBootstrapWithCustomCloudJSON(t *testing.T, rawConfigJSON string) (string, store.Config, []byte) {
	t.Helper()

	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("my-project"); err != nil {
		_ = s.Close()
		t.Fatalf("EnrollProject: %v", err)
	}
	_ = s.Close()

	// Write the custom cloud.json (bypassing saveCloudConfig
	// so the test can include sentinel data that json.Marshal
	// would not produce).
	path := cloudconfig.Path(cfg.DataDir)
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(rawConfigJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

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
	return stdout, cfg, []byte(rawConfigJSON)
}

// loadSnapshotForProject opens the store and reads the
// cloud upgrade state for the test project. The snapshot
// is the contract under test: it must contain the raw
// bytes of cloud.json (not decoded JSON).
func loadSnapshotForProject(t *testing.T, cfg store.Config, project string) store.CloudUpgradeSnapshot {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	state, err := s.GetCloudUpgradeState(project)
	if err != nil {
		t.Fatalf("GetCloudUpgradeState: %v", err)
	}
	if state == nil {
		t.Fatal("expected cloud upgrade state to be saved by the bootstrap snapshot")
	}
	return state.Snapshot
}

// TestCloudUpgradeBootstrapSnapshotWritebackUsesRawBytes
// pins the spec REQ-2 contract at the call site
// cmd/engram/cloud.go:550: the snapshot writeback inside
// captureUpgradeSnapshotBeforeBootstrap must read cloud.json
// as RAW BYTES (via os.ReadFile), not via cloudconfig.Load
// (which would decode and drop unknown fields).
//
// The test writes a cloud.json with a sentinel "sentinel"
// field that cloudconfig.Config does not declare. If the
// snapshot used cloudconfig.Load + json.Marshal, the
// sentinel would be lost. If the snapshot uses
// os.ReadFile(cloudconfig.Path(...)), the sentinel is
// preserved verbatim.
//
// The test asserts:
//
//   - the snapshot.CloudConfigJSON contains the exact bytes
//     of the cloud.json file, including the sentinel;
//   - the snapshot.CloudConfigPresent is true.
//
// This is the critical contract: the snapshot is used to
// ROLLBACK the upgrade (T-608.9+), and the rollback writes
// the snapshot back to disk. If the snapshot were decoded
// JSON, the rollback would lose any data the original file
// had but cloudconfig.Config does not declare.
func TestCloudUpgradeBootstrapSnapshotWritebackUsesRawBytes(t *testing.T) {
	rawConfigJSON := `{"server_url":"https://cloud.example.test/","token":"t","sentinel":"fingerprint"}`

	_, cfg, rawBytes := runCloudUpgradeBootstrapWithCustomCloudJSON(t, rawConfigJSON)

	snapshot := loadSnapshotForProject(t, cfg, "my-project")
	if !snapshot.CloudConfigPresent {
		t.Fatal("expected snapshot.CloudConfigPresent=true, got false")
	}
	if snapshot.CloudConfigJSON != string(rawBytes) {
		t.Fatalf("snapshot must equal raw cloud.json bytes.\nwant: %q\ngot:  %q", string(rawBytes), snapshot.CloudConfigJSON)
	}
	// The sentinel must survive — this is the proof the
	// snapshot used os.ReadFile, not cloudconfig.Load.
	if !strings.Contains(snapshot.CloudConfigJSON, "sentinel") {
		t.Fatalf("snapshot must contain the sentinel field (proves raw bytes, not decoded): %q", snapshot.CloudConfigJSON)
	}
}

// TestCloudUpgradeBootstrapSnapshotWritebackPathMatchesCloudconfigPath
// pins the migration shape: the snapshot's source file
// must be at cloudconfig.Path(cfg.DataDir). The test
// verifies the snapshot's CloudConfigJSON equals the
// bytes of the file at cloudconfig.Path, which is the
// only file the snapshot is allowed to read.
//
// This is functionally equivalent to the legacy
// cloudConfigPath(cfg), but the migration must use the
// new package's path function (per spec REQ-2). The
// test pins the path identity by comparing the snapshot
// to the file's bytes.
func TestCloudUpgradeBootstrapSnapshotWritebackPathMatchesCloudconfigPath(t *testing.T) {
	rawConfigJSON := `{"server_url":"https://cloud.example.test/","token":"t"}`

	_, cfg, rawBytes := runCloudUpgradeBootstrapWithCustomCloudJSON(t, rawConfigJSON)

	path := cloudconfig.Path(cfg.DataDir)
	if !strings.HasSuffix(path, filepath.Join("cloud.json")) {
		t.Fatalf("expected path to end with cloud.json, got %q", path)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(onDisk) != string(rawBytes) {
		t.Fatalf("on-disk cloud.json should be the test's raw bytes.\nwant: %q\ngot:  %q", string(rawBytes), string(onDisk))
	}

	snapshot := loadSnapshotForProject(t, cfg, "my-project")
	if snapshot.CloudConfigJSON != string(onDisk) {
		t.Fatalf("snapshot must equal the on-disk cloud.json at cloudconfig.Path.\nwant: %q\ngot:  %q", string(onDisk), snapshot.CloudConfigJSON)
	}
}

// TestCloudUpgradeBootstrapSnapshotWritebackHandlesMissingFile
// is a TRIANGULATE test that pins the error handling:
// when cloud.json does not exist (e.g., the user has not
// run `engram cloud config` yet), the snapshot writeback
// must handle the os.ErrNotExist gracefully — the function
// returns nil, the snapshot.CloudConfigPresent is false,
// and the snapshot.CloudConfigJSON is empty.
//
// The test does NOT write a cloud.json. The bootstrap still
// runs (because the validator is called with cc.ServerURL
// from resolveCloudRuntimeConfig, which returns a zero-value
// ServerURL when no file exists). But the early-return
// guard at cmd/engram/cloud.go:492 fires first: when
// cc.ServerURL is empty, the function returns with
// "cloud upgrade bootstrap requires configured cloud server".
//
// To exercise the snapshot's missing-file path, the test
// must pass a valid URL but with no cloud.json. We use
// ENGRAM_CLOUD_SERVER to provide the URL at runtime, which
// the existing T-608.7 helper pins to empty. So the
// TRIANGULATE uses a different shape: write a cloud.json
// with only the token (no server_url) so cc.ServerURL
// is empty via the zero-value, but the file still has
// data the snapshot can read.
//
// Actually, the snapshot's missing-file branch is only
// reachable when cc.ServerURL is non-empty (otherwise the
// guard fires). So this test sets ENGRAM_CLOUD_SERVER to
// a valid URL and writes NO cloud.json. The runtime
// config picks up the env, passes validation, and reaches
// the snapshot function.
func TestCloudUpgradeBootstrapSnapshotWritebackHandlesMissingFile(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)

	// Provide the server URL via env so the bootstrap
	// function passes its guard. Do NOT write cloud.json —
	// the snapshot must observe an absent file and handle
	// the os.ErrNotExist gracefully.
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test/")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "t")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("my-project"); err != nil {
		_ = s.Close()
		t.Fatalf("EnrollProject: %v", err)
	}
	_ = s.Close()

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
	if !strings.Contains(stdout, "project: my-project") {
		t.Fatalf("expected bootstrap to reach print stage, got: %q", stdout)
	}

	snapshot := loadSnapshotForProject(t, cfg, "my-project")
	if snapshot.CloudConfigPresent {
		t.Fatalf("expected snapshot.CloudConfigPresent=false (no file), got true with JSON=%q", snapshot.CloudConfigJSON)
	}
	if strings.TrimSpace(snapshot.CloudConfigJSON) != "" {
		t.Fatalf("expected empty snapshot.CloudConfigJSON, got %q", snapshot.CloudConfigJSON)
	}
}
