package store

import (
	"reflect"
	"testing"
)

func TestUnicodeProjectLegacyRepair(t *testing.T) {
	const source, canonical = "cafe\u0301", "café"
	for _, explicit := range []bool{false, true} {
		name := "consolidate"
		if explicit {
			name = "explicit"
		}
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			seedLegacyMergeRecords(t, s, source)
			seedPendingLegacyMutations(t, s, source)
			if _, err := s.db.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES ('unicode-prompt', 'legacy-session', 'original prompt', ?)`, source); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO sync_enrolled_projects (project) VALUES (?)`, source); err != nil {
				t.Fatal(err)
			}
			before, err := s.Export()
			if err != nil {
				t.Fatal(err)
			}
			pendingBefore, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
			if err != nil {
				t.Fatal(err)
			}
			enrolledBefore, err := s.ListEnrolledProjects()
			if err != nil {
				t.Fatal(err)
			}
			preview, err := s.PreviewExplicitProjectMerge(source, canonical)
			if err != nil {
				t.Fatalf("Unicode preview: %v", err)
			}
			if preview.Canonical != canonical || preview.Source != source || preview.ObservationsUpdated != 1 || preview.SessionsUpdated != 1 || preview.PromptsUpdated != 1 || !preview.SyncIdentityChanges {
				t.Fatalf("preview = %+v", preview)
			}
			if _, err := s.PreviewExplicitProjectMerge(source, "cafe"); err == nil {
				t.Fatal("accepted a different accented name")
			}
			after, err := s.Export()
			if err != nil {
				t.Fatal(err)
			}
			before.ExportedAt = ""
			after.ExportedAt = ""
			pendingAfter, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
			if err != nil {
				t.Fatal(err)
			}
			enrolledAfter, err := s.ListEnrolledProjects()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pendingBefore, pendingAfter) || !reflect.DeepEqual(enrolledBefore, enrolledAfter) {
				t.Fatal("preview/rejection changed records, counters, journal or enrollment")
			}
			var result *MergeResult
			if explicit {
				result, err = s.MergeExplicitProjectVariants([]string{source}, canonical)
			} else {
				result, err = s.MergeProjects([]string{source}, canonical)
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.ObservationsUpdated != 1 || result.SessionsUpdated != 1 || result.PromptsUpdated != 1 || len(result.SourcesMerged) != 1 || result.SourcesMerged[0] != source {
				t.Fatalf("result = %+v", result)
			}
			records, err := s.Export()
			if err != nil {
				t.Fatal(err)
			}
			if len(records.Sessions) != 1 || records.Sessions[0].Project != canonical || len(records.Observations) != 1 || records.Observations[0].Project == nil || *records.Observations[0].Project != canonical || len(records.Prompts) != 1 || records.Prompts[0].Project != canonical || records.Prompts[0].Content != "original prompt" {
				t.Fatalf("merged records = %+v", records)
			}
			enrolled, err := s.ListEnrolledProjects()
			if err != nil {
				t.Fatal(err)
			}
			if len(enrolled) != 1 || enrolled[0].Project != canonical {
				t.Fatalf("enrollment = %+v", enrolled)
			}
			pending := pendingMutationsByEntityKey(t, s)
			for _, key := range []string{"legacy-session", "legacy-obs", "unicode-prompt"} {
				mutation, ok := pending[key]
				if !ok || mutation.Project != canonical || payloadProject(t, mutation.Payload) != canonical {
					t.Fatalf("pending %q = %+v", key, mutation)
				}
			}
			if skipped, err := s.SkipAckNonEnrolledMutations(DefaultSyncTargetKey); err != nil || skipped != 0 {
				t.Fatalf("skip-ack = %d, %v", skipped, err)
			}
		})
	}
}
