package diagnostic

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

type mutationRepairCall struct {
	name, project, target string
	apply                 bool
}

type mutationRepairStore struct {
	calls        []mutationRepairCall
	appliedCalls []string
	failAt       int
	err          error
	directories  []store.SyncMutationDirectoryRepairAction
	titles       store.SyncMutationTitleRepairReport
	superseded   store.SyncMutationSupersedeReport
	quarantine   store.SyncMutationQuarantineReport
	source       store.ObservationSourceTitleRepairReport
}

func (s *mutationRepairStore) record(name, project, target string, apply bool) error {
	s.calls = append(s.calls, mutationRepairCall{name, project, target, apply})
	if len(s.calls) == s.failAt {
		return s.err
	}
	if apply {
		s.appliedCalls = append(s.appliedCalls, name)
	}
	return nil
}

func (s *mutationRepairStore) RepairPendingSessionDirectories(project string, apply bool) ([]store.SyncMutationDirectoryRepairAction, error) {
	return s.directories, s.record("directory", project, "", apply)
}
func (s *mutationRepairStore) RepairObservationMutationTitles(project string, apply bool) (store.SyncMutationTitleRepairReport, error) {
	return s.titles, s.record("title", project, "", apply)
}
func (s *mutationRepairStore) SupersedeUnenrolledLegacyMutations(target, project string, apply bool) (store.SyncMutationSupersedeReport, error) {
	return s.superseded, s.record("supersede", project, target, apply)
}
func (s *mutationRepairStore) QuarantineIrreparableSyncMutations(target, project string, apply bool) (store.SyncMutationQuarantineReport, error) {
	return s.quarantine, s.record("quarantine", project, target, apply)
}
func (s *mutationRepairStore) RepairObservationSourceTitles(project string, apply bool) (store.ObservationSourceTitleRepairReport, error) {
	return s.source, s.record("source", project, "", apply)
}

func TestRepairSyncMutationRequiredFieldsOrderModesAndErrors(t *testing.T) {
	const project = "mixed_case_project"
	for _, mode := range []RepairMode{RepairModePlan, RepairModeDryRun, RepairModeApply} {
		for failAt := 0; failAt <= 5; failAt++ {
			t.Run(fmt.Sprintf("%s/failure-%d", mode, failAt), func(t *testing.T) {
				apply := mode == RepairModeApply
				s := &mutationRepairStore{
					failAt: failAt, err: errors.New("unchanged repair error"),
					directories: []store.SyncMutationDirectoryRepairAction{{Seq: 1}},
					titles:      store.SyncMutationTitleRepairReport{Actions: []store.SyncMutationTitleRepairAction{{Seq: 2}}},
					superseded:  store.SyncMutationSupersedeReport{Actions: []store.SyncMutationSupersedeAction{{Seq: 3}}},
					quarantine:  store.SyncMutationQuarantineReport{Project: project, Applied: apply, Actions: []store.SyncMutationQuarantineAction{{Seq: 1}, {Seq: 2}, {Seq: 3}, {Seq: 4}}},
					source: store.ObservationSourceTitleRepairReport{
						Actions: []store.ObservationSourceTitleRepairAction{{ID: 5, Title: "restored"}}, BackupPath: "existing-backup.sqlite",
						Unrepairable: []store.ObservationSourceUnrepairable{{Reason: "manual investigation"}},
					},
				}
				result, err := RepairSyncMutationRequiredFields(s, project, mode)
				wantCalls := []mutationRepairCall{
					{"directory", project, "", apply}, {"title", project, "", apply},
					{"supersede", project, store.DefaultSyncTargetKey, apply},
					{"quarantine", project, store.DefaultSyncTargetKey, apply}, {"source", project, "", apply},
				}
				if failAt > 0 {
					wantCalls = wantCalls[:failAt]
					if err != s.err || !reflect.DeepEqual(result, SyncMutationRepairResult{}) {
						t.Fatalf("error/result=%v/%+v, want original error and no result", err, result)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					wantActions := []store.SyncMutationQuarantineAction{{Seq: 4}}
					if apply {
						wantActions = []store.SyncMutationQuarantineAction{{Seq: 1}, {Seq: 2}, {Seq: 3}, {Seq: 4}}
					}
					want := SyncMutationRepairResult{
						Quarantine:       store.SyncMutationQuarantineReport{Project: project, Applied: apply, Actions: wantActions},
						DirectoryRepairs: s.directories, Repairs: s.titles.Actions, Superseded: s.superseded.Actions,
						SourceRepairs: s.source.Actions, SourceRepairBackupPath: s.source.BackupPath, SourceUnrepairable: s.source.Unrepairable,
					}
					if !reflect.DeepEqual(result, want) {
						t.Fatalf("result=%+v, want %+v", result, want)
					}
				}
				if !reflect.DeepEqual(s.calls, wantCalls) {
					t.Fatalf("calls=%+v, want %+v", s.calls, wantCalls)
				}
				var wantApplied []string
				if apply {
					completed := len(wantCalls)
					if failAt > 0 {
						completed--
					}
					for _, call := range wantCalls[:completed] {
						wantApplied = append(wantApplied, call.name)
					}
				}
				if !reflect.DeepEqual(s.appliedCalls, wantApplied) {
					t.Fatalf("completed apply calls=%v, want %v", s.appliedCalls, wantApplied)
				}
			})
		}
	}
}

func TestRepairSyncMutationRequiredFieldsAppliedAggregation(t *testing.T) {
	for _, active := range []string{"none", "directory", "title", "supersede", "quarantine", "source"} {
		t.Run(active, func(t *testing.T) {
			s := &mutationRepairStore{
				quarantine: store.SyncMutationQuarantineReport{Applied: true},
				source: store.ObservationSourceTitleRepairReport{
					Applied: true, BackupPath: "forwarded-backup.sqlite",
					Unrepairable: []store.ObservationSourceUnrepairable{{Reason: "not an applied action"}},
				},
			}
			switch active {
			case "directory":
				s.directories = []store.SyncMutationDirectoryRepairAction{{Seq: 1}}
			case "title":
				s.titles.Actions = []store.SyncMutationTitleRepairAction{{Seq: 1}}
			case "supersede":
				s.superseded.Actions = []store.SyncMutationSupersedeAction{{Seq: 1}}
			case "quarantine":
				s.quarantine.Actions = []store.SyncMutationQuarantineAction{{Seq: 1}}
			case "source":
				s.source.Actions = []store.ObservationSourceTitleRepairAction{{ID: 1}}
			}
			result, err := RepairSyncMutationRequiredFields(s, "", RepairModeApply)
			if err != nil || result.Quarantine.Applied != (active != "none") {
				t.Fatalf("result=%+v, err=%v; applied should depend only on action lists", result, err)
			}
		})
	}
}
