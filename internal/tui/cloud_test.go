package tui

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloudconfig"
	tea "github.com/charmbracelet/bubbletea"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestSaveCloudServerURLReplacesMalformedConfig(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(cloudconfig.Path(dataDir), []byte("{not JSON"), 0o644); err != nil {
		t.Fatalf("seed malformed cloud config: %v", err)
	}

	if err := saveCloudServerURL(dataDir, "https://cloud.example.test/"); err != nil {
		t.Fatalf("replace malformed cloud config: %v", err)
	}
	config, err := cloudconfig.Load(dataDir)
	if err != nil {
		t.Fatalf("load replacement cloud config: %v", err)
	}
	if config.ServerURL != "https://cloud.example.test/" {
		t.Fatalf("server URL = %q", config.ServerURL)
	}
}

func TestPingCloudServerUsesSingleHealthPathForTrailingSlashURL(t *testing.T) {
	originalTransport := pingCloudTransport
	t.Cleanup(func() { pingCloudTransport = originalTransport })
	gotPath := ""
	pingCloudTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotPath = request.URL.Path
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})

	status, err := pingCloudServerStatus("https://cloud.example.test/", "")
	if err != nil || status != "reachable" {
		t.Fatalf("ping status = %q, error = %v", status, err)
	}
	if gotPath != cloudHealthPath {
		t.Fatalf("health request path = %q, want %q", gotPath, cloudHealthPath)
	}
}

func TestCloudConfigTabCyclesFocus(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudConfig
	m.CloudConfigFocus = cloudConfigFocusInput
	m.CloudConfigInput.Focus()

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := updatedModel.(Model)
	if updated.CloudConfigFocus != cloudConfigFocusTest || updated.CloudConfigInput.Focused() {
		t.Fatal("tab should move focus from the input to Test")
	}

	updatedModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	updated = updatedModel.(Model)
	if updated.CloudConfigFocus != cloudConfigFocusInput || !updated.CloudConfigInput.Focused() {
		t.Fatal("shift-tab should return focus to the input")
	}

	updated.CloudConfigFocus = cloudConfigFocusCancel
	updated.CloudConfigInput.Blur()
	updatedModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = updatedModel.(Model)
	if updated.CloudConfigFocus != cloudConfigFocusInput || !updated.CloudConfigInput.Focused() {
		t.Fatal("tab should wrap from Cancel to the input")
	}
}

func TestCloudConfigViewLabelsTokenOverrideHint(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudConfig
	m.CloudConfigTokenSource = TokenSourceFile

	view := m.viewCloudConfig()
	for _, want := range []string{"Server URL:", "Token source:", "Set ENGRAM_CLOUD_TOKEN to override cloud.json.token"} {
		if !strings.Contains(view, want) {
			t.Fatalf("cloud config view missing %q: %q", want, view)
		}
	}
}

func TestCloudStatusRendersParityFieldsAndSyncReason(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudStatus
	m.Width = 160
	m.CloudStatusServerURL = "https://cloud.example.test"
	m.CloudStatusTarget = "cloud"
	m.CloudStatusAuthStatus = "ready"
	m.CloudStatusSyncLifecycle = "degraded"
	m.CloudStatusSyncReasonCode = "auth_required"
	m.CloudStatusSyncReasonMessage = "token missing"

	view := m.View()
	for _, want := range []string{"Cloud status:", "configured (target=cloud)", "Auth status:", "ready", "Sync diagnostic:", "reason_code:", "auth_required", "reason_message:", "token missing"} {
		if !strings.Contains(view, want) {
			t.Fatalf("status view missing %q: %q", want, view)
		}
	}
}

func TestCloudDaemonProbeMessageUpdatesStatusIndependently(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudStatus

	updatedModel, _ := m.Update(cloudDaemonProbeMsg{result: cloudconfig.Result{Status: cloudconfig.ProbeNotRunning, Port: 8123}})
	updated := updatedModel.(Model)
	if updated.CloudStatusLocalDaemon != "not running on port 8123" {
		t.Fatalf("local daemon = %q", updated.CloudStatusLocalDaemon)
	}
	if !strings.Contains(updated.CloudStatusDaemonHint, "engram serve") {
		t.Fatalf("daemon hint = %q", updated.CloudStatusDaemonHint)
	}
}

func TestCloudStatusMessagesPreserveDaemonProbeAcrossArrivalOrders(t *testing.T) {
	status := cloudStatusLoadedMsg{
		serverURL: "https://cloud.example.test",
		target:    "cloud",
	}
	probe := cloudDaemonProbeMsg{result: cloudconfig.Result{Status: cloudconfig.ProbeNotRunning, Port: 8123}}

	tests := []struct {
		name     string
		messages []tea.Msg
	}{
		{name: "status before daemon probe", messages: []tea.Msg{status, probe}},
		{name: "daemon probe before status", messages: []tea.Msg{probe, status}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(nil, "")
			m.Screen = ScreenCloudStatus
			m.CloudStatusLoading = true

			for _, msg := range tt.messages {
				updatedModel, _ := m.Update(msg)
				m = updatedModel.(Model)
			}

			if m.CloudStatusLocalDaemon != "not running on port 8123" {
				t.Fatalf("local daemon = %q", m.CloudStatusLocalDaemon)
			}
			if !strings.Contains(m.CloudStatusDaemonHint, "engram serve") {
				t.Fatalf("daemon hint = %q", m.CloudStatusDaemonHint)
			}
			if m.CloudStatusLoading {
				t.Fatal("status load should finish")
			}
		})
	}
}

func TestCloudStatusLoadClearsDaemonProbeState(t *testing.T) {
	tests := []struct {
		name string
		load func(Model) (tea.Model, tea.Cmd)
	}{
		{
			name: "entering status",
			load: func(m Model) (tea.Model, tea.Cmd) {
				m.Screen = ScreenCloudSettings
				m.Cursor = 1
				return m.handleCloudSettingsKeys("enter")
			},
		},
		{
			name: "reloading status",
			load: func(m Model) (tea.Model, tea.Cmd) {
				m.Screen = ScreenCloudStatus
				return m.handleCloudStatusKeys("r")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(nil, "")
			m.CloudStatusLocalDaemon = "running on port 8123"
			m.CloudStatusDaemonHint = "stale hint"

			updatedModel, cmd := tt.load(m)
			updated := updatedModel.(Model)
			if !updated.CloudStatusLoading {
				t.Fatal("status load should be marked as loading")
			}
			if updated.CloudStatusLocalDaemon != "" || updated.CloudStatusDaemonHint != "" {
				t.Fatalf("status load should clear daemon state, got %q / %q", updated.CloudStatusLocalDaemon, updated.CloudStatusDaemonHint)
			}
			if cmd == nil {
				t.Fatal("status load should start status and daemon commands")
			}
		})
	}
}

func TestCloudEnrollmentToggleErrorClearsLoading(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudEnrollment
	m.CloudEnrollmentLoading = true

	updatedModel, _ := m.Update(cloudEnrollmentToggledMsg{err: errors.New("database locked")})
	updated := updatedModel.(Model)
	if updated.CloudEnrollmentLoading {
		t.Fatal("toggle error should clear loading")
	}
	if updated.CloudEnrollmentError != "database locked" {
		t.Fatalf("enrollment error = %q", updated.CloudEnrollmentError)
	}
}
