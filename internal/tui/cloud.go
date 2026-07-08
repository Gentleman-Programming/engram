package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	cloudConfigFileName = "cloud.json"
	cloudHealthPath     = "/health"
)

// Token source labels displayed on the Cloud Config screen.
const (
	TokenSourceEnv  = "set via ENGRAM_CLOUD_TOKEN"
	TokenSourceFile = "read from cloud.json"
	TokenSourceNone = "not set"
)

type tuiCloudConfig struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token,omitempty"`
}

func cloudConfigPath(dataDir string) string {
	return filepath.Join(dataDir, cloudConfigFileName)
}

func loadCloudConfig(dataDir string) (*tuiCloudConfig, error) {
	path := cloudConfigPath(dataDir)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &tuiCloudConfig{}, nil
		}
		return nil, err
	}
	var cc tuiCloudConfig
	if err := json.Unmarshal(b, &cc); err != nil {
		return nil, err
	}
	return &cc, nil
}

func saveCloudConfig(dataDir, serverURL string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	cc, err := loadCloudConfig(dataDir)
	if err != nil {
		return err
	}
	cc.ServerURL = serverURL
	b, err := json.MarshalIndent(cc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cloudConfigPath(dataDir), b, 0o644)
}

func tokenSourceMessage(dataDir string) string {
	if strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_TOKEN")) != "" {
		return TokenSourceEnv
	}
	cc, err := loadCloudConfig(dataDir)
	if err == nil && strings.TrimSpace(cc.Token) != "" {
		return TokenSourceFile
	}
	return TokenSourceNone
}

func effectiveCloudToken(dataDir string) string {
	if token := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_TOKEN")); token != "" {
		return token
	}
	cc, err := loadCloudConfig(dataDir)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cc.Token)
}

// pingCloudTransport can be overridden in tests to avoid real network calls.
var pingCloudTransport http.RoundTripper = http.DefaultTransport

func pingCloudServer(serverURL, token string) tea.Cmd {
	return func() tea.Msg {
		status, err := pingCloudServerStatus(serverURL, token)
		return cloudPingMsg{status: status, err: err}
	}
}

func pingCloudServerStatus(serverURL, token string) (string, error) {
	validatedURL, err := validateCloudServerURL(serverURL)
	if err != nil {
		return "unreachable", err
	}

	req, err := http.NewRequest(http.MethodGet, validatedURL+cloudHealthPath, nil)
	if err != nil {
		return "unreachable", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 3 * time.Second, Transport: pingCloudTransport}
	resp, err := client.Do(req)
	if err != nil {
		return "unreachable", err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "unauthorized", nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return "reachable", nil
	default:
		return "unreachable", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
}

func validateCloudServerURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.ParseRequestURI(trimmed)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https")
	}
	if strings.TrimSpace(parsed.Host) == "" || strings.TrimSpace(parsed.Hostname()) == "" {
		return "", fmt.Errorf("host is required")
	}
	if strings.TrimSpace(parsed.RawQuery) != "" {
		return "", fmt.Errorf("query is not allowed")
	}
	if strings.TrimSpace(parsed.Fragment) != "" {
		return "", fmt.Errorf("fragment is not allowed")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

// isInsecureNoAuth mirrors cmd/engram/main.go:envBool for the
// ENGRAM_CLOUD_INSECURE_NO_AUTH env var. Truthy values are "1", "true",
// "yes" and "on" (case-insensitive, whitespace-trimmed).
func isInsecureNoAuth() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("ENGRAM_CLOUD_INSECURE_NO_AUTH")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// daemonProbeStatus describes the outcome of probing the local engram daemon.
type daemonProbeStatus string

const (
	daemonProbeRunning     daemonProbeStatus = "running"
	daemonProbeNotRunning  daemonProbeStatus = "not_running"
	daemonProbeUnreachable daemonProbeStatus = "unreachable"
)

// daemonProbeResult captures the outcome of a single probe.
type daemonProbeResult struct {
	Status daemonProbeStatus
	Port   int
	Err    error
}

const defaultDaemonProbePort = 7437

// daemonProbeTimeout is a var (not const) so tests can shorten it when
// exercising slow paths.
var daemonProbeTimeout = time.Second

// daemonProbeTransport can be overridden in tests to avoid real network calls.
var daemonProbeTransport http.RoundTripper = http.DefaultTransport

// probeLocalDaemon mirrors cmd/engram/cloud_daemon_probe.go:defaultCloudDaemonProbe.
// A dial error to 127.0.0.1 is interpreted as "not running"; any other error
// (timeout, non-2xx response, malformed reply) maps to "unreachable".
func probeLocalDaemon(ctx context.Context, port int) daemonProbeResult {
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	client := &http.Client{Timeout: daemonProbeTimeout, Transport: daemonProbeTransport}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return daemonProbeResult{Status: daemonProbeUnreachable, Port: port, Err: err}
	}
	resp, err := client.Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return daemonProbeResult{Status: daemonProbeNotRunning, Port: port, Err: err}
		}
		return daemonProbeResult{Status: daemonProbeUnreachable, Port: port, Err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return daemonProbeResult{Status: daemonProbeRunning, Port: port}
	}
	return daemonProbeResult{Status: daemonProbeUnreachable, Port: port}
}

// resolveDaemonProbePort mirrors cmd/engram/cloud_daemon_probe.go:resolveDaemonProbePort.
// It reads ENGRAM_PORT and falls back to 7437.
func resolveDaemonProbePort() int {
	if p := strings.TrimSpace(os.Getenv("ENGRAM_PORT")); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	return defaultDaemonProbePort
}


