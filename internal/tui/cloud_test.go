package tui

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloudconfig"
)

func writeCloudJSON(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "cloud.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write cloud.json: %v", err)
	}
}

// saveTUIServerURL tests — the helper that writes the server URL into
// cloud.json while preserving any pre-existing Token field. Replaces
// the forked saveCloudConfig deleted in T-608.16.

func TestSaveTUIServerURLPreservesToken(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"server_url":"https://old.example.com","token":"file-token"}`)

	if err := saveTUIServerURL(dir, "https://new.example.com"); err != nil {
		t.Fatalf("saveTUIServerURL: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "cloud.json"))
	if err != nil {
		t.Fatalf("read cloud.json: %v", err)
	}
	if !bytes.Contains(b, []byte(`"server_url":"https://new.example.com"`)) {
		t.Fatalf("server_url not updated in %s", string(b))
	}
	if !bytes.Contains(b, []byte(`"token":"file-token"`)) {
		t.Fatalf("token was not preserved in %s", string(b))
	}
}

func TestSaveTUIServerURLOnEmptyDirWritesOnlyURL(t *testing.T) {
	dir := t.TempDir()

	if err := saveTUIServerURL(dir, "https://new.example.com"); err != nil {
		t.Fatalf("saveTUIServerURL: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "cloud.json"))
	if err != nil {
		t.Fatalf("read cloud.json: %v", err)
	}
	if !bytes.Contains(b, []byte(`"server_url":"https://new.example.com"`)) {
		t.Fatalf("server_url not written in %s", string(b))
	}
}

func TestSaveTUIServerURLPropagatesMalformedError(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{not valid json`)

	err := saveTUIServerURL(dir, "https://new.example.com")
	if err == nil {
		t.Fatal("expected error from malformed file")
	}
}

func httpHandlerWithStatus(code int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	})
}

type fakePingTransport struct {
	statusCode int
	err        error
}

func (f *fakePingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.statusCode,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Request:    req,
		Header:     make(http.Header),
	}, nil
}

func TestPingCloudServerReachable(t *testing.T) {
	orig := pingCloudTransport
	pingCloudTransport = &fakePingTransport{statusCode: http.StatusOK}
	defer func() { pingCloudTransport = orig }()

	msg := pingCloudServer("https://cloud.example.com", "token")().(cloudPingMsg)
	if msg.status != "reachable" {
		t.Fatalf("status = %q, want reachable", msg.status)
	}
	if msg.err != nil {
		t.Fatalf("unexpected err: %v", msg.err)
	}
}

func TestPingCloudServerUnauthorized(t *testing.T) {
	orig := pingCloudTransport
	pingCloudTransport = &fakePingTransport{statusCode: http.StatusUnauthorized}
	defer func() { pingCloudTransport = orig }()

	msg := pingCloudServer("https://cloud.example.com", "token")().(cloudPingMsg)
	if msg.status != "unauthorized" {
		t.Fatalf("status = %q, want unauthorized", msg.status)
	}
}

