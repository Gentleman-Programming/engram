package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/remote"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// ─── Test Helpers ────────────────────────────────────────────────────────────

func testCloudStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.New(store.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func captureCloudOutput(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	old := cloudWriter
	t.Cleanup(func() { cloudWriter = old })
	cloudWriter = buf
	return buf
}

func setCloudReader(t *testing.T, input string) {
	t.Helper()
	old := cloudReader
	t.Cleanup(func() { cloudReader = old })
	cloudReader = strings.NewReader(input)
}

func setTestArgs(t *testing.T, args ...string) {
	t.Helper()
	old := os.Args
	t.Cleanup(func() { os.Args = old })
	os.Args = args
}

func captureExitCode(t *testing.T) *int {
	t.Helper()
	code := -1
	old := exitFunc
	t.Cleanup(func() { exitFunc = old })
	exitFunc = func(c int) { code = c }
	return &code
}

// cloudAuthServer returns an httptest server that checks Authorization header
// and serves a healthy /api/v1/health endpoint.
func cloudAuthServer(t *testing.T, validKey string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+validKey {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/health":
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func saveTestCloudConfig(t *testing.T, s *store.Store, serverURL, apiKey string) {
	t.Helper()
	cfg := remote.CloudConfig{
		ServerURL:    serverURL,
		APIKey:       apiKey,
		Mode:         "local-sync",
		PushDebounce: 10 * time.Second,
		PullInterval: 120 * time.Second,
	}
	if err := remote.SaveToStore(s, cfg); err != nil {
		t.Fatal(err)
	}
}

// ─── T25: Enroll / Unenroll ─────────────────────────────────────────────────

func TestCloudEnroll(t *testing.T) {
	s := testCloudStore(t)
	buf := captureCloudOutput(t)
	setTestArgs(t, "engram", "cloud", "enroll", "myapp")

	cmdCloudEnroll(s)

	output := buf.String()
	if !strings.Contains(output, "Enrolled: myapp") {
		t.Errorf("expected output to contain 'Enrolled: myapp', got: %q", output)
	}

	enrolled, err := s.IsProjectEnrolled("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if !enrolled {
		t.Error("expected project to be enrolled after cmdCloudEnroll")
	}
}

func TestCloudEnroll_AlreadyEnrolled(t *testing.T) {
	s := testCloudStore(t)
	if err := s.EnrollProject("myapp"); err != nil {
		t.Fatal(err)
	}

	buf := captureCloudOutput(t)
	setTestArgs(t, "engram", "cloud", "enroll", "myapp")

	cmdCloudEnroll(s)

	output := buf.String()
	if !strings.Contains(output, "Already enrolled") {
		t.Errorf("expected output to contain 'Already enrolled', got: %q", output)
	}
}

func TestCloudUnenroll(t *testing.T) {
	s := testCloudStore(t)
	if err := s.EnrollProject("myapp"); err != nil {
		t.Fatal(err)
	}

	buf := captureCloudOutput(t)
	setTestArgs(t, "engram", "cloud", "unenroll", "myapp")

	cmdCloudUnenroll(s)

	output := buf.String()
	if !strings.Contains(output, "Unenrolled: myapp") {
		t.Errorf("expected output to contain 'Unenrolled: myapp', got: %q", output)
	}

	enrolled, err := s.IsProjectEnrolled("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if enrolled {
		t.Error("expected project to be unenrolled after cmdCloudUnenroll")
	}
}

func TestCloudUnenroll_NotEnrolled(t *testing.T) {
	s := testCloudStore(t)
	buf := captureCloudOutput(t)
	setTestArgs(t, "engram", "cloud", "unenroll", "nonexistent")

	cmdCloudUnenroll(s)

	output := buf.String()
	if !strings.Contains(output, "Not enrolled") {
		t.Errorf("expected output to contain 'Not enrolled', got: %q", output)
	}
}

// ─── T22: Setup ──────────────────────────────────────────────────────────────

func TestCloudSetup_ValidCredentials(t *testing.T) {
	srv := cloudAuthServer(t, "valid-key")
	s := testCloudStore(t)
	buf := captureCloudOutput(t)
	setCloudReader(t, srv.URL+"\nvalid-key\nlocal-sync\n")

	cmdCloudSetup(s)

	output := buf.String()
	if !strings.Contains(output, "Connected successfully") {
		t.Errorf("expected 'Connected successfully' in output, got: %q", output)
	}

	// Verify config was persisted.
	cfg, err := remote.LoadFromStore(s)
	if err != nil {
		t.Fatalf("expected config to be saved, got error: %v", err)
	}
	if cfg.ServerURL != srv.URL {
		t.Errorf("expected ServerURL=%q, got %q", srv.URL, cfg.ServerURL)
	}
	if cfg.APIKey != "valid-key" {
		t.Errorf("expected APIKey=valid-key, got %q", cfg.APIKey)
	}
	if cfg.Mode != "local-sync" {
		t.Errorf("expected Mode=local-sync, got %q", cfg.Mode)
	}
}

func TestCloudSetup_BadAPIKey(t *testing.T) {
	srv := cloudAuthServer(t, "valid-key")
	s := testCloudStore(t)
	buf := captureCloudOutput(t)
	exitCode := captureExitCode(t)
	setCloudReader(t, srv.URL+"\nbad-key\nlocal-sync\n")

	cmdCloudSetup(s)

	output := buf.String()
	if !strings.Contains(output, "Connection failed") {
		t.Errorf("expected 'Connection failed' in output, got: %q", output)
	}
	if *exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", *exitCode)
	}

	// Verify config was NOT saved.
	_, err := remote.LoadFromStore(s)
	if err == nil {
		t.Error("expected config NOT to be saved after failed setup")
	}
}

// ─── T23: Sync ───────────────────────────────────────────────────────────────

func TestCloudSync_OutputFormat(t *testing.T) {
	s := testCloudStore(t)
	saveTestCloudConfig(t, s, "http://example.com", "test-key")
	buf := captureCloudOutput(t)

	old := doCloudSync
	t.Cleanup(func() { doCloudSync = old })
	doCloudSync = func(_ *store.Store, _ remote.CloudConfig) (int, int, error) {
		return 3, 5, nil
	}

	cmdCloudSync(s)

	output := buf.String()
	if !strings.Contains(output, "pushed 3") {
		t.Errorf("expected 'pushed 3' in output, got: %q", output)
	}
	if !strings.Contains(output, "pulled 5") {
		t.Errorf("expected 'pulled 5' in output, got: %q", output)
	}
}

func TestCloudSync_NoConfig(t *testing.T) {
	s := testCloudStore(t)
	buf := captureCloudOutput(t)
	exitCode := captureExitCode(t)

	cmdCloudSync(s)

	output := buf.String()
	if !strings.Contains(output, "Cloud not configured") {
		t.Errorf("expected 'Cloud not configured' in output, got: %q", output)
	}
	if !strings.Contains(output, "engram cloud setup") {
		t.Errorf("expected 'engram cloud setup' hint in output, got: %q", output)
	}
	if *exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", *exitCode)
	}
}

// ─── T24: Status ─────────────────────────────────────────────────────────────

func TestCloudStatus_Healthy(t *testing.T) {
	srv := cloudAuthServer(t, "test-key")
	s := testCloudStore(t)
	saveTestCloudConfig(t, s, srv.URL, "test-key")
	buf := captureCloudOutput(t)

	cmdCloudStatus(s)

	output := buf.String()

	// All 6 fields must be present.
	for _, field := range []string{"Mode:", "Server:", "Health:", "Pending:", "Last sync:", "Projects:"} {
		if !strings.Contains(output, field) {
			t.Errorf("expected %q in output, got: %q", field, output)
		}
	}

	if !strings.Contains(output, "connected") {
		t.Errorf("expected 'connected' in health output, got: %q", output)
	}
}

func TestCloudStatus_Unreachable(t *testing.T) {
	// Start and immediately close server to get an unreachable URL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	unreachableURL := srv.URL
	srv.Close()

	s := testCloudStore(t)
	saveTestCloudConfig(t, s, unreachableURL, "test-key")
	buf := captureCloudOutput(t)

	cmdCloudStatus(s)

	output := buf.String()
	if !strings.Contains(output, "unreachable") {
		t.Errorf("expected 'unreachable' in output, got: %q", output)
	}

	// Other fields must still be displayed.
	for _, field := range []string{"Mode:", "Server:", "Pending:", "Last sync:", "Projects:"} {
		if !strings.Contains(output, field) {
			t.Errorf("expected %q in output even when unreachable, got: %q", field, output)
		}
	}
}
