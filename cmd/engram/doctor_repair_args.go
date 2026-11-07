package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/diagnostic"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

type doctorRepairOptions struct {
	project, check                      string
	mode                                diagnostic.RepairMode
	replacementID, sourceID             string
	sourceSelected, replacementSelected bool
	help                                bool
}

// parseDoctorRepairArgs validates options without output, process exits or
// opening a store. Help short-circuits only when encountered as an argument.
func parseDoctorRepairArgs(args []string) (doctorRepairOptions, error) {
	var options doctorRepairOptions
	modeCount := 0
	identityFlags := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--replacement-id", "--source-id":
			if i+1 >= len(args) {
				return options, errors.New(args[i] + " requires a value")
			}
			identityFlags = true
			if args[i] == "--replacement-id" {
				options.replacementSelected = true
				options.replacementID = args[i+1]
			} else {
				options.sourceID = args[i+1]
				options.sourceSelected = true
			}
			i++
		case "--project":
			if i+1 >= len(args) {
				return options, errors.New("--project requires a value")
			}
			options.project = args[i+1]
			i++
		case "--check":
			if i+1 >= len(args) {
				return options, errors.New("--check requires a value")
			}
			options.check = args[i+1]
			i++
		case "--plan":
			options.mode = diagnostic.RepairModePlan
			modeCount++
		case "--dry-run":
			options.mode = diagnostic.RepairModeDryRun
			modeCount++
		case "--apply":
			options.mode = diagnostic.RepairModeApply
			modeCount++
		case "--help", "-h", "help":
			options.help = true
			return options, nil
		default:
			return options, fmt.Errorf("unknown doctor repair argument %q", args[i])
		}
	}

	options.project, _ = store.NormalizeProject(options.project)
	options.project = strings.TrimSpace(options.project)
	options.check = strings.TrimSpace(options.check)
	if options.project == "" && options.check != diagnostic.CheckSyncMutationRequiredFields && options.check != diagnostic.CheckOrphanedPendingRelations {
		return options, errors.New("--project is required")
	}
	if options.check == "" {
		return options, errors.New("--check is required")
	}
	if modeCount == 0 && options.check == diagnostic.CheckSyncMutationRequiredFields {
		options.mode = diagnostic.RepairModeDryRun
	} else if modeCount != 1 {
		return options, errors.New("exactly one of --plan, --dry-run, or --apply is required")
	}
	if identityFlags && options.check != diagnostic.CheckInvalidSessionIdentity {
		return options, errors.New("identity flags require --check invalid_session_identity")
	}
	if options.sourceSelected && !options.replacementSelected {
		return options, errors.New("--source-id requires --replacement-id")
	}
	if !diagnostic.IsRepairableCode(options.check) {
		if _, err := diagnostic.DefaultRegistry().Lookup(options.check); err == nil {
			return options, errors.New(options.check + " is a diagnostic-only check with no repair; run engram doctor --check " + options.check)
		}
		return options, errors.New("unsupported repair check " + options.check)
	}

	return options, nil
}
