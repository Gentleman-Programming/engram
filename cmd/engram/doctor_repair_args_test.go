package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/diagnostic"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Pin validation order and help at the public command boundary before any
// store can open. These expectations also characterize the pre-refactor CLI.
func TestDoctorRepairArgsCLIContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"empty", nil, "--project is required"},
		{"project value", []string{"--project"}, "--project requires a value"},
		{"check value", []string{"--check"}, "--check requires a value"},
		{"source value", []string{"--source-id"}, "--source-id requires a value"},
		{"replacement value", []string{"--replacement-id"}, "--replacement-id requires a value"},
		{"unknown", []string{"--unknown"}, `unknown doctor repair argument "--unknown"`},
		{"project before check", []string{"--apply"}, "--project is required"},
		{"missing check", []string{"--project", "alpha", "--apply"}, "--check is required"},
		{"mode before unknown check", []string{"--project", "alpha", "--check", "unknown"}, "exactly one of --plan, --dry-run, or --apply is required"},
		{"repeated mode", []string{"--check", "sync_mutation_required_fields", "--plan", "--plan"}, "exactly one of --plan, --dry-run, or --apply is required"},
		{"empty irrelevant identity", []string{"--check", "sync_mutation_required_fields", "--source-id", ""}, "identity flags require --check invalid_session_identity"},
		{"empty source selected", []string{"--project", "alpha", "--check", "invalid_session_identity", "--plan", "--source-id", ""}, "--source-id requires --replacement-id"},
		{"unknown check", []string{"--project", "alpha", "--check", "unknown", "--plan"}, "unsupported repair check unknown"},
		{"early help", []string{"--help", "--unknown"}, ""},
		{"help after incomplete semantic options", []string{"--apply", "-h"}, ""},
		{"help alias", []string{"help"}, ""},
		{"error before help", []string{"--unknown", "--help"}, `unknown doctor repair argument "--unknown"`},
		{"help consumed as value", []string{"--project", "--help"}, "--check is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldStore, oldExit := storeNew, exitFunc
			t.Cleanup(func() { storeNew, exitFunc = oldStore, oldExit })
			storeNew = func(store.Config) (*store.Store, error) {
				t.Fatal("validation/help opened the store")
				return nil, nil
			}
			exitCode := 0
			exitFunc = func(code int) { exitCode = code }
			withArgs(t, append([]string{"engram", "doctor", "repair"}, tc.args...)...)
			stdout, stderr := captureOutput(t, func() { cmdDoctorRepair(store.Config{}) })
			if !strings.Contains(stdout, "usage: engram doctor") {
				t.Fatalf("missing usage: %q", stdout)
			}
			if tc.want == "" {
				if exitCode != 0 || stderr != "" {
					t.Fatalf("help: exit=%d stderr=%q", exitCode, stderr)
				}
			} else if exitCode != 1 || stderr != "engram doctor repair failed: "+tc.want+"\n" {
				t.Fatalf("exit=%d stderr=%q want=%q", exitCode, stderr, tc.want)
			}
		})
	}
}

func TestParseDoctorRepairArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want doctorRepairOptions
	}{
		{"implicit sync dry run", []string{"--check", "sync_mutation_required_fields"}, doctorRepairOptions{check: diagnostic.CheckSyncMutationRequiredFields, mode: diagnostic.RepairModeDryRun}},
		{"explicit sync apply", []string{"--check", "sync_mutation_required_fields", "--apply"}, doctorRepairOptions{check: diagnostic.CheckSyncMutationRequiredFields, mode: diagnostic.RepairModeApply}},
		{"global pending plan", []string{"--check", "orphaned_pending_relations", "--plan"}, doctorRepairOptions{check: diagnostic.CheckOrphanedPendingRelations, mode: diagnostic.RepairModePlan}},
		{"normalized last project", []string{"--project", "first", "--project", " Alpha ", "--check", " invalid_session_identity ", "--dry-run"}, doctorRepairOptions{project: "alpha", check: diagnostic.CheckInvalidSessionIdentity, mode: diagnostic.RepairModeDryRun}},
		{"absent identity", []string{"--project", "alpha", "--check", "invalid_session_identity", "--plan"}, doctorRepairOptions{project: "alpha", check: diagnostic.CheckInvalidSessionIdentity, mode: diagnostic.RepairModePlan}},
		{"explicit empty identity", []string{"--project", "alpha", "--check", "invalid_session_identity", "--plan", "--source-id", "", "--replacement-id", ""}, doctorRepairOptions{project: "alpha", check: diagnostic.CheckInvalidSessionIdentity, mode: diagnostic.RepairModePlan, sourceSelected: true, replacementSelected: true}},
		{"replacement only", []string{"--project", "alpha", "--check", "invalid_session_identity", "--plan", "--replacement-id", " new "}, doctorRepairOptions{project: "alpha", check: diagnostic.CheckInvalidSessionIdentity, mode: diagnostic.RepairModePlan, replacementID: " new ", replacementSelected: true}},
		{"last source and check", []string{"--project", "alpha", "--check", "unknown", "--check", "invalid_session_identity", "--apply", "--source-id", "first", "--source-id", "old", "--replacement-id", "new"}, doctorRepairOptions{project: "alpha", check: diagnostic.CheckInvalidSessionIdentity, mode: diagnostic.RepairModeApply, sourceID: "old", replacementID: "new", sourceSelected: true, replacementSelected: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]string(nil), tc.args...)
			got, err := parseDoctorRepairArgs(tc.args)
			if err != nil || got != tc.want {
				t.Fatalf("options=%+v err=%v want=%+v", got, err, tc.want)
			}
			if !reflect.DeepEqual(before, tc.args) {
				t.Fatal("parser mutated caller arguments")
			}
		})
	}
}
