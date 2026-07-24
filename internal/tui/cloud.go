package tui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloudconfig"

	tea "github.com/charmbracelet/bubbletea"
)

const cloudHealthPath = "/health"

// TokenSourceEnv is the display label used when ENGRAM_CLOUD_TOKEN
// environment variable is set. Mirrors cloudconfig.LabelSourceEnv so the
// view layer can render the same string the CLI's status line prints;
// the parity is pinned by cloudconfig.TestCLIAndTUIAgreeOnTokenSource.
const TokenSourceEnv = "set via ENGRAM_CLOUD_TOKEN"

// TokenSourceFile is the display label used when a token is read
// from the cloud.json config file. Mirrors cloudconfig.LabelSourceFile.
const TokenSourceFile = "read from cloud.json"

// TokenSourceNone is the display label used when no cloud token is
// available from any source. Mirrors cloudconfig.LabelSourceNone.
const TokenSourceNone = "not set"

// envCloudServer is the environment variable the TUI's Cloud Config form
// honors as an override of the file-stored server URL. It matches the
// CLI's cmd/engram/main.go:resolveCloudRuntimeConfig behavior; the TUI's
// old effectiveCloudToken / loadCloudConfigCmd did not, which is the
// silent precedence drift this package fixes.
const envCloudServer = "ENGRAM_CLOUD_SERVER"

// tuiCloudConfigForUI returns the effective Config the TUI's Cloud
// Config form should display and persist: the cloudconfig.Load result
// with the ENGRAM_CLOUD_SERVER env var applied as an override when it
// is set and non-empty (after whitespace trimming).
//
// A missing or malformed cloud.json yields a zero-value Config (no
// error surfaced to the view — the user can still type a URL and save).
// A whitespace-only env value is treated as unset, matching the CLI.
func tuiCloudConfigForUI(dataDir string) cloudconfig.Config {
	cfg, _ := cloudconfig.Load(dataDir)
	if cfg == nil {
		cfg = &cloudconfig.Config{}
	}
	if v := strings.TrimSpace(os.Getenv(envCloudServer)); v != "" {
		cfg.ServerURL = v
	}
	return *cfg
}

// saveTUIServerURL persists serverURL into the cloud.json inside dataDir
// while preserving any pre-existing Token field. It replaces the old
// forked saveCloudConfig (T-608.16): load the existing config, update
// only the URL, save through the package so perms + atomicity are owned
// in one place.
func saveTUIServerURL(dataDir, serverURL string) error {
	cfg, err := cloudconfig.Load(dataDir)
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = &cloudconfig.Config{}
	}
	cfg.ServerURL = serverURL
	return cloudconfig.Save(dataDir, cfg)
}

// cloudDaemonProbeMsg is delivered when probeLocalDaemonCmd completes.
// It carries the cloudconfig.Result from a single LocalDaemonProbe call
// (T-608.17) so the TUI can render the daemon status independently of
// the cloud-config form's load message.
//
// T-608.19 wires the message into the update loop: the Cloud Status
// arm dispatches probeLocalDaemonCmd alongside the status load, and a
// new case arm in Update writes probe.result to m.CloudDaemonProbe.
type cloudDaemonProbeMsg struct {
	result cloudconfig.Result
	err    error
}

// probeLocalDaemonCmd returns a tea.Cmd that runs cloudconfig.LocalDaemonProbe
// once (with the default 1s ProbeTimeout, per ADR-1) and delivers the
// outcome as a cloudDaemonProbeMsg. The probe targets the port returned
// by cloudconfig.ResolvePort, which honors ENGRAM_PORT.
//
// This is the TUI's local-daemon probe entry point. It is decoupled
// from the cloud /health probe (pingCloudServer, kept inline per ADR-2):
// the two have different timeouts (1s vs 3s) and different status
// mappings (ProbeRunning/ProbeNotRunning/ProbeUnreachable vs
// reachable/unauthorized/unreachable).
func probeLocalDaemonCmd() tea.Cmd {
	return func() tea.Msg {
		result := cloudconfig.LocalDaemonProbe(context.Background(), cloudconfig.ResolvePort())
		return cloudDaemonProbeMsg{result: result}
	}
}

// pingCloudTransport can be overridden in tests to avoid real network calls.
var pingCloudTransport http.RoundTripper = http.DefaultTransport

// pingCloudServer returns a tea.Cmd that probes the cloud server health
// endpoint. The result is delivered as a cloudPingMsg.
//
// Use this when the user triggers a "Test Connection" action on the Cloud
// Config screen. Kept inline in the TUI per ADR-2: it is a TUI-only
// tea.Cmd wrapper around a synchronous HTTP GET with its own 3s timeout
// and a different status mapping than the local-daemon probe; the
// cloudconfig package does not need to own it.
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
// or "unreachable" on any other error. URL validation delegates to
// cloudconfig.ValidateServerURL (T-608.17), which accepts http/https
// with a host, clears query/fragment on success.
func pingCloudServerStatus(serverURL, token string) (string, error) {
	validatedURL, err := cloudconfig.ValidateServerURL(serverURL)
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
