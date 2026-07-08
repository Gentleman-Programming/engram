package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeCloudJSON(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "cloud.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write cloud.json: %v", err)
	}
}

func TestLoadCloudConfigReadsServerURLAndToken(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"server_url":"https://cloud.example.com","token":"file-token"}`)

	cc, err := loadCloudConfig(dir)
	if err != nil {
		t.Fatalf("loadCloudConfig: %v", err)
	}
	if cc.ServerURL != "https://cloud.example.com" {
		t.Fatalf("server_url = %q", cc.ServerURL)
	}
	if cc.Token != "file-token" {
		t.Fatalf("token = %q", cc.Token)
	}
}

func TestLoadCloudConfigMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()

	cc, err := loadCloudConfig(dir)
	if err != nil {
		t.Fatalf("loadCloudConfig: %v", err)
	}
	if cc.ServerURL != "" || cc.Token != "" {
		t.Fatalf("expected empty config, got %+v", cc)
	}
}

func TestTokenSourceEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"token":"file-token"}`)

	t.Setenv("ENGRAM_CLOUD_TOKEN", "env-token")
	if got := tokenSourceMessage(dir); got != TokenSourceEnv {
		t.Fatalf("source = %q, want %q", got, TokenSourceEnv)
	}
}

func TestTokenSourceFileFallback(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"token":"file-token"}`)

	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	if got := tokenSourceMessage(dir); got != TokenSourceFile {
		t.Fatalf("source = %q, want %q", got, TokenSourceFile)
	}
}

func TestTokenSourceNone(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	if got := tokenSourceMessage(dir); got != TokenSourceNone {
		t.Fatalf("source = %q, want %q", got, TokenSourceNone)
	}
}

func TestSaveCloudConfigWritesOnlyServerURLAndPreservesToken(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"server_url":"https://old.example.com","token":"file-token"}`)

	if err := saveCloudConfig(dir, "https://new.example.com"); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "cloud.json"))
	if err != nil {
		t.Fatalf("read cloud.json: %v", err)
	}
	if !bytes.Contains(b, []byte(`"server_url": "https://new.example.com"`)) {
		t.Fatalf("server_url not updated in %s", string(b))
	}
	if !bytes.Contains(b, []byte(`"token": "file-token"`)) {
		t.Fatalf("token was not preserved in %s", string(b))
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

func TestSaveCloudConfigDoesNotWriteToken(t *testing.T) {
	dir := t.TempDir()

	if err := saveCloudConfig(dir, "https://new.example.com"); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "cloud.json"))
	if err != nil {
		t.Fatalf("read cloud.json: %v", err)
	}
	if bytes.Contains(b, []byte(`"token"`)) {
		t.Fatalf("token must never be written by TUI, got %s", string(b))
	}
}

func TestValidateCloudServerURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"https ok", "https://cloud.example.com", "https://cloud.example.com", false},
		{"trims space", "  https://cloud.example.com  ", "https://cloud.example.com", false},
		{"missing scheme", "cloud.example.com", "", true},
		{"bad scheme", "ftp://cloud.example.com", "", true},
		{"missing host", "https://", "", true},
		{"query not allowed", "https://cloud.example.com?x=1", "", true},
		{"fragment not allowed", "https://cloud.example.com#x", "", true},
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

func TestEffectiveCloudTokenEnvWins(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"token":"file-token"}`)

	t.Setenv("ENGRAM_CLOUD_TOKEN", "env-token")
	if got := effectiveCloudToken(dir); got != "env-token" {
		t.Fatalf("token = %q, want %q", got, "env-token")
	}
}

func TestEffectiveCloudTokenFileFallback(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"token":"file-token"}`)

	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	if got := effectiveCloudToken(dir); got != "file-token" {
		t.Fatalf("token = %q, want %q", got, "file-token")
	}
}

func TestEffectiveCloudTokenNone(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	if got := effectiveCloudToken(dir); got != "" {
		t.Fatalf("token = %q, want empty", got)
	}
}

func TestEffectiveCloudTokenTrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"token":"  file-token  "}`)

	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	if got := effectiveCloudToken(dir); got != "file-token" {
		t.Fatalf("token = %q, want %q", got, "file-token")
	}

	t.Setenv("ENGRAM_CLOUD_TOKEN", "  env-token  ")
	if got := effectiveCloudToken(dir); got != "env-token" {
		t.Fatalf("env token = %q, want %q", got, "env-token")
	}
}

func TestSaveCloudConfigMissingFileCreatesNew(t *testing.T) {
	dir := t.TempDir()

	if err := saveCloudConfig(dir, "https://new.example.com"); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "cloud.json"))
	if err != nil {
		t.Fatalf("read cloud.json: %v", err)
	}
	if !bytes.Contains(b, []byte(`"server_url": "https://new.example.com"`)) {
		t.Fatalf("server_url not written in %s", string(b))
	}
	if bytes.Contains(b, []byte(`"token"`)) {
		t.Fatalf("token must not appear in fresh save, got %s", string(b))
	}
}

func TestSaveCloudConfigMalformedJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{not json`)

	if err := saveCloudConfig(dir, "https://new.example.com"); err == nil {
		t.Fatal("expected error for malformed cloud.json")
	}
}

func TestIsInsecureNoAuth(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want bool
	}{
		{"empty", "", false},
		{"1", "1", true},
		{"true", "true", true},
		{"True", "True", true},
		{"TRUE", "TRUE", true},
		{"yes", "yes", true},
		{"on", "on", true},
		{"0", "0", false},
		{"false", "false", false},
		{"no", "no", false},
		{"whitespace 1", "  1  ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", tt.env)
			if got := isInsecureNoAuth(); got != tt.want {
				t.Fatalf("isInsecureNoAuth() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveDaemonProbePort(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT", "")
		if got := resolveDaemonProbePort(); got != 7437 {
			t.Fatalf("port = %d, want 7437", got)
		}
	})
	t.Run("custom", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT", "9999")
		if got := resolveDaemonProbePort(); got != 9999 {
			t.Fatalf("port = %d, want 9999", got)
		}
	})
	t.Run("invalid falls back", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT", "not-a-number")
		if got := resolveDaemonProbePort(); got != 7437 {
			t.Fatalf("port = %d, want 7437", got)
		}
	})
	t.Run("out of range high falls back", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT", "65536")
		if got := resolveDaemonProbePort(); got != 7437 {
			t.Fatalf("port = %d, want 7437", got)
		}
	})
	t.Run("zero falls back", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT", "0")
		if got := resolveDaemonProbePort(); got != 7437 {
			t.Fatalf("port = %d, want 7437", got)
		}
	})
}

func TestProbeLocalDaemonRunning(t *testing.T) {
	server := httptest.NewServer(httpHandlerWithStatus(http.StatusOK))
	defer server.Close()

	port := server.Listener.Addr().(*net.TCPAddr).Port
	res := probeLocalDaemon(context.Background(), port)
	if res.Status != daemonProbeRunning {
		t.Fatalf("status = %q, want %q", res.Status, daemonProbeRunning)
	}
	if res.Port != port {
		t.Fatalf("port = %d, want %d", res.Port, port)
	}
}

func TestProbeLocalDaemonNotRunning(t *testing.T) {
	server := httptest.NewServer(httpHandlerWithStatus(http.StatusOK))
	server.Close()

	port := server.Listener.Addr().(*net.TCPAddr).Port
	res := probeLocalDaemon(context.Background(), port)
	if res.Status != daemonProbeNotRunning {
		t.Fatalf("status = %q, want %q", res.Status, daemonProbeNotRunning)
	}
}

func TestProbeLocalDaemonUnreachable(t *testing.T) {
	server := httptest.NewServer(httpHandlerWithStatus(http.StatusInternalServerError))
	defer server.Close()

	port := server.Listener.Addr().(*net.TCPAddr).Port
	res := probeLocalDaemon(context.Background(), port)
	if res.Status != daemonProbeUnreachable {
		t.Fatalf("status = %q, want %q", res.Status, daemonProbeUnreachable)
	}
}

func TestProbeLocalDaemonTransportInjectable(t *testing.T) {
	orig := daemonProbeTransport
	daemonProbeTransport = &fakePingTransport{statusCode: http.StatusOK}
	defer func() { daemonProbeTransport = orig }()

	res := probeLocalDaemon(context.Background(), 1234)
	if res.Status != daemonProbeRunning {
		t.Fatalf("status = %q, want %q", res.Status, daemonProbeRunning)
	}
	if res.Port != 1234 {
		t.Fatalf("port = %d, want 1234", res.Port)
	}
}

func TestProbeLocalDaemonDialError(t *testing.T) {
	orig := daemonProbeTransport
	daemonProbeTransport = &fakePingTransport{err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}
	defer func() { daemonProbeTransport = orig }()

	res := probeLocalDaemon(context.Background(), 1234)
	if res.Status != daemonProbeNotRunning {
		t.Fatalf("status = %q, want %q", res.Status, daemonProbeNotRunning)
	}
}


