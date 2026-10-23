package store

import (
	"reflect"
	"testing"
)

func safetyPending(t *testing.T, s *Store) []SyncMutation {
	t.Helper()
	rows, err := s.ListPendingProjectMutations("alpha")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestProjectTombstoneLegacyProjectSpelling(t *testing.T) {
	for _, scope := range []string{"alpha", ""} {
		for _, enrolled := range []bool{false, true} {
			name := scope
			if name == "" {
				name = "unscoped"
			}
			if enrolled {
				name += "/enrolled"
			}
			t.Run(name, func(t *testing.T) {
				s := newTestStore(t)
				check := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				for _, project := range []string{"alpha", "beta"} {
					check(s.EnrollProject(project))
					check(s.CreateSession(project+"-session", project, "/synthetic"))
					_, err := s.AddObservation(AddObservationParams{SessionID: project + "-session", Type: "decision", Title: "old", Content: "deleted content", Project: project, Scope: "project"})
					check(err)
					check(s.UnenrollProject(project))
					_, err = s.DeleteProject(project, true)
					check(err)
				}
				// Current public writers normalize project names. Recreate only the
				// historical journal spelling emitted by legacy startup backfills.
				_, err := s.DB().Exec(`UPDATE sync_mutations SET project = 'Alpha' WHERE project = 'alpha'`)
				check(err)
				if enrolled {
					check(s.EnrollProject("alpha"))
				}
				before, err := s.ListPendingProjectMutations("")
				check(err)
				dry, err := s.SupersedeUnenrolledLegacyMutations(DefaultSyncTargetKey, scope, false)
				check(err)
				unchanged, err := s.ListPendingProjectMutations("")
				check(err)
				if !reflect.DeepEqual(before, unchanged) {
					t.Error("dry-run changed journal")
				}
				applied, err := s.SupersedeUnenrolledLegacyMutations(DefaultSyncTargetKey, scope, true)
				check(err)
				if !reflect.DeepEqual(dry.Actions, applied.Actions) {
					t.Error("dry-run/apply actions differ")
				}
				after, err := s.ListPendingProjectMutations("")
				check(err)
				checked := 0
				for _, original := range before {
					if original.Op != SyncOpUpsert || original.Disposition != SyncMutationDispositionPending {
						continue
					}
					checked++
					want := SyncMutationDispositionPending
					if (original.Project == "Alpha" && !enrolled) || (original.Project == "beta" && scope == "") {
						want = SyncMutationDispositionSuperseded
					}
					found := false
					for _, current := range after {
						if current.Seq == original.Seq {
							found = true
							if current.Disposition != want {
								t.Errorf("project %s seq %d disposition=%s want=%s", original.Project, original.Seq, current.Disposition, want)
							}
							if want == SyncMutationDispositionPending && !reflect.DeepEqual(original, current) {
								t.Error("excluded mutation changed")
							}
						}
					}
					if !found {
						t.Error("journal evidence disappeared")
					}
				}
				if checked != 4 {
					t.Fatalf("expected four historical upserts, got %d", checked)
				}
			})
		}
	}
}

func TestProjectTombstonePreservesNewSameIdentityMutation(t *testing.T) {
	s := newTestStore(t)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(s.EnrollProject("alpha"))
	check(s.CreateSession("same-session", "alpha", "/synthetic"))
	id, err := s.AddObservation(AddObservationParams{SessionID: "same-session", Type: "decision", Title: "original", Content: "old content", Project: "alpha", Scope: "project"})
	check(err)
	original, err := s.GetObservation(id)
	check(err)
	snapshot, err := s.Export()
	check(err)
	check(s.UnenrollProject("alpha"))
	_, err = s.DeleteProject("alpha", true)
	check(err)
	_, err = s.Import(snapshot)
	check(err)
	check(s.EnrollProject("alpha"))
	restored, err := s.Export()
	check(err)
	var restoredID int64
	for _, observation := range restored.Observations {
		if observation.SyncID == original.SyncID {
			restoredID = observation.ID
		}
	}
	if restoredID == 0 {
		t.Fatal("public import did not restore the original sync identity")
	}
	content := "new content after project deletion"
	_, err = s.UpdateObservation(restoredID, UpdateObservationParams{Content: &content})
	check(err)
	check(s.UnenrollProject("alpha"))
	before := safetyPending(t, s)
	var generations []SyncMutation
	for _, mutation := range before {
		if mutation.Entity == SyncEntityObservation && mutation.EntityKey == original.SyncID {
			generations = append(generations, mutation)
		}
	}
	if len(generations) != 2 || generations[0].OccurredAt >= generations[1].OccurredAt {
		t.Fatalf("fixture needs two ordered public mutation generations: %+v", generations)
	}
	// Construct the historical fence deterministically; outcome assertions use
	// the public repair and journal APIs, not direct storage inspection.
	result, err := s.DB().Exec(`UPDATE sync_delete_tombstones SET deleted_at = ? WHERE entity = 'project' AND entity_key = 'alpha'`, generations[0].OccurredAt)
	check(err)
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		t.Fatalf("expected one project fence: %d (%v)", count, err)
	}
	sequence, err := s.MaxPendingSyncMutationSeq(DefaultSyncTargetKey)
	check(err)
	dry, err := s.SupersedeUnenrolledLegacyMutations(DefaultSyncTargetKey, "alpha", false)
	check(err)
	if !reflect.DeepEqual(before, safetyPending(t, s)) {
		t.Error("dry-run changed public pending state")
	}
	if after, err := s.MaxPendingSyncMutationSeq(DefaultSyncTargetKey); err != nil || after != sequence {
		t.Fatalf("dry-run changed sequence: %d -> %d (%v)", sequence, after, err)
	}
	applied, err := s.SupersedeUnenrolledLegacyMutations(DefaultSyncTargetKey, "alpha", true)
	check(err)
	if !reflect.DeepEqual(dry.Actions, applied.Actions) {
		t.Errorf("dry-run/apply disagree: %+v / %+v", dry.Actions, applied.Actions)
	}
	oldPending, freshPending := false, false
	for _, mutation := range safetyPending(t, s) {
		oldPending = oldPending || (mutation.Seq == generations[0].Seq && mutation.Disposition == SyncMutationDispositionPending)
		freshPending = freshPending || (mutation.Seq == generations[1].Seq && mutation.Disposition == SyncMutationDispositionPending)
	}
	if oldPending || !freshPending {
		t.Errorf("time fence lost: old_pending=%t fresh_pending=%t", oldPending, freshPending)
	}
	current, err := s.GetObservation(restoredID)
	check(err)
	if current.Content != content {
		t.Error("repair changed the restored source content")
	}
}

