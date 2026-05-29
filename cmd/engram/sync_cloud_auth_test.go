package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/autosync"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// TestResolveCloudRuntimeConfigFallsBackToFileToken asserts that
// resolveCloudRuntimeConfig uses the token stored in cloud.json when
// ENGRAM_CLOUD_TOKEN is not set in the environment (issue #343).
func TestResolveCloudRuntimeConfigFallsBackToFileToken(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	const fileToken = "file-token-from-cloud-json"
	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "https://cloud.example.test",
		Token:     fileToken,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("resolveCloudRuntimeConfig: %v", err)
	}
	if cc.Token != fileToken {
		t.Fatalf("expected token %q from cloud.json fallback, got %q (ENGRAM_CLOUD_TOKEN not set)", fileToken, cc.Token)
	}
}

// TestResolveCloudRuntimeConfigEnvTokenTakesPrecedence asserts that when both
// ENGRAM_CLOUD_TOKEN and a token in cloud.json are present, the env var wins.
func TestResolveCloudRuntimeConfigEnvTokenTakesPrecedence(t *testing.T) {
	cfg := testConfig(t)
	const envToken = "env-token"
	const fileToken = "file-token"
	t.Setenv("ENGRAM_CLOUD_TOKEN", envToken)
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "https://cloud.example.test",
		Token:     fileToken,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("resolveCloudRuntimeConfig: %v", err)
	}
	if cc.Token != envToken {
		t.Fatalf("expected env token %q to take precedence over file token %q, got %q", envToken, fileToken, cc.Token)
	}
}

// TestSyncCloudSendsAuthorizationHeaderFromFileToken is an end-to-end test that
// verifies sync --cloud attaches the Authorization: Bearer header when the token
// is sourced from cloud.json and ENGRAM_CLOUD_TOKEN is not set (issue #343).
func TestSyncCloudSendsAuthorizationHeaderFromFileToken(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	const fileToken = "secret-file-token"

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sync/pull":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":1,"chunks":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/sync/push":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := testConfig(t)

	// Persist token in cloud.json; do NOT set ENGRAM_CLOUD_TOKEN.
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: srv.URL,
		Token:     fileToken,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.EnrollProject("demo"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	_ = s.Close()

	withArgs(t, "engram", "sync", "--cloud", "--project", "demo")
	_, _, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })

	if _, ok := recovered.(exitCode); ok {
		t.Fatal("sync --cloud fataled; expected success with file token")
	}

	wantAuth := "Bearer " + fileToken
	if !strings.EqualFold(gotAuth, wantAuth) {
		t.Fatalf("expected Authorization header %q, got %q (file token not forwarded)", wantAuth, gotAuth)
	}
}

// TestTryStartAutosyncUsesFileToken asserts that tryStartAutosync picks up the
// cloud token from cloud.json when ENGRAM_CLOUD_TOKEN env var is absent (issue #421).
// This is the Windows Task Scheduler scenario: the background process runs in a
// separate session context without the env var, so the token must come from the
// persisted config file.
func TestTryStartAutosyncUsesFileToken(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")  // env var absent — must fall back to file
	t.Setenv("ENGRAM_CLOUD_SERVER", "") // env var absent — server from file too

	const fileToken = "file-only-token-421"
	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "http://127.0.0.1:19998",
		Token:     fileToken,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	// Track whether the manager factory was reached.
	managerCreated := false
	old := newAutosyncManager
	newAutosyncManager = func(_ *store.Store, _ autosync.CloudTransport, _ autosync.Config) startableAutosyncManager {
		managerCreated = true
		return &fakeStartableManager{}
	}
	defer func() { newAutosyncManager = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, stopFn := tryStartAutosync(ctx, s, cfg)

	// If the file-token fallback is missing, tryStartAutosync returns (nil, nil)
	// because cc.Token is empty after resolveCloudRuntimeConfig ignores the file.
	if mgr == nil {
		t.Fatal("tryStartAutosync returned nil manager when token is only in cloud.json — file token fallback not working for autosync startup (issue #421)")
	}
	if stopFn == nil {
		t.Fatal("expected non-nil stop function when manager starts successfully")
	}
	if !managerCreated {
		t.Fatal("newAutosyncManager factory was never reached — tryStartAutosync aborted before creating manager")
	}
	stopFn()
}
