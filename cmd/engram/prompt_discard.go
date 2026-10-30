package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

const discardPromptUsage = "usage: engram doctor discard-empty-prompt --project PROJECT --seq SEQ [--dry-run|--apply --backup PATH] [--json]"

// This public report deliberately excludes the Store-bound private snapshot.
type discardPromptReport struct {
	Status             string                `json:"status"`
	Project            string                `json:"project"`
	SelectedSeq        int64                 `json:"selected_seq"`
	PromptID           int64                 `json:"prompt_id"`
	SyncID             string                `json:"sync_id"`
	SessionID          string                `json:"session_id"`
	SourceInboxID      string                `json:"source_inbox_id"`
	Sequences          []int64               `json:"sequences"`
	PlannedEffects     []string              `json:"planned_effects"`
	Tombstone          bool                  `json:"tombstone"`
	DeleteQueued       bool                  `json:"delete_queued"`
	DeleteSeq          int64                 `json:"delete_seq"`
	BackupPath         string                `json:"backup_path"`
	BackupPathMeaning  string                `json:"backup_path_meaning,omitempty"`
	RemoteConfirmation bool                  `json:"remote_confirmation"`
	Blocker            *discardPromptBlocker `json:"blocker,omitempty"`
}

type discardPromptBlocker struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func writeDiscardPromptReport(report discardPromptReport, jsonOut bool) {
	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(report)
		return
	}
	// Output is best-effort, as in the JSON path; it does not change command outcomes.
	_, _ = fmt.Fprintf(os.Stdout, "status: %s\nproject: %s\nselected_seq: %d\nprompt_id: %d\nsync_id: %s\nsession_id: %s\nsource_inbox_id: %s\nsequences: %v\nplanned_effects: %v\ntombstone: %t\ndelete_queued: %t\ndelete_seq: %d\n",
		report.Status, report.Project, report.SelectedSeq, report.PromptID,
		report.SyncID, report.SessionID, report.SourceInboxID, report.Sequences,
		report.PlannedEffects, report.Tombstone, report.DeleteQueued, report.DeleteSeq)
	if report.BackupPath != "" {
		_, _ = fmt.Fprintf(os.Stdout, "backup_path: %s (%s)\n", report.BackupPath, report.BackupPathMeaning)
	}
	if report.Blocker != nil {
		_, _ = fmt.Fprintf(os.Stdout, "blocker: %s: %s\n", report.Blocker.Code, report.Blocker.Detail)
	}
	_, _ = fmt.Fprintln(os.Stdout, "NO REMOTE CONFIRMATION: older in-flight sends or other devices may write afterward.")
}

func cmdDoctorDiscardEmptyPrompt(cfg store.Config) {
	args := os.Args[3:]
	jsonOut := false
	for _, arg := range args {
		if arg == "--json" {
			jsonOut = true
		}
	}
	report := discardPromptReport{Status: "error"}
	emit := func(err error) {
		if err != nil {
			report.Blocker = &discardPromptBlocker{Code: "command_error", Detail: err.Error()}
			var blocker *store.LegacyEmptyPromptDiscardBlocker
			if errors.As(err, &blocker) {
				report.Blocker = &discardPromptBlocker{Code: blocker.Code, Detail: blocker.Detail}
				report.Status = "blocked"
			}
		}
		writeDiscardPromptReport(report, jsonOut)
		if err != nil {
			exitFunc(1)
		}
	}

	seen := map[string]bool{}
	project, seqText, backup := "", "", ""
	apply, dry := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--help" || arg == "-h" || arg == "help" {
			_, _ = fmt.Fprintln(os.Stdout, discardPromptUsage)
			return
		}
		if seen[arg] {
			emit(fmt.Errorf("duplicate argument %q", arg))
			return
		}
		seen[arg] = true
		switch arg {
		case "--json":
		case "--apply":
			apply = true
		case "--dry-run":
			dry = true
		case "--project", "--seq", "--backup":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				emit(fmt.Errorf("%s requires a value", arg))
				return
			}
			i++
			switch arg {
			case "--project":
				project = args[i]
			case "--seq":
				seqText = args[i]
			case "--backup":
				backup = args[i]
			}
		default:
			emit(fmt.Errorf("unknown argument %q", arg))
			return
		}
	}

	report.Project = project
	seq, err := strconv.ParseInt(seqText, 10, 64)
	report.SelectedSeq = seq
	if strings.TrimSpace(project) == "" || err != nil || seq <= 0 {
		emit(fmt.Errorf("explicit project and positive int64 --seq required"))
		return
	}
	if apply && dry {
		emit(fmt.Errorf("--apply and --dry-run are mutually exclusive"))
		return
	}
	if (apply && strings.TrimSpace(backup) == "") || (!apply && seen["--backup"]) {
		emit(fmt.Errorf("--backup is required with --apply and forbidden without it"))
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		emit(err)
		return
	}
	// Closing the command-owned store must not replace the reported operation outcome.
	defer func() { _ = s.Close() }()
	resolved, err := resolveCLIProject(s, project, false)
	if err != nil {
		emit(err)
		return
	}
	report.Project = resolved
	plan, err := s.PlanLegacyEmptyPromptDiscard(resolved, seq)
	report.PromptID = plan.PromptID
	report.SyncID = plan.SyncID
	report.SessionID = plan.SessionID
	report.SourceInboxID = plan.SourceInboxID
	report.Sequences = plan.Sequences
	if err != nil {
		emit(err)
		return
	}
	report.PlannedEffects = []string{
		"supersede_exact_blank_upserts", "create_prompt_tombstone",
		"delete_canonical_prompt_and_fts", "enqueue_one_prompt_delete",
	}
	report.Status = "dry_run"
	if apply {
		// Never deserialize a preview: this invocation plans current state and
		// passes the same in-memory, Store-bound plan to the Store's revalidation.
		result, applyErr := s.ApplyLegacyEmptyPromptDiscard(plan, backup)
		report.BackupPath = result.BackupPath
		if applyErr != nil {
			report.Status = "error"
			if result.BackupPath != "" {
				report.BackupPathMeaning = "INTENDED destination; may contain a backup or empty reservation; not proof of a valid backup"
			}
			emit(applyErr)
			return
		}
		report.Status = result.Status
		report.Sequences = result.Sequences
		report.DeleteSeq = result.DeleteSeq
		report.Tombstone = true
		report.DeleteQueued = true
		report.BackupPathMeaning = "successful backup"
	}
	emit(nil)
}
