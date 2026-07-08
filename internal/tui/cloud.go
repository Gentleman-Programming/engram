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


