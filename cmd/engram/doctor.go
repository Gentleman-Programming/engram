package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"encoding/json"
	"github.com/Gentleman-Programming/engram/v2/internal/diagnostic"
	"github.com/Gentleman-Programming/engram/v2/internal/setup"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func cmdDoctor(cfg store.Config) {
	if len(os.Args) > 2 && os.Args[2] == "repair" {
		cmdDoctorRepair(cfg)
		return
	}
	jsonOut := false
	project := ""
	check := ""
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--json":
			jsonOut = true
		case "--project":
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "error: --project requires a value")
				exitFunc(1)
				return
			}
			project = os.Args[i+1]
			i++
		case "--check":
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "error: --check requires a value")
				exitFunc(1)
				return
			}
			check = os.Args[i+1]
			i++
		case "--help", "-h", "help":
			printDoctorUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "error: unknown doctor argument %q\n", os.Args[i])
			printDoctorUsage()
			exitFunc(1)
			return
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()
	if strings.TrimSpace(project) != "" {
		// Doctor can inspect a pending-sync project before it has an observation
		// bucket, so its explicit diagnostic filter is structurally validated but
		// does not require ProjectExists.
		project, err = resolveCLIProject(s, project, false)
		if err != nil {
			fatal(err)
			return
		}
	}

	report, err := runDiagnostics(context.Background(), s, strings.TrimSpace(project), strings.TrimSpace(check))
	if err != nil {
		report = diagnostic.ErrorReport(project, err)
		if jsonOut {
			writeDoctorJSON(report)
		} else {
			fmt.Fprintf(os.Stderr, "engram doctor failed: %s\n", err)
		}
		if errors.Is(err, diagnostic.ErrInvalidCheck) {
			exitFunc(1)
		}
		return
	}

	if strings.TrimSpace(project) == "" && strings.TrimSpace(check) == "" {
		stale, inspectErr := setup.StaleMCPCommands()
		if inspectErr != nil {
			report.Checks = append(report.Checks, diagnostic.CheckResult{
				CheckID: "mcp_inspection_error", Result: "error", Severity: "error",
				ReasonCode: "mcp_inspection_error", Message: "Could not inspect generic MCP registrations: " + inspectErr.Error(),
				Why:          "Client configuration could not be read safely.",
				SafeNextStep: "Review the client configuration and rerun engram doctor.",
			})
			report.Summary.Total++
			report.Summary.Errors++
			report.Status = "error"
		} else if len(stale) > 0 {
			findings := make([]diagnostic.Finding, 0, len(stale))
			for _, item := range stale {
				next := "engram setup " + item.Slug
				evidence, _ := json.Marshal(item)
				findings = append(findings, diagnostic.Finding{CheckID: "stale_mcp_command", Severity: "warning", ReasonCode: "stale_mcp_command", Message: item.Slug + " Engram MCP executable path is missing", Why: "The configured absolute command no longer exists.", Evidence: evidence, SafeNextStep: next, RequiresConfirmation: false})
			}
			report.Checks = append(report.Checks, diagnostic.CheckResult{CheckID: "stale_mcp_command", Result: "warning", Severity: "warning", ReasonCode: "stale_mcp_command", Message: "Generic MCP registrations reference missing Engram binaries.", Why: "A configured absolute command no longer exists.", SafeNextStep: "Run engram setup <slug> for each affected client.", Findings: findings})
			report.Summary.Total++
			report.Summary.Warnings++
			if report.Status == "ok" {
				report.Status = "warning"
			}
		}
	}
	if jsonOut {
		writeDoctorJSON(report)
		return
	}
	renderDoctorText(report)
}

func printDoctorUsage() {
	fmt.Fprintln(os.Stdout, "usage: engram doctor [--json] [--project PROJECT] [--check CODE]")
	fmt.Fprintln(os.Stdout, "       engram doctor repair --project PROJECT --check CODE (--plan|--dry-run|--apply)")
	fmt.Fprintln(os.Stdout, "       engram doctor repair [--project PROJECT] --check "+diagnostic.CheckSyncMutationRequiredFields+" [--plan|--dry-run|--apply] (default: --dry-run)")
	_, _ = fmt.Fprintln(os.Stdout, "       engram doctor repair --project PROJECT --check invalid_session_identity --replacement-id ID [--source-id SOURCE] (--plan|--dry-run|--apply)")
	_, _ = fmt.Fprintln(os.Stdout, "note: --project is required for every repair check except "+diagnostic.CheckSyncMutationRequiredFields+", where it optionally scopes title repair, supersession, quarantine, and source-title repair.")
	fmt.Fprintln(os.Stdout, "checks: "+strings.Join(diagnostic.RegisteredCodes(), ", "))
	_, _ = fmt.Fprintln(os.Stdout, "diagnostic-only checks with no repair: "+strings.Join(diagnosticOnlyCheckCodes(), ", "))
}