func TestProjectTombstonePreservesRetainedSessionJournal(t *testing.T) {
	for _, dependent := range []string{"observation", "prompt"} {
		t.Run(dependent, func(t *testing.T) {
			s := newTestStore(t)
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			check(s.EnrollProject("alpha"))
			check(s.CreateSession("retained-session", "alpha", "/synthetic"))
			beta := "beta"
			data := &ExportData{Version: currentExportVersion}
			if dependent == "observation" {
				data.Observations = []Observation{{SyncID: "foreign-observation", SessionID: "retained-session", Type: "decision", Title: "beta", Content: "beta content", Project: &beta, Scope: "project", CreatedAt: Now(), UpdatedAt: Now()}}
			} else {
				data.Prompts = []Prompt{{SyncID: "foreign-prompt", SessionID: "retained-session", Content: "beta prompt", Project: beta, CreatedAt: Now()}}
			}
			_, err := s.Import(data)
			check(err)
			check(s.UnenrollProject("alpha"))
			deleted, err := s.DeleteProject("alpha", true)
			check(err)
			if deleted.SessionsDeleted != 0 {
				t.Fatal("cross-project dependency did not preserve the source session")
			}
			before := safetyPending(t, s)
			dry, err := s.SupersedeUnenrolledLegacyMutations(DefaultSyncTargetKey, "alpha", false)
			check(err)
			if !reflect.DeepEqual(before, safetyPending(t, s)) {
				t.Error("dry-run changed retained session journal")
			}
			applied, err := s.SupersedeUnenrolledLegacyMutations(DefaultSyncTargetKey, "alpha", true)
			check(err)
			for _, report := range []SyncMutationSupersedeReport{dry, applied} {
				for _, action := range report.Actions {
					if action.Entity == SyncEntitySession && action.EntityKey == "retained-session" {
						t.Error("repair classified a retained session as deleted")
					}
				}
			}
			if !reflect.DeepEqual(before, safetyPending(t, s)) {
				t.Error("repair retired a still-needed session upsert")
			}
			session, err := s.GetSession("retained-session")
			check(err)
			if session == nil {
				t.Error("retained source session disappeared")
			}
			after, err := s.Export()
			check(err)
			if len(after.Observations)+len(after.Prompts) != 1 {
				t.Error("cross-project dependent content disappeared")
			}
		})
	}
}
