package main

import (
	"context"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/autosync"
	engramsrv "github.com/Gentleman-Programming/engram/v2/internal/server"
)

// fakeStatusProvider implements server.SyncStatusProvider for fallback tests.
type fakeStatusProvider struct {
	status engramsrv.SyncStatus
}

func (f *fakeStatusProvider) Status(_ string) engramsrv.SyncStatus {
	return f.status
}

// fakeAutosyncManager returns controlled Status values.
type fakeAutosyncManager struct {
	status autosync.Status
}

func (f *fakeAutosyncManager) Status() autosync.Status {
	return f.status
}

func (f *fakeAutosyncManager) Run(_ context.Context) {}
func (f *fakeAutosyncManager) NotifyDirty()      {}
func (f *fakeAutosyncManager) Stop()             {}
func (f *fakeAutosyncManager) StopForUpgrade(_ string) error {
	return nil
}
func (f *fakeAutosyncManager) ResumeAfterUpgrade(_ string) error {
	return nil
}

// ─── Status adapter tests (REQ-209) ──────────────────────────────────────────

func TestSyncStatusAdapterHealthy(t *testing.T) {
	mgr := &fakeAutosyncManager{status: autosync.Status{Phase: autosync.PhaseHealthy}}
	fallback := &fakeStatusProvider{status: engramsrv.SyncStatus{Phase: "degraded"}}
	adapter := &autosyncStatusAdapter{mgr: mgr, fallback: fallback}

	got := adapter.Status("proj-a")
	if got.Phase != "healthy" {
		t.Fatalf("expected healthy, got %q", got.Phase)
	}
}

func TestSyncStatusAdapterRunning(t *testing.T) {
	for _, phase := range []string{autosync.PhasePushing, autosync.PhasePulling, autosync.PhaseIdle} {
		mgr := &fakeAutosyncManager{status: autosync.Status{Phase: phase}}
		fallback := &fakeStatusProvider{}
		adapter := &autosyncStatusAdapter{mgr: mgr, fallback: fallback}

		got := adapter.Status("proj-a")
		if got.Phase != "running" {
			t.Fatalf("phase=%q: expected running, got %q", phase, got.Phase)
		}
	}
}

func TestSyncStatusAdapterBackoff(t *testing.T) {
	// BW5: When the manager sets a specific reason code, it is passed through.
	// When reason code is empty, the adapter defaults to "transport_failed".
	tests := []struct {
		name           string
		reasonCode     string
		expectedReason string
	}{
		{name: "empty reason defaults to transport_failed", reasonCode: "", expectedReason: "transport_failed"},
		{name: "internal_error passed through", reasonCode: "internal_error", expectedReason: "internal_error"},
		{name: "auth_required passed through", reasonCode: "auth_required", expectedReason: "auth_required"},
		{name: "server_unsupported passed through", reasonCode: "server_unsupported", expectedReason: "server_unsupported"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &fakeAutosyncManager{status: autosync.Status{
				Phase:      autosync.PhaseBackoff,
				ReasonCode: tc.reasonCode,
			}}
			fallback := &fakeStatusProvider{}
			adapter := &autosyncStatusAdapter{mgr: mgr, fallback: fallback}

			got := adapter.Status("proj-a")
			if got.Phase != "degraded" {
				t.Fatalf("expected degraded, got %q", got.Phase)
			}
			if got.ReasonCode != tc.expectedReason {
				t.Fatalf("expected %q, got %q", tc.expectedReason, got.ReasonCode)
			}
		})
	}
}

func TestSyncStatusAdapterPushFailed(t *testing.T) {
	mgr := &fakeAutosyncManager{status: autosync.Status{
		Phase:     autosync.PhasePushFailed,
		LastError: "connection refused",
	}}
	fallback := &fakeStatusProvider{}
	adapter := &autosyncStatusAdapter{mgr: mgr, fallback: fallback}

	got := adapter.Status("proj-a")
	if got.Phase != "degraded" {
		t.Fatalf("expected degraded, got %q", got.Phase)
	}
}

func TestSyncStatusAdapterPullFailed(t *testing.T) {
	mgr := &fakeAutosyncManager{status: autosync.Status{
		Phase:     autosync.PhasePullFailed,
		LastError: "timeout",
	}}
	fallback := &fakeStatusProvider{}
	adapter := &autosyncStatusAdapter{mgr: mgr, fallback: fallback}

	got := adapter.Status("proj-a")
	if got.Phase != "degraded" {
		t.Fatalf("expected degraded, got %q", got.Phase)
	}
}

func TestSyncStatusAdapterDisabled(t *testing.T) {
	mgr := &fakeAutosyncManager{status: autosync.Status{
		Phase:      autosync.PhaseDisabled,
		ReasonCode: "paused",
	}}
	fallback := &fakeStatusProvider{}
	adapter := &autosyncStatusAdapter{mgr: mgr, fallback: fallback}

	got := adapter.Status("proj-a")
	if got.ReasonCode != "upgrade_paused" {
		t.Fatalf("expected upgrade_paused, got %q", got.ReasonCode)
	}
}

func TestSyncStatusAdapterNilFallback(t *testing.T) {
	// nil mgr → delegate to fallback.
	expected := engramsrv.SyncStatus{Phase: "healthy", ReasonCode: "all-good"}
	fallback := &fakeStatusProvider{status: expected}
	adapter := &autosyncStatusAdapter{mgr: nil, fallback: fallback}

	got := adapter.Status("proj-a")
	if got.Phase != expected.Phase || got.ReasonCode != expected.ReasonCode {
		t.Fatalf("expected %+v, got %+v", expected, got)
	}
}

func TestSyncStatusAdapterOverlayUpgradeStage(t *testing.T) {
	// When autosync is enabled, upgrade fields come from fallback.
	now := time.Now()
	_ = now
	mgr := &fakeAutosyncManager{status: autosync.Status{Phase: autosync.PhaseHealthy}}
	fallback := &fakeStatusProvider{status: engramsrv.SyncStatus{
		Phase:                "degraded",
		UpgradeStage:         "planned",
		UpgradeReasonCode:    "upgrade_planned",
		UpgradeReasonMessage: "upgrade is planned",
	}}
	adapter := &autosyncStatusAdapter{mgr: mgr, fallback: fallback}

	got := adapter.Status("proj-a")
	// Autosync phase wins
	if got.Phase != "healthy" {
		t.Fatalf("expected healthy (autosync wins), got %q", got.Phase)
	}
	// Upgrade fields overlaid from fallback
	if got.UpgradeStage != "planned" {
		t.Fatalf("expected upgrade_stage=planned, got %q", got.UpgradeStage)
	}
}
