package tui

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloudconfig"
	"github.com/Gentleman-Programming/engram/internal/store"
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

func TestCloudSavePersistsReachableStatusOnConfigurationScreen(t *testing.T) {
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("default config: %v", err)
	}
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	m := New(s, "")
	m.Screen = ScreenCloudConfig
	m.CloudConfigFocus = cloudConfigFocusSave
	m.CloudConfigSaving = true
	m.CloudConfigInput.SetValue("https://cloud.example.test")
	m.CloudRequestGeneration = 1

	updatedModel, _ := m.Update(cloudPingMsg{origin: cloudPingFromConfig, generation: 1, serverURL: "https://cloud.example.test", status: "reachable"})
	updated := updatedModel.(Model)
	if updated.Screen != ScreenCloudConfig || updated.CloudConfigFocus != cloudConfigFocusSave || updated.CloudConfigPingStatus != "reachable" {
		t.Fatalf("save outcome was not retained on config screen: %+v", updated)
	}
	if !strings.Contains(updated.View(), "Connection: reachable") {
		t.Fatalf("config view does not render save outcome: %q", updated.View())
	}
	saved, err := cloudconfig.Load(s.DataDir())
	if err != nil || saved.ServerURL != "https://cloud.example.test" {
		t.Fatalf("saved server URL = %q, err=%v", saved.ServerURL, err)
	}
}

func TestCloudResponsesRejectStaleGenerationsAndPreserveSeparateErrors(t *testing.T) {
	t.Run("stale config load", func(t *testing.T) {
		m := New(nil, "")
		m.Screen, m.CloudRequestGeneration = ScreenCloudConfig, 2
		m.CloudConfigInput.SetValue("https://current.example.test")
		updated, _ := m.Update(cloudConfigLoadedMsg{generation: 1, serverURL: "https://stale.example.test"})
		if updated.(Model).CloudConfigInput.Value() != "https://current.example.test" {
			t.Fatal("stale config load overwrote current input")
		}
	})
	t.Run("stale status load and daemon probe", func(t *testing.T) {
		m := New(nil, "")
		m.Screen, m.CloudRequestGeneration, m.CloudStatusServerURL, m.CloudStatusLocalDaemon = ScreenCloudStatus, 2, "https://current.example.test", "running"
		updated, _ := m.Update(cloudStatusLoadedMsg{generation: 1, serverURL: "https://stale.example.test"})
		m = updated.(Model)
		updated, _ = m.Update(cloudDaemonProbeMsg{generation: 1, result: cloudconfig.Result{Status: cloudconfig.ProbeNotRunning, Port: 8123}})
		m = updated.(Model)
		if m.CloudStatusServerURL != "https://current.example.test" || m.CloudStatusLocalDaemon != "running" {
			t.Fatal("stale status response overwrote current state")
		}
	})
	t.Run("pings require current URLs", func(t *testing.T) {
		m := New(nil, "")
		m.Screen, m.CloudRequestGeneration, m.CloudConfigSaving = ScreenCloudConfig, 2, true
		m.CloudConfigInput.SetValue("https://current.example.test")
		updated, _ := m.Update(cloudPingMsg{origin: cloudPingFromConfig, generation: 2, serverURL: "https://stale.example.test", status: "reachable"})
		m = updated.(Model)
		if !m.CloudConfigSaving {
			t.Fatal("config ping for an old URL changed current save state")
		}
		m.Screen, m.CloudStatusServerURL, m.CloudStatusHealth = ScreenCloudStatus, "https://current.example.test", "reachable"
		updated, _ = m.Update(cloudPingMsg{origin: cloudPingFromStatus, generation: 1, serverURL: "https://old.example.test", status: "unreachable"})
		if updated.(Model).CloudStatusHealth != "reachable" {
			t.Fatal("stale status ping after refresh overwrote health")
		}
	})
	t.Run("current chained ping preserves sync error and clears health error", func(t *testing.T) {
		cfg, err := store.DefaultConfig()
		if err != nil {
			t.Fatalf("default config: %v", err)
		}
		cfg.DataDir = t.TempDir()
		s, err := store.New(cfg)
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		m := New(s, "")
		m.Screen, m.CloudRequestGeneration = ScreenCloudStatus, 2
		updated, cmd := m.Update(cloudStatusLoadedMsg{generation: 2, serverURL: "https://current.example.test", lastError: "sync failed"})
		m = updated.(Model)
		if cmd == nil || m.CloudStatusLastError != "sync failed" || m.CloudStatusHealthError != "" {
			t.Fatalf("status load did not retain sync error and clear health error: %+v", m)
		}
		updated, _ = m.Update(cloudPingMsg{origin: cloudPingFromStatus, generation: 2, serverURL: "https://current.example.test", status: "unreachable", err: errors.New("health failed")})
		m = updated.(Model)
		if m.CloudStatusLastError != "sync failed" || m.CloudStatusHealthError != "health failed" {
			t.Fatalf("health failure overwrote sync error: %+v", m)
		}
		if !strings.Contains(m.View(), "Sync error: sync failed") || !strings.Contains(m.View(), "Health error: health failed") {
			t.Fatalf("status view does not render separate errors: %q", m.View())
		}
		updated, _ = m.Update(cloudPingMsg{origin: cloudPingFromStatus, generation: 2, serverURL: "https://current.example.test", status: "reachable"})
		m = updated.(Model)
		if m.CloudStatusHealthError != "" || !strings.Contains(m.View(), "Sync error: sync failed") || strings.Contains(m.View(), "Health error:") {
			t.Fatalf("successful health ping did not clear health error independently: %q", m.View())
		}
	})
}
