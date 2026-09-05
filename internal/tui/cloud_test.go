package tui

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/cloudconfig"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
	tea "github.com/charmbracelet/bubbletea"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestSaveCloudServerURLPreservesStoredToken(t *testing.T) {
	dataDir := t.TempDir()
	if err := cloudconfig.Save(dataDir, &cloudconfig.Config{ServerURL: "https://old.example.test", Token: "stored-token"}); err != nil {
		t.Fatalf("seed cloud config: %v", err)
	}

	if err := saveCloudServerURL(dataDir, "https://cloud.example.test/"); err != nil {
		t.Fatalf("save cloud server URL: %v", err)
	}
	config, err := cloudconfig.Load(dataDir)
	if err != nil {
		t.Fatalf("load saved cloud config: %v", err)
	}
	if config.ServerURL != "https://cloud.example.test/" || config.Token != "stored-token" {
		t.Fatalf("saved cloud config = %+v, want updated URL and preserved token", config)
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

func TestCloudConfigPersistsUnauthorizedPingResult(t *testing.T) {
	originalTransport := pingCloudTransport
	t.Cleanup(func() { pingCloudTransport = originalTransport })
	pingCloudTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})

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

	updatedModel, _ := m.Update(pingCloudServer(cloudPingFromConfig, 1, "https://cloud.example.test", "token")())
	updated := updatedModel.(Model)
	if updated.CloudConfigSaving || updated.CloudConfigPingStatus != "unauthorized" || updated.CloudConfigError != "" {
		t.Fatalf("unauthorized config ping state = %+v", updated)
	}
	saved, err := cloudconfig.Load(s.DataDir())
	if err != nil || saved.ServerURL != "https://cloud.example.test" {
		t.Fatalf("saved server URL = %q, err=%v", saved.ServerURL, err)
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
}

func TestCloudConfigViewShowsReadOnlyTokenSource(t *testing.T) {
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

func TestCloudStatusRendersOnlyApprovedFields(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudStatus
	m.Width = 160
	m.CloudStatusServerURL = "https://cloud.example.test"
	m.CloudStatusHealth = "reachable"
	m.CloudStatusLastSync = "2026-08-31T10:00:00Z"
	m.CloudStatusPendingCount = 2
	m.CloudStatusLastError = "sync timeout"

	view := strings.Join(strings.Fields(m.View()), " ")
	for _, want := range []string{"Server URL:", "Connection health:", "Last successful sync:", "Pending mutations:", "Last error:"} {
		if !strings.Contains(view, want) {
			t.Fatalf("status view missing %q: %q", want, view)
		}
	}
	for _, unwanted := range []string{"Token source:", "Sync lifecycle:", "Local daemon:", "Auth status:", "Sync readiness:"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("status view includes removed field %q: %q", unwanted, view)
		}
	}
}

func TestCloudEnrollmentToggleErrorClearsLoading(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudEnrollment
	m.CloudEnrollmentLoading = true

	updatedModel, _ := m.Update(cloudEnrollmentToggledMsg{err: errors.New("database locked")})
	updated := updatedModel.(Model)
	if updated.CloudEnrollmentLoading || updated.CloudEnrollmentError != "database locked" {
		t.Fatalf("toggle error state = loading:%t error:%q", updated.CloudEnrollmentLoading, updated.CloudEnrollmentError)
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
	if updated.CloudConfigPingStatus != "reachable" || !strings.Contains(updated.View(), "Connection: reachable") {
		t.Fatalf("save outcome was not retained on config screen: %+v", updated)
	}
}
