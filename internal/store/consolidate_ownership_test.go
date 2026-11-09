package store

import (
	"errors"
	"testing"
)

func TestConsolidateExpectedProject(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		for _, tc := range []struct {
			name, owner string
			want        error
		}{
			{"wrong owner", "other-project", ErrObservationProjectMismatch},
			{"empty assertion", "", ErrExpectedProjectRequired},
		} {
			t.Run(tc.name+map[bool]string{false: " preview", true: " confirm"}[confirm], func(t *testing.T) {
				s, ids, _ := consolidateTestStore(t)
				preview, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
				if err != nil {
					t.Fatal(err)
				}
				before := snapshotConsolidation(t, s)
				result, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids, Confirm: confirm, Fingerprint: preview.Plan.Fingerprint, ExpectedProject: &tc.owner})
				if !errors.Is(err, tc.want) || result != nil {
					t.Errorf("result=%+v error=%v; want nil/%v", result, err, tc.want)
				}
				assertConsolidationUnchanged(t, s, before)
			})
		}
	}
}

func TestConsolidateExpectedProjectLegacyAndMatch(t *testing.T) {
	for _, asserted := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy nil", true: "explicit owner"}[asserted], func(t *testing.T) {
			s, ids, _ := consolidateTestStore(t)
			p := ConsolidateParams{IDs: ids}
			if asserted {
				owner := "proj-cons"
				p.ExpectedProject = &owner
			}
			before := snapshotConsolidation(t, s)
			preview, err := s.ConsolidateObservations(p)
			if err != nil || preview == nil || preview.Plan.Project != "proj-cons" || preview.Survivor != nil {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
			assertConsolidationUnchanged(t, s, before)
			p.Confirm = true
			p.Fingerprint = preview.Plan.Fingerprint
			out, err := s.ConsolidateObservations(p)
			if err != nil || out == nil || out.Survivor == nil || derefString(out.Survivor.Project) != "proj-cons" {
				t.Fatalf("confirm=%+v err=%v", out, err)
			}
			data, err := s.Export()
			if err != nil {
				t.Fatal(err)
			}
			byID := make(map[int64]Observation)
			for _, source := range data.Observations {
				byID[source.ID] = source
			}
			for _, id := range ids {
				source := byID[id]
				if source.ID != id || source.DeletedAt == nil || source.SupersededBySyncID == nil || *source.SupersededBySyncID != out.Survivor.SyncID {
					t.Fatalf("source=%+v", source)
				}
			}
		})
	}
}