func printDoctorRepairUsage() {
	_, _ = fmt.Fprintln(os.Stdout, "usage: engram doctor repair --project PROJECT --check CODE (--plan|--dry-run|--apply)")
	_, _ = fmt.Fprintln(os.Stdout, "       engram doctor repair [--project PROJECT] --check "+diagnostic.CheckSyncMutationRequiredFields+" [--plan|--dry-run|--apply] (default: --dry-run)")
	_, _ = fmt.Fprintln(os.Stdout, "       engram doctor repair --project PROJECT --check invalid_session_identity --replacement-id ID [--source-id SOURCE] (--plan|--dry-run|--apply)")
	_, _ = fmt.Fprintln(os.Stdout, "note: --project is required for every repair check except "+diagnostic.CheckSyncMutationRequiredFields+", where it optionally scopes title repair, supersession, quarantine, and source-title repair.")
	_, _ = fmt.Fprintln(os.Stdout, "repairable checks: "+strings.Join(diagnostic.RepairableCodes(), ", "))
	_, _ = fmt.Fprintln(os.Stdout, "diagnostic-only checks with no repair: "+strings.Join(diagnosticOnlyCheckCodes(), ", "))
}

func diagnosticOnlyCheckCodes() []string {
	repairable := make(map[string]bool, len(diagnostic.RepairableCodes()))
	for _, code := range diagnostic.RepairableCodes() {
		repairable[code] = true
	}

	registered := diagnostic.RegisteredCodes()
	diagnosticOnly := make([]string, 0, len(registered)-len(repairable))
	for _, code := range registered {
		if !repairable[code] {
			diagnosticOnly = append(diagnosticOnly, code)
		}
	}
	return diagnosticOnly
}

