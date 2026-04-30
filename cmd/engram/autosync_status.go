package main

import (
	"github.com/Gentleman-Programming/engram/internal/cloud/autosync"
	"github.com/Gentleman-Programming/engram/internal/server"
)

// autosyncStatusProvider is the interface subset of autosync.Manager used by the adapter.
// This avoids importing the full Manager type and allows test fakes.
type autosyncStatusProvider interface {
	Status() autosync.Status
}

// autosyncStatusAdapter implements server.SyncStatusProvider by mapping
// autosync.Manager phases to server.SyncStatus. When mgr is nil (autosync
// disabled), it falls back to the storeSyncStatusProvider.
//
// REQ-209, REQ-cloud-sync-status.
type autosyncStatusAdapter struct {
	mgr      autosyncStatusProvider
	fallback server.SyncStatusProvider
}

// Status implements server.SyncStatusProvider.
func (a *autosyncStatusAdapter) Status(project string) server.SyncStatus {
	if a.mgr == nil {
		if a.fallback != nil {
			return a.fallback.Status(project)
		}
		return server.SyncStatus{Phase: "idle"}
	}

	// Get upgrade-stage overlay from fallback (these come from the store, not autosync).
	var upgradeStage, upgradeCode, upgradeMsg string
	if a.fallback != nil {
		fb := a.fallback.Status(project)
		upgradeStage = fb.UpgradeStage
		upgradeCode = fb.UpgradeReasonCode
		upgradeMsg = fb.UpgradeReasonMessage
	}

	st := a.mgr.Status()
	result := a.mapPhase(st)
	result.UpgradeStage = upgradeStage
	result.UpgradeReasonCode = upgradeCode
	result.UpgradeReasonMessage = upgradeMsg
	return result
}

// mapPhase converts an autosync.Status to a server.SyncStatus.
// Phase mapping per REQ-209:
//   - PhaseHealthy → healthy
//   - PhasePushing / PhasePulling / PhaseIdle → running
//   - PhasePushFailed / PhasePullFailed / PhaseBackoff → degraded + transport_failed
//   - PhaseDisabled → degraded + upgrade_paused
func (a *autosyncStatusAdapter) mapPhase(st autosync.Status) server.SyncStatus {
	base := server.SyncStatus{
		Enabled:             true,
		LastError:           st.LastError,
		ConsecutiveFailures: st.ConsecutiveFailures,
		BackoffUntil:        st.BackoffUntil,
		LastSyncAt:          st.LastSyncAt,
		// Phase E: propagate deferred/dead counts from autosync.Status.
		DeferredCount: st.DeferredCount,
		DeadCount:     st.DeadCount,
	}

	switch st.Phase {
	case autosync.PhaseHealthy:
		base.Phase = "healthy"
		base.ReasonCode = ""

	case autosync.PhasePushing, autosync.PhasePulling, autosync.PhaseIdle:
		base.Phase = "running"

	case autosync.PhasePushFailed, autosync.PhasePullFailed, autosync.PhaseBackoff:
		base.Phase = "degraded"
		// BW5: Pass through specific reason codes (auth_required, policy_forbidden)
		// set by the manager; fall back to "transport_failed" for generic failures.
		if st.ReasonCode != "" {
			base.ReasonCode = st.ReasonCode
		} else {
			base.ReasonCode = "transport_failed"
		}
		if st.ReasonMessage != "" {
			base.ReasonMessage = st.ReasonMessage
		} else {
			base.ReasonMessage = st.LastError
		}

	case autosync.PhaseDisabled:
		base.Phase = "degraded"
		base.ReasonCode = "upgrade_paused"
		base.ReasonMessage = st.ReasonMessage

	default:
		base.Phase = st.Phase
		base.ReasonCode = st.ReasonCode
		base.ReasonMessage = st.ReasonMessage
	}

	return base
}