func TestPingCloudServerUnreachable(t *testing.T) {
	orig := pingCloudTransport
	pingCloudTransport = &fakePingTransport{err: errors.New("connection refused")}
	defer func() { pingCloudTransport = orig }()

	msg := pingCloudServer("https://cloud.example.com", "token")().(cloudPingMsg)
	if msg.status != "unreachable" {
		t.Fatalf("status = %q, want unreachable", msg.status)
	}
	if msg.err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestPingCloudServer5xxIsUnreachable(t *testing.T) {
	orig := pingCloudTransport
	pingCloudTransport = &fakePingTransport{statusCode: http.StatusServiceUnavailable}
	defer func() { pingCloudTransport = orig }()

	msg := pingCloudServer("https://cloud.example.com", "token")().(cloudPingMsg)
	if msg.status != "unreachable" {
		t.Fatalf("status = %q, want unreachable", msg.status)
	}
}

func TestPingCloudServerMalformedURL(t *testing.T) {
	orig := pingCloudTransport
	pingCloudTransport = &fakePingTransport{}
	defer func() { pingCloudTransport = orig }()

	msg := pingCloudServer("not a url", "token")().(cloudPingMsg)
	if msg.status != "unreachable" {
		t.Fatalf("status = %q, want unreachable", msg.status)
	}
}

// validateCloudServerURL is a thin shim around cloudconfig.ValidateServerURL
// (T-608.17 will delete the shim). These tests pin the shim's behavior so
// the migration is auditable.

func TestValidateCloudServerURLShim(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"https ok", "https://cloud.example.com", "https://cloud.example.com", false},
		{"missing scheme", "cloud.example.com", "", true},
		{"bad scheme", "ftp://cloud.example.com", "", true},
		{"missing host", "https://", "", true},
		{"http ok", "http://cloud.example.com", "http://cloud.example.com", false},
		{"empty", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateCloudServerURL(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateCloudServerURL(%q) err = %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("validateCloudServerURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// tuiCloudConfigForUI tests — the helper that reads cloudconfig + applies
// the ENGRAM_CLOUD_SERVER override (T-608.16).
//
// The bug being fixed: the old effectiveCloudToken / loadCloudConfigCmd in
// the TUI did not honor ENGRAM_CLOUD_SERVER, so the TUI's Cloud Config
// form could display a stale server URL after the user set the env var.
// These tests pin the precedence: env > file > zero-value, with
// whitespace-only env treated as unset.

func TestTuiCloudConfigForUIAppliesEnvServerOverride(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"server_url":"https://file.example.com","token":"file-token"}`)
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://env.example.com")

	cfg := tuiCloudConfigForUI(dir)
	if cfg.ServerURL != "https://env.example.com" {
		t.Fatalf("env should override file, got ServerURL=%q", cfg.ServerURL)
	}
	if cfg.Token != "file-token" {
		t.Fatalf("token field from file should be preserved, got Token=%q", cfg.Token)
	}
}

func TestTuiCloudConfigForUIFallsBackToFileWhenEnvUnset(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"server_url":"https://file.example.com","token":"file-token"}`)
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	cfg := tuiCloudConfigForUI(dir)
	if cfg.ServerURL != "https://file.example.com" {
		t.Fatalf("file should win when env empty, got ServerURL=%q", cfg.ServerURL)
	}
}

func TestTuiCloudConfigForUIWhitespaceEnvTreatedAsUnset(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"server_url":"https://file.example.com","token":"file-token"}`)
	t.Setenv("ENGRAM_CLOUD_SERVER", "   \t  ")

	cfg := tuiCloudConfigForUI(dir)
	if cfg.ServerURL != "https://file.example.com" {
		t.Fatalf("whitespace-only env should be unset; file should win, got ServerURL=%q", cfg.ServerURL)
	}
}

func TestTuiCloudConfigForUINoFileNoEnvReturnsZeroValue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	cfg := tuiCloudConfigForUI(dir)
	if cfg.ServerURL != "" || cfg.Token != "" {
		t.Fatalf("expected zero-value, got %+v", cfg)
	}
}

func TestTuiCloudConfigForUIMalformedFileReturnsZeroValue(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{not valid json`)
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	cfg := tuiCloudConfigForUI(dir)
	if cfg.ServerURL != "" || cfg.Token != "" {
		t.Fatalf("malformed file should yield zero-value, got %+v", cfg)
	}
}

func TestTuiCloudConfigForUIEnvOnlyReturnsConfigWithURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://env.example.com")

	cfg := tuiCloudConfigForUI(dir)
	if cfg.ServerURL != "https://env.example.com" {
		t.Fatalf("env should populate URL when no file present, got ServerURL=%q", cfg.ServerURL)
	}
}

// loadCloudConfigCmd tests — the message contract after T-608.16.
// The message must carry the effective server URL (env override applied)
// and the canonical token source label (from cloudconfig.SourceLabel).

func TestLoadCloudConfigCmdAppliesEnvServerOverride(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"server_url":"https://file.example.com","token":"file-token"}`)
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://env.example.com")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	msg := loadCloudConfigCmd(dir)()
	loaded, ok := msg.(cloudConfigLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T", msg)
	}
	if loaded.err != nil {
		t.Fatalf("unexpected error: %v", loaded.err)
	}
	if loaded.serverURL != "https://env.example.com" {
		t.Fatalf("serverURL = %q, want env override", loaded.serverURL)
	}
	if loaded.tokenSource != cloudconfig.LabelSourceFile {
		t.Fatalf("tokenSource = %q, want %q (cloudconfig label)", loaded.tokenSource, cloudconfig.LabelSourceFile)
	}
}

func TestLoadCloudConfigCmdReportsEnvTokenSource(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"server_url":"https://file.example.com","token":"file-token"}`)
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "env-token")

	msg := loadCloudConfigCmd(dir)()
	loaded, ok := msg.(cloudConfigLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T", msg)
	}
	if loaded.err != nil {
		t.Fatalf("unexpected error: %v", loaded.err)
	}
	if loaded.tokenSource != cloudconfig.LabelSourceEnv {
		t.Fatalf("tokenSource = %q, want %q (cloudconfig label)", loaded.tokenSource, cloudconfig.LabelSourceEnv)
	}
}

func TestLoadCloudConfigCmdReportsNoneSourceWhenNoToken(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"server_url":"https://file.example.com"}`)
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	msg := loadCloudConfigCmd(dir)()
	loaded, ok := msg.(cloudConfigLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T", msg)
	}
	if loaded.err != nil {
		t.Fatalf("unexpected error: %v", loaded.err)
	}
	if loaded.tokenSource != cloudconfig.LabelSourceNone {
		t.Fatalf("tokenSource = %q, want %q (cloudconfig label)", loaded.tokenSource, cloudconfig.LabelSourceNone)
	}
}