func cmdDoctorRepair(cfg store.Config) {
	project := ""
	check := ""
	mode := diagnostic.RepairMode("")
	modeCount := 0
	replacementID := ""
	sourceID := ""
	sourceSelected := false
	identityFlags := false
	replacementSelected := false
	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--replacement-id", "--source-id":
			if i+1 >= len(os.Args) {
				failDoctorRepair(os.Args[i] + " requires a value")
				return
			}
			identityFlags = true
			if os.Args[i] == "--replacement-id" {
				replacementSelected = true
				replacementID = os.Args[i+1]
			} else {
				sourceID = os.Args[i+1]
				sourceSelected = true
			}
			i++
		case "--project":
			if i+1 >= len(os.Args) {
				failDoctorRepair("--project requires a value")
				return
			}
			project = os.Args[i+1]
			i++
		case "--check":
			if i+1 >= len(os.Args) {
				failDoctorRepair("--check requires a value")
				return
			}
			check = os.Args[i+1]
			i++
		case "--plan":
			mode = diagnostic.RepairModePlan
			modeCount++
		case "--dry-run":
			mode = diagnostic.RepairModeDryRun
			modeCount++
		case "--apply":
			mode = diagnostic.RepairModeApply
			modeCount++
		case "--help", "-h", "help":
			printDoctorRepairUsage()
			return
		default:
			failDoctorRepair(fmt.Sprintf("unknown doctor repair argument %q", os.Args[i]))
			return
		}
	}

	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	check = strings.TrimSpace(check)
	if project == "" && check != diagnostic.CheckSyncMutationRequiredFields {
		failDoctorRepair("--project is required")
		return
	}
	if check == "" {
		failDoctorRepair("--check is required")
		return
	}
	if modeCount == 0 && check == diagnostic.CheckSyncMutationRequiredFields {
		mode = diagnostic.RepairModeDryRun
	} else if modeCount != 1 {
		failDoctorRepair("exactly one of --plan, --dry-run, or --apply is required")
		return
	}
	if identityFlags && check != diagnostic.CheckInvalidSessionIdentity {
		failDoctorRepair("identity flags require --check invalid_session_identity")
		return
	}
	if sourceSelected && !replacementSelected {
		failDoctorRepair("--source-id requires --replacement-id")
		return
	}
	if !diagnostic.IsRepairableCode(check) {
		if _, err := diagnostic.DefaultRegistry().Lookup(check); err == nil {
			failDoctorRepair(check + " is a diagnostic-only check with no repair; run engram doctor --check " + check)
			return
		}
		failDoctorRepair("unsupported repair check " + check)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()
	if check == diagnostic.CheckSyncMutationRequiredFields {
		repairs, err := s.RepairObservationMutationTitles(project, mode == diagnostic.RepairModeApply)
		if err != nil {
			failDoctorRepair(err.Error())
			return
		}
		superseded, err := s.SupersedeUnenrolledLegacyMutations(store.DefaultSyncTargetKey, project, mode == diagnostic.RepairModeApply)
		if err != nil {
			failDoctorRepair(err.Error())
			return
		}
		report, err := s.QuarantineIrreparableSyncMutations(store.DefaultSyncTargetKey, project, mode == diagnostic.RepairModeApply)
		if err != nil {
			failDoctorRepair(err.Error())
			return
		}
		if mode != diagnostic.RepairModeApply {
			handledSeqs := make(map[int64]struct{}, len(repairs.Actions)+len(superseded.Actions))
			for _, action := range repairs.Actions {
				handledSeqs[action.Seq] = struct{}{}
			}
			for _, action := range superseded.Actions {
				handledSeqs[action.Seq] = struct{}{}
			}
			remaining := report.Actions[:0]
			for _, action := range report.Actions {
				if _, handled := handledSeqs[action.Seq]; !handled {
					remaining = append(remaining, action)
				}
			}
			report.Actions = remaining
		}
		sourceRepairs, err := s.RepairObservationSourceTitles(project, mode == diagnostic.RepairModeApply)
		if err != nil {
			failDoctorRepair(err.Error())
			return
		}
		if mode == diagnostic.RepairModeApply {
			report.Applied = len(repairs.Actions) > 0 || len(report.Actions) > 0 || len(superseded.Actions) > 0 || len(sourceRepairs.Actions) > 0
		}
		writeDoctorRepairJSON(struct {
			store.SyncMutationQuarantineReport
			Repairs                []store.SyncMutationTitleRepairAction      `json:"repairs"`
			Superseded             []store.SyncMutationSupersedeAction        `json:"superseded"`
			SourceRepairs          []store.ObservationSourceTitleRepairAction `json:"source_repairs"`
			SourceRepairBackupPath string                                     `json:"source_repair_backup_path,omitempty"`
		}{report, repairs.Actions, superseded.Actions, sourceRepairs.Actions, sourceRepairs.BackupPath})
		return
	}

	ctx := context.Background()
	report, err := runDiagnostics(ctx, s, project, check)
	if err != nil {
		failDoctorRepair(err.Error())
		return
	}
	plan, err := buildRepairPlan(ctx, diagnostic.Scope{Store: s, Project: project}, report, check, mode)
	if err != nil {
		failDoctorRepair(err.Error())
		return
	}
	if check == diagnostic.CheckInvalidSessionIdentity && replacementSelected {
		plan = diagnostic.PlanSessionIdentityReplacement(diagnostic.Scope{Store: s, Project: project}, report, plan, sourceID, sourceSelected, replacementID)
		if plan.Status == "blocked" {
			writeDoctorRepairJSON(plan)
			return
		}
		switch mode {
		case diagnostic.RepairModeApply:
			result, err := s.ApplySessionIdentityRepair(*plan.IdentityRepair)
			if err != nil {
				failDoctorRepair(err.Error())
				return
			}
			plan.Status = "applied"
			if len(plan.Skipped) > 0 {
				plan.Status = "partial"
			}
			plan.BackupPath = result.BackupPath
			plan.Counts.SessionsApplied = 1
			plan.Counts.ObservationsApplied = plan.IdentityRepair.Observations
			plan.Counts.PromptsApplied = plan.IdentityRepair.Prompts
		case diagnostic.RepairModePlan:
			plan.Status = "planned"
		default:
			plan.Status = "dry_run"
		}
		writeDoctorRepairJSON(plan)
		return
	}
	if check == diagnostic.CheckOrphanedObservationSession {
		plan.Counts.SessionsPlanned = int64(len(plan.PlaceholderSessions))
		for _, action := range plan.PlaceholderSessions {
			plan.Counts.ObservationsPlanned += action.ObservationCount
		}
		if mode == diagnostic.RepairModeApply && len(plan.PlaceholderSessions) > 0 {
			applied, err := s.RestoreOrphanedObservationSessions(plan.PlaceholderSessions)
			if err != nil {
				failDoctorRepair(err.Error())
				return
			}
			if len(applied) > 0 {
				plan.Status = "applied"
			} else {
				plan.Status = "noop"
			}
			for _, action := range applied {
				plan.Counts.SessionsApplied++
				plan.Counts.ObservationsApplied += action.ObservationCount
			}
		}
		writeDoctorRepairJSON(plan)
		return
	}
	if check == diagnostic.CheckSyncTargetClosedSpace {
		cleanup, err := s.CleanupForeignSyncTargets(mode == diagnostic.RepairModeApply)
		if err != nil {
			failDoctorRepair(err.Error())
			return
		}
		plan.TargetActions = make([]diagnostic.SyncTargetCleanupAction, 0, len(cleanup.Actions))
		if mode == diagnostic.RepairModeApply && len(cleanup.Actions) > 0 {
			plan.Status = "applied"
		}
		for _, action := range cleanup.Actions {
			plan.TargetActions = append(plan.TargetActions, diagnostic.SyncTargetCleanupAction{TargetKey: action.TargetKey, RetargetedMutations: action.RetargetedMutations, RetainedMutations: action.RetainedMutations, StateRemoved: action.StateRemoved})
			if action.RetainedMutations > 0 && mode == diagnostic.RepairModeApply {
				plan.Status = "blocked"
				if cleanup.Applied {
					plan.Status = "partial"
				}
			}
		}
		writeDoctorRepairJSON(plan)
		return
	}
	actions := make([]store.SessionProjectReclassification, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		actions = append(actions, store.SessionProjectReclassification{SessionID: action.SessionID, FromProject: action.FromProject, ToProject: action.ToProject})
	}
	if mode == diagnostic.RepairModeApply && len(actions) > 0 {
		counts, err := s.EstimateSessionProjectReclassification(actions)
		if err != nil {
			failDoctorRepair(err.Error())
			return
		}
		plan.Counts.SessionsPlanned = counts.Sessions
		plan.Counts.ObservationsPlanned = counts.Observations
		plan.Counts.PromptsPlanned = counts.Prompts
		result, err := s.ApplySessionProjectReclassification(actions)
		if err != nil {
			failDoctorRepair(err.Error())
			return
		}
		plan.Status = "applied"
		plan.BackupPath = result.BackupPath
		plan.Counts.SessionsApplied = result.Counts.Sessions
		plan.Counts.ObservationsApplied = result.Counts.Observations
		plan.Counts.PromptsApplied = result.Counts.Prompts
	} else {
		counts, err := s.EstimateSessionProjectReclassification(actions)
		if err != nil {
			failDoctorRepair(err.Error())
			return
		}
		plan.Counts.SessionsPlanned = counts.Sessions
		plan.Counts.ObservationsPlanned = counts.Observations
		plan.Counts.PromptsPlanned = counts.Prompts
	}
	writeDoctorRepairJSON(plan)
}

