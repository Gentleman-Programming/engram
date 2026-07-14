package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	cloudConfigFileName = "cloud.json"
	cloudHealthPath     = "/health"
)

// TokenSourceEnv is the display label used when ENGRAM_CLOUD_TOKEN
// environment variable is set.
const TokenSourceEnv = "set via ENGRAM_CLOUD_TOKEN"

// TokenSourceFile is the display label used when a token is read
// from the cloud.json config file.
const TokenSourceFile = "read from cloud.json"

// TokenSourceNone is the display label used when no cloud token is
// available from any source.
const TokenSourceNone = "not set"

type tuiCloudConfig struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token,omitempty"`
}

// cloudConfigPath returns the full path to the cloud config JSON file
// within the given data directory.
func cloudConfigPath(dataDir string) string {
	return filepath.Join(dataDir, cloudConfigFileName)
}

// loadCloudConfig reads the cloud config JSON file from the data directory.
//
// Returns an empty config if the file does not exist. Returns an error
// if the file exists but cannot be read or parsed.
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

// saveCloudConfig writes the given serverURL into the cloud config JSON file
// within the data directory, preserving any existing fields.
//
// Creates the data directory if it does not exist.
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

// tokenSourceMessage returns a user-facing label describing how the cloud
// token is currently configured: via environment variable, config file, or
// not set at all.
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

// effectiveCloudToken returns the first available cloud token, checking the
// ENGRAM_CLOUD_TOKEN environment variable before falling back to the token
// stored in the cloud config file.
//
// Returns an empty string if no token is available from any source.
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

// pingCloudServer returns a tea.Cmd that probes the cloud server health
// endpoint. The result is delivered as a cloudPingMsg.
//
// Use this when the user triggers a "Test Connection" action on the Cloud
// Config screen.
func pingCloudServer(serverURL, token string) tea.Cmd {
	return func() tea.Msg {
		status, err := pingCloudServerStatus(serverURL, token)
		return cloudPingMsg{status: status, err: err}
	}
}

// pingCloudServerStatus performs a synchronous HTTP GET to the cloud
// server's /health endpoint.
//
// Returns "reachable" on a 2xx response, "unauthorized" on 401,
// or "unreachable" on any other error.
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

// validateCloudServerURL parses and validates a cloud server URL.
//
// The URL must have an http or https scheme, a non-empty host, and no
// query parameters or fragments. Returns the normalized URL string on
// success.
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


