package tui

import (
	"fmt"
	"net/http"
	"os"
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

func saveCloudServerURL(dataDir, serverURL string) error {
	cfg, err := cloudconfig.Load(dataDir)
	if err != nil {
		// The form is an explicit replacement action, so a malformed legacy
		// file must not prevent the user from restoring a usable configuration.
		cfg = &cloudconfig.Config{}
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

func cloudDaemonStatus(result cloudconfig.Result) (string, string) {
	switch result.Status {
	case cloudconfig.ProbeRunning:
		return fmt.Sprintf("running on port %d", result.Port), ""
	case cloudconfig.ProbeNotRunning:
		return fmt.Sprintf("not running on port %d", result.Port), "Hint: run `engram serve` to resume autosync; on macOS see DOCS.md launchd template to keep it alive across upgrades"
	default:
		return fmt.Sprintf("unreachable on port %d", result.Port), ""
	}
}

func cloudStatusAuth(token string, insecure bool) (status, warning, hint, readiness string) {
	readiness = "ready for explicit --project sync (project must be enrolled)"
	if token != "" {
		return "ready (token provided via runtime cloud config)", "", "", readiness
	}
	if insecure {
		return "ready (insecure local-dev mode: ENGRAM_CLOUD_INSECURE_NO_AUTH=1)", "Warning: bearer auth is disabled in insecure mode; do not use in production", "", readiness
	}
	return "token not configured (client token is optional at preflight)", "", "Hint: if the remote server enforces bearer auth, set ENGRAM_CLOUD_TOKEN", readiness
}

func cloudInsecureNoAuth() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv("ENGRAM_CLOUD_INSECURE_NO_AUTH")))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
