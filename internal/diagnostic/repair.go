package diagnostic

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

const (
	RepairModePlan   RepairMode = "plan"
	RepairModeDryRun RepairMode = "dry_run"
	RepairModeApply  RepairMode = "apply"
)

type RepairMode string

// repairImplementations is the authoritative registry of diagnostic checks
// that have a doctor repair implementation. Keep the command-specific sync
// mutation repair here with the BuildRepairPlan implementations so callers
// cannot advertise a separate, hand-maintained support list.
var repairImplementations = map[string]struct{}{
	CheckSessionProjectDirectoryMismatch:  {},
	CheckManualSessionNameProjectMismatch: {},
	CheckInvalidSessionIdentity:           {},
	CheckOrphanedObservationSession:       {},
	CheckSyncMutationRequiredFields:       {},
	CheckSyncTargetClosedSpace:            {},
}

// RepairableCodes returns the registered repair implementations in stable
// order for command validation and help output.
func RepairableCodes() []string {
	codes := make([]string, 0, len(repairImplementations))
	for code := range repairImplementations {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// IsRepairableCode reports whether a diagnostic check has a repair
// implementation.
func IsRepairableCode(code string) bool {
	_, ok := repairImplementations[strings.TrimSpace(code)]
	return ok
}

type ProjectReclassifyAction struct {
	SessionID      string `json:"session_id"`
	FromProject    string `json:"from_project"`
	ToProject      string `json:"to_project"`
	ReasonCode     string `json:"reason_code"`
	EvidenceSource string `json:"evidence_source,omitempty"`
	EvidencePath   string `json:"evidence_path,omitempty"`
}

type RepairSkip struct {
	SessionID  string `json:"session_id,omitempty"`
	ReasonCode string `json:"reason_code"`
	Message    string `json:"message"`
}

type RepairCounts struct {
	SessionsPlanned     int64 `json:"sessions_planned"`
	ObservationsPlanned int64 `json:"observations_planned"`
	PromptsPlanned      int64 `json:"prompts_planned"`
	SessionsApplied     int64 `json:"sessions_applied"`
	ObservationsApplied int64 `json:"observations_applied"`
	PromptsApplied      int64 `json:"prompts_applied"`
}

// SyncTargetCleanupAction identifies one sync target doctor can remove without
// deleting its journal payloads.
type SyncTargetCleanupAction struct {
	TargetKey           string `json:"target_key"`
	RetargetedMutations int64  `json:"retargeted_mutations"`
	RetainedMutations   int64  `json:"retained_mutations"`
	StateRemoved        bool   `json:"state_removed"`
}

type RepairPlan struct {
	Project             string                             `json:"project"`
	Check               string                             `json:"check"`
	Mode                RepairMode                         `json:"mode"`
	Status              string                             `json:"status"`
	Actions             []ProjectReclassifyAction          `json:"actions"`
	TargetActions       []SyncTargetCleanupAction          `json:"target_actions,omitempty"`
	PlaceholderSessions []store.OrphanedSessionPlaceholder `json:"placeholder_sessions,omitempty"`
	Skipped             []RepairSkip                       `json:"skipped,omitempty"`
	Counts              RepairCounts                       `json:"counts"`
	BackupPath          string                             `json:"backup_path,omitempty"`
}

func BuildRepairPlan(ctx context.Context, scope Scope, report Report, check string, mode RepairMode) (RepairPlan, error) {
	_ = ctx
	project := normalizeProjectName(scope.Project)
	check = strings.TrimSpace(check)
	plan := RepairPlan{Project: project, Check: check, Mode: mode, Status: "planned", Actions: []ProjectReclassifyAction{}}
	switch mode {
	case RepairModePlan:
		plan.Status = "planned"
	case RepairModeDryRun:
		plan.Status = "dry_run"
	case RepairModeApply:
		plan.Status = "planned"
	default:
		return RepairPlan{}, fmt.Errorf("unsupported repair mode %q", mode)
	}

	switch check {
	case CheckSessionProjectDirectoryMismatch:
		planDirectoryMismatchRepair(&plan, report)
	case CheckManualSessionNameProjectMismatch:
		if err := planManualSessionRepair(&plan, scope); err != nil {
			return RepairPlan{}, err
		}
	case CheckInvalidSessionIdentity:
		planInvalidSessionIdentityRepair(&plan, report)
	case CheckOrphanedObservationSession:
		planOrphanedObservationSessionRepair(&plan, report)
	case CheckSyncTargetClosedSpace:
		if err := planForeignSyncTargetCleanup(&plan, scope); err != nil {
			return RepairPlan{}, err
		}
	default:
		return RepairPlan{}, fmt.Errorf("unsupported repair check %q", check)
	}

	dedupeAndSortRepairPlan(&plan)
	if len(plan.Actions) == 0 && len(plan.TargetActions) == 0 && len(plan.PlaceholderSessions) == 0 {
		plan.Status = "noop"
	}
	return plan, nil
}

func planForeignSyncTargetCleanup(plan *RepairPlan, scope Scope) error {
	cleanup, err := scope.Store.CleanupForeignSyncTargets(false)
	if err != nil {
		return err
	}
	for _, action := range cleanup.Actions {
		plan.TargetActions = append(plan.TargetActions, SyncTargetCleanupAction{TargetKey: action.TargetKey, RetargetedMutations: action.RetargetedMutations, RetainedMutations: action.RetainedMutations, StateRemoved: action.StateRemoved})
	}
	return nil
}

// planOrphanedObservationSessionRepair turns orphaned-session findings into
// placeholder actions, grouping evidence by session ID so a session referenced
// from multiple normalized projects is skipped deterministically instead of
// being attached to an arbitrary project.
func planOrphanedObservationSessionRepair(plan *RepairPlan, report Report) {
	candidates := map[string]store.OrphanedSessionPlaceholder{}
	ambiguous := map[string]bool{}
	for _, check := range report.Checks {
		for _, finding := range check.Findings {
			if finding.ReasonCode != CheckOrphanedObservationSession {
				continue
			}
			var evidence store.OrphanedObservationSessionEvidence
			if err := json.Unmarshal(finding.Evidence, &evidence); err != nil {
				plan.Skipped = append(plan.Skipped, RepairSkip{ReasonCode: "invalid_doctor_evidence", Message: err.Error()})
				continue
			}
			project := normalizeProjectName(evidence.Project)
			if evidence.SessionID == "" || project == "" || evidence.FirstObservedAt == "" {
				plan.Skipped = append(plan.Skipped, RepairSkip{SessionID: evidence.SessionID, ReasonCode: "invalid_orphaned_session_evidence", Message: "orphaned session repair requires a non-blank session ID, project, and first observation timestamp"})
				continue
			}
			candidate := store.OrphanedSessionPlaceholder{SessionID: evidence.SessionID, Project: project, ObservationCount: evidence.ObservationCount, StartedAt: evidence.FirstObservedAt}
			if existing, found := candidates[candidate.SessionID]; found && existing.Project != candidate.Project {
				ambiguous[candidate.SessionID] = true
				continue
			}
			candidates[candidate.SessionID] = candidate
		}
	}
	for sessionID, candidate := range candidates {
		if ambiguous[sessionID] {
			plan.Skipped = append(plan.Skipped, RepairSkip{SessionID: sessionID, ReasonCode: "ambiguous_orphaned_session_project", Message: "the same missing session ID is referenced by multiple projects"})
			continue
		}
		plan.PlaceholderSessions = append(plan.PlaceholderSessions, candidate)
	}
	sort.Slice(plan.PlaceholderSessions, func(i, j int) bool {
		return plan.PlaceholderSessions[i].SessionID < plan.PlaceholderSessions[j].SessionID
	})
}

func planInvalidSessionIdentityRepair(plan *RepairPlan, report Report) {
	for _, check := range report.Checks {
		for _, finding := range check.Findings {
			switch finding.ReasonCode {
			case CheckInvalidSessionIdentity:
				plan.Skipped = append(plan.Skipped, RepairSkip{
					ReasonCode: "cannot_repair_without_explicit_canonical_session_id",
					Message:    "cannot repair without explicit canonical session ID; no supported repair input exists",
				})
			case ReasonQuarantinedPulledSessionIdentity:
				// The pull already skipped this mutation and advanced its
				// cursor. Nothing local is broken, so repair reports it instead
				// of leaving an unexplained noop next to a doctor finding.
				plan.Skipped = append(plan.Skipped, RepairSkip{
					ReasonCode: ReasonQuarantinedPulledSessionIdentity,
					Message:    "pulled session mutation was quarantined with a blank identity; it can only be applied once the remote side publishes a canonical session ID",
				})
			}
		}
	}
}

func planDirectoryMismatchRepair(plan *RepairPlan, report Report) {
	for _, check := range report.Checks {
		for _, finding := range check.Findings {
			var ev struct {
				SessionID              string `json:"session_id"`
				SessionProject         string `json:"session_project"`
				DirectoryProject       string `json:"directory_project"`
				DirectoryProjectSource string `json:"directory_project_source"`
				DirectoryProjectPath   string `json:"directory_project_path"`
			}
			if err := json.Unmarshal(finding.Evidence, &ev); err != nil {
				plan.Skipped = append(plan.Skipped, RepairSkip{ReasonCode: "invalid_doctor_evidence", Message: err.Error()})
				continue
			}
			from := normalizeProjectName(ev.SessionProject)
			to := normalizeProjectName(ev.DirectoryProject)
			if !isTrustedDirectoryEvidence(ev.DirectoryProjectSource) {
				plan.Skipped = append(plan.Skipped, RepairSkip{SessionID: ev.SessionID, ReasonCode: "untrusted_directory_evidence", Message: "directory evidence is not git_remote or git_root"})
				continue
			}
			if ev.SessionID == "" || from == "" || to == "" || from == to || from != plan.Project {
				plan.Skipped = append(plan.Skipped, RepairSkip{SessionID: ev.SessionID, ReasonCode: "invalid_reclassification_evidence", Message: "doctor evidence does not describe a supported project move"})
				continue
			}
			plan.Actions = append(plan.Actions, ProjectReclassifyAction{SessionID: ev.SessionID, FromProject: from, ToProject: to, ReasonCode: finding.ReasonCode, EvidenceSource: ev.DirectoryProjectSource, EvidencePath: ev.DirectoryProjectPath})
		}
	}
}

func planManualSessionRepair(plan *RepairPlan, scope Scope) error {
	sessions, err := scope.Store.ListDiagnosticSessions("")
	if err != nil {
		return err
	}
	known := make(map[string]bool)
	for _, session := range sessions {
		project := normalizeProjectName(session.Project)
		if project != "" {
			known[project] = true
		}
	}
	for _, session := range sessions {
		from := normalizeProjectName(session.Project)
		if from != plan.Project {
			continue
		}
		to, knownManualTarget := knownManualSessionTarget(session.Name, known)
		if to == "" || from == to {
			continue
		}
		if !knownManualTarget {
			plan.Skipped = append(plan.Skipped, RepairSkip{SessionID: session.ID, ReasonCode: "manual_name_unknown_project", Message: "manual session suffix is not a known local project"})
			continue
		}
		plan.Actions = append(plan.Actions, ProjectReclassifyAction{SessionID: session.ID, FromProject: from, ToProject: to, ReasonCode: CheckManualSessionNameProjectMismatch})
	}
	return nil
}

func isTrustedDirectoryEvidence(source string) bool {
	switch strings.TrimSpace(source) {
	case "git_remote", "git_root":
		return true
	default:
		return false
	}
}

func dedupeAndSortRepairPlan(plan *RepairPlan) {
	seen := map[string]ProjectReclassifyAction{}
	for _, action := range plan.Actions {
		key := action.SessionID + "\x00" + action.FromProject + "\x00" + action.ToProject
		seen[key] = action
	}
	plan.Actions = plan.Actions[:0]
	for _, action := range seen {
		plan.Actions = append(plan.Actions, action)
	}
	sort.Slice(plan.Actions, func(i, j int) bool { return plan.Actions[i].SessionID < plan.Actions[j].SessionID })
	targets := map[string]SyncTargetCleanupAction{}
	for _, action := range plan.TargetActions {
		targets[action.TargetKey] = action
	}
	plan.TargetActions = plan.TargetActions[:0]
	for _, action := range targets {
		plan.TargetActions = append(plan.TargetActions, action)
	}
	sort.Slice(plan.TargetActions, func(i, j int) bool { return plan.TargetActions[i].TargetKey < plan.TargetActions[j].TargetKey })
	sort.Slice(plan.Skipped, func(i, j int) bool {
		if plan.Skipped[i].SessionID == plan.Skipped[j].SessionID {
			return plan.Skipped[i].ReasonCode < plan.Skipped[j].ReasonCode
		}
		return plan.Skipped[i].SessionID < plan.Skipped[j].SessionID
	})
}
