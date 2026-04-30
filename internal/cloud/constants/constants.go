package constants

import "github.com/Gentleman-Programming/engram/internal/store"

const (
	TargetKeyCloud = store.DefaultSyncTargetKey

	ReasonBlockedUnenrolled           = "blocked_unenrolled"
	ReasonNonEnrolledPendingMutations = "non_enrolled_pending_mutations"
	ReasonPaused                      = "paused"
	ReasonAuthRequired                = "auth_required"
	ReasonPolicyForbidden             = "policy_forbidden"
	ReasonTransportFailed             = "transport_failed"
	ReasonCloudConfigError            = "cloud_config_error"

	UpgradeStatusReady   = "ready"
	UpgradeStatusBlocked = "blocked"

	UpgradeClassReady      = "ready"
	UpgradeClassRepairable = "repairable"
	UpgradeClassBlocked    = "blocked"
	UpgradeClassPolicy     = "policy"

	UpgradeReasonReady                 = "upgrade_ready"
	UpgradeReasonRepairableUnenrolled  = "upgrade_repairable_unenrolled"
	UpgradeReasonBlockedProjectMissing = "upgrade_blocked_project_required"
	UpgradeReasonPolicyConfig          = ReasonCloudConfigError
	UpgradeReasonPolicyForbidden       = ReasonPolicyForbidden

	UpgradeErrorClassRepairable = UpgradeClassRepairable
	UpgradeErrorClassBlocked    = UpgradeClassBlocked
	UpgradeErrorClassPolicy     = UpgradeClassPolicy

	UpgradeErrorCodeProjectRequired = UpgradeReasonBlockedProjectMissing
	UpgradeErrorCodePayloadInvalid  = "upgrade_repairable_payload_invalid"
	UpgradeErrorCodePayloadTooLarge = "upgrade_repairable_payload_too_large"
	UpgradeErrorCodeChunkConflict   = "upgrade_repairable_chunk_conflict"
	UpgradeErrorCodeInternal        = "upgrade_blocked_internal"
)

var DeterministicReasons = []string{
	ReasonBlockedUnenrolled,
	ReasonNonEnrolledPendingMutations,
	ReasonPaused,
	ReasonAuthRequired,
	ReasonPolicyForbidden,
	ReasonTransportFailed,
	ReasonCloudConfigError,
}
