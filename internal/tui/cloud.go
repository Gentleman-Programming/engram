package tui

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloudconfig"
	tea "github.com/charmbracelet/bubbletea"
)

const cloudHealthPath = "/health"

const (
	TokenSourceEnv  = cloudconfig.LabelSourceEnv
	TokenSourceFile = cloudconfig.LabelSourceFile
	TokenSourceNone = cloudconfig.LabelSourceNone
)

type cloudPingOrigin int

const (
	cloudPingFromConfig cloudPingOrigin = iota
	cloudPingFromStatus
)

// pingCloudTransport can be overridden by focused TUI tests.
var pingCloudTransport http.RoundTripper = http.DefaultTransport

func loadCloudConfigForUI(dataDir string) (*cloudconfig.Config, error) {
	cfg, err := cloudconfig.Load(dataDir)
	if err != nil {
		return nil, err
	}
	return cloudconfig.ApplyServerOverride(cfg), nil
}

// saveCloudServerURL preserves the existing token because this TUI form owns
// only the server URL.
func saveCloudServerURL(dataDir, serverURL string) error {
	cfg, err := cloudconfig.Load(dataDir)
	if err != nil {
		return err
	}
	cfg.ServerURL = serverURL
	return cloudconfig.Save(dataDir, cfg)
}

func pingCloudServer(origin cloudPingOrigin, generation uint64, serverURL, token string) tea.Cmd {
	return func() tea.Msg {
		status, err := pingCloudServerStatus(serverURL, token)
		return cloudPingMsg{origin: origin, generation: generation, serverURL: serverURL, status: status, err: err}
	}
}

func pingCloudServerStatus(serverURL, token string) (string, error) {
	validatedURL, err := cloudconfig.ValidateServerURL(serverURL)
	if err != nil {
		return "unreachable", err
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(validatedURL, "/")+cloudHealthPath, nil)
	if err != nil {
		return "unreachable", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second, Transport: pingCloudTransport}).Do(req)
	if err != nil {
		return "unreachable", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "unauthorized", nil
	case resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices:
		return "reachable", nil
	default:
		return "unreachable", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
}
