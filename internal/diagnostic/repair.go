package diagnostic

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	RepairModePlan   RepairMode = "plan"
	RepairModeDryRun RepairMode = "dry_run"
	RepairModeApply  RepairMode = "apply"
)

type RepairMode string

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

type RepairPlan struct {
	Project    string                    `json:"project"`
	Check      string                    `json:"check"`
	Mode       RepairMode                `json:"mode"`
	Status     string                    `json:"status"`
	Actions    []ProjectReclassifyAction `json:"actions"`
	Skipped    []RepairSkip              `json:"skipped,omitempty"`
	Counts     RepairCounts              `json:"counts"`
	BackupPath string                    `json:"backup_path,omitempty"`
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
	default:
		return RepairPlan{}, fmt.Errorf("unsupported repair check %q", check)
	}

	dedupeAndSortRepairPlan(&plan)
	if len(plan.Actions) == 0 {
		plan.Status = "noop"
	}
	return plan, nil
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
	known := map[string]bool{}
	byProject := make([]string, 0)
	for _, session := range sessions {
		project := normalizeProjectName(session.Project)
		if project != "" && !known[project] {
			known[project] = true
			byProject = append(byProject, project)
		}
	}
	_ = byProject
	for _, session := range sessions {
		from := normalizeProjectName(session.Project)
		if from != plan.Project {
			continue
		}
		name := strings.TrimSpace(session.Name)
		if !strings.HasPrefix(name, "manual-save-") {
			continue
		}
		to := normalizeProjectName(strings.TrimPrefix(name, "manual-save-"))
		if to == "" || from == to {
			continue
		}
		if !known[to] {
			plan.Skipped = append(plan.Skipped, RepairSkip{SessionID: session.ID, ReasonCode: "manual_name_unknown_project", Message: "manual session suffix is not a known local project"})
			continue
		}
		if detected, ok := detectSessionDirectoryProject(scope, map[string]DetectedProject{}, session.Directory); ok && isTrustedDirectoryEvidence(detected.Source) && normalizeProjectName(detected.Project) != to {
			plan.Skipped = append(plan.Skipped, RepairSkip{SessionID: session.ID, ReasonCode: "trusted_directory_contradicts_manual_name", Message: "trusted directory evidence points at a different project"})
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
	sort.Slice(plan.Skipped, func(i, j int) bool {
		if plan.Skipped[i].SessionID == plan.Skipped[j].SessionID {
			return plan.Skipped[i].ReasonCode < plan.Skipped[j].ReasonCode
		}
		return plan.Skipped[i].SessionID < plan.Skipped[j].SessionID
	})
}