func failDoctorRepair(message string) {
	fmt.Fprintln(os.Stderr, "engram doctor repair failed: "+message)
	printDoctorRepairUsage()
	exitFunc(1)
}

func writeDoctorRepairJSON(value any) {
	out, err := jsonMarshalIndent(value, "", "  ")
	if err != nil {
		fatal(err)
		return
	}
	fmt.Println(string(out))
}

func writeDoctorJSON(report diagnostic.Report) {
	out, err := jsonMarshalIndent(report, "", "  ")
	if err != nil {
		fatal(err)
		return
	}
	fmt.Println(string(out))
}

func renderDoctorText(report diagnostic.Report) {
	fmt.Printf("Engram Doctor: %s\n", report.Status)
	if report.Project != "" {
		fmt.Printf("Project: %s\n", report.Project)
	}
	fmt.Printf("Checks: %d ok=%d warnings=%d blocked=%d errors=%d\n\n", report.Summary.Total, report.Summary.OK, report.Summary.Warnings, report.Summary.Blocked, report.Summary.Errors)
	for _, check := range report.Checks {
		fmt.Printf("[%s] %s — %s\n", check.Result, check.CheckID, check.Message)
		if check.Why != "" {
			fmt.Printf("  why: %s\n", check.Why)
		}
		if check.SafeNextStep != "" {
			fmt.Printf("  next: %s\n", check.SafeNextStep)
		}
		for _, finding := range check.Findings {
			fmt.Printf("  - %s: %s\n", finding.ReasonCode, finding.Message)
			if finding.SafeNextStep != "" {
				fmt.Printf("    next: %s\n", finding.SafeNextStep)
			}
			if len(finding.Evidence) > 0 {
				fmt.Printf("    evidence: %s\n", string(finding.Evidence))
			}
		}
	}
}
