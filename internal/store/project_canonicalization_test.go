package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestExplicitMergeCanonicalizationRollback(t *testing.T) {
	testExplicitMergeRollback(t, "Acme-api", "acme-api")
}

func TestExplicitMergeSpaceRollback(t *testing.T) {
	testExplicitMergeRollback(t, "plugin obsidian", "plugin-obsidian")
}

func testExplicitMergeRollback(t *testing.T, source, canonical string) {
	t.Helper()
	s := newTestStore(t)
	seedLegacyMergeRecords(t, s, source)
	seedPendingLegacyMutations(t, s, source)
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
	injected := errors.New("canonicalization commit failure")
	// Fail at commit after record and journal migration have both executed.
	s.hooks.commit = func(tx *sql.Tx) error { return injected }
	if _, err := s.MergeExplicitProjectVariants([]string{source}, canonical); !errors.Is(err, injected) {
		t.Fatalf("merge error = %v", err)
	}
	s.hooks.commit = nil
	after, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	before.ExportedAt, after.ExportedAt = "", ""
	pendingAfter, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	enrolledAfter, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pendingBefore, pendingAfter) || !reflect.DeepEqual(enrolledBefore, enrolledAfter) {
		t.Fatal("failed transaction persisted records, journal or enrollment")
	}
}

func TestExplicitMergeCanonicalization(t *testing.T) {
	testExplicitMergeRecordsAndSync(t, "Acme-api", "acme-api")
}

func TestExplicitMergeSpaceVariant(t *testing.T) {
	for _, pair := range [][2]string{{"plugin obsidian", "plugin-obsidian"}, {"plugin  obsidian", "plugin_obsidian"}, {"plugin-obsidian", "plugin obsidian"}} {
		t.Run(pair[0]+"->"+pair[1], func(t *testing.T) { testExplicitMergeRecordsAndSync(t, pair[0], pair[1]) })
	}
}

func TestExplicitMergeSpaceSelectsOnlyNamedSource(t *testing.T) {
	s := newTestStore(t)
	projects := map[string]string{"selected": "plugin obsidian", "other-case": "Plugin Obsidian", "other-separator": "plugin_obsidian", "target": "plugin-obsidian"}
	for id, project := range projects {
		if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, '')`, id, project); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.MergeExplicitProjectVariants([]string{"plugin obsidian"}, "plugin-obsidian")
	if err != nil || result.SessionsUpdated != 1 {
		t.Fatalf("merge = %+v, %v", result, err)
	}
	projects["selected"] = "plugin-obsidian"
	for id, want := range projects {
		var got string
		if err := s.db.QueryRow(`SELECT project FROM sessions WHERE id = ?`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("%s project = %q, want %q; %v", id, got, want, err)
		}
	}
}

func testExplicitMergeRecordsAndSync(t *testing.T, source, canonical string) {
	t.Helper()
	for _, enrolled := range []bool{false, true} {
		name := "local-only"
		if enrolled {
			name = "enrolled"
		}
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			seedLegacyMergeRecords(t, s, source)
			if _, err := s.db.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES ('legacy-prompt', 'legacy-session', 'original prompt', ?)`, source); err != nil {
				t.Fatal(err)
			}
			if enrolled {
				seedPendingLegacyMutations(t, s, source)
				if _, err := s.db.Exec(`INSERT INTO sync_enrolled_projects (project) VALUES (?)`, source); err != nil {
					t.Fatal(err)
				}
			}
			// No public API lists acknowledged journal history; inspect this fixture
			// by named fields while all pending assertions use the public API.
			if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project, acked_at) VALUES (?, 'session', 'acked-history', 'upsert', ?, 'local', ?, '2026-01-01 00:00:00')`, DefaultSyncTargetKey, fmt.Sprintf(`{"project":%q,"extra":"preserve"}`, source), source); err != nil {
				t.Fatal(err)
			}
			readHistory := func() SyncMutation {
				t.Helper()
				var m SyncMutation
				if err := s.db.QueryRow(`SELECT seq, target_key, entity, entity_key, op, payload, source, project, occurred_at, acked_at, disposition, ifnull(disposition_reason, ''), ifnull(disposition_evidence, ''), disposition_at FROM sync_mutations WHERE entity_key = 'acked-history'`).Scan(&m.Seq, &m.TargetKey, &m.Entity, &m.EntityKey, &m.Op, &m.Payload, &m.Source, &m.Project, &m.OccurredAt, &m.AckedAt, &m.Disposition, &m.DispositionReason, &m.DispositionEvidence, &m.DispositionAt); err != nil {
					t.Fatal(err)
				}
				return m
			}
			historyBefore := readHistory()
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
				t.Fatal(err)
			}
			if preview.Source != source || preview.Canonical != canonical || preview.ObservationsUpdated != 1 || preview.SessionsUpdated != 1 || preview.PromptsUpdated != 1 || preview.SyncIdentityChanges != enrolled {
				t.Fatalf("preview = %+v", preview)
			}
			for _, pair := range [][2]string{{canonical, source}, {source, "unrelated"}, {"ACME-API", canonical}} {
				if _, err := s.PreviewExplicitProjectMerge(pair[0], pair[1]); err == nil {
					t.Fatalf("accepted %q", pair)
				}
			}
			after, err := s.Export()
			if err != nil {
				t.Fatal(err)
			}
			before.ExportedAt, after.ExportedAt = "", ""
			pendingAfter, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
			if err != nil {
				t.Fatal(err)
			}
			enrolledAfter, err := s.ListEnrolledProjects()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pendingBefore, pendingAfter) || !reflect.DeepEqual(enrolledBefore, enrolledAfter) {
				t.Fatal("preview/rejections mutated state")
			}
			result, err := s.MergeExplicitProjectVariants([]string{source}, canonical)
			if err != nil {
				t.Fatal(err)
			}
			if result.ObservationsUpdated != 1 || result.SessionsUpdated != 1 || result.PromptsUpdated != 1 || !reflect.DeepEqual(result.SourcesMerged, []string{source}) {
				t.Fatalf("result = %+v", result)
			}
			after, err = s.Export()
			if err != nil {
				t.Fatal(err)
			}
			for i := range before.Sessions {
				before.Sessions[i].Project = canonical
			}
			for i := range before.Observations {
				p := canonical
				before.Observations[i].Project = &p
			}
			for i := range before.Prompts {
				before.Prompts[i].Project = canonical
			}
			after.ExportedAt = ""
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("records changed beyond ownership: before=%+v after=%+v", before, after)
			}
			enrolledAfter, err = s.ListEnrolledProjects()
			if err != nil {
				t.Fatal(err)
			}
			if enrolled {
				if len(enrolledAfter) != 1 || enrolledAfter[0].Project != canonical {
					t.Fatalf("enrollment = %+v", enrolledAfter)
				}
				pending := pendingMutationsByEntityKey(t, s)
				for _, original := range pendingBefore {
					got := pending[original.EntityKey]
					var oldPayload, newPayload map[string]any
					if err := json.Unmarshal([]byte(original.Payload), &oldPayload); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal([]byte(got.Payload), &newPayload); err != nil {
						t.Fatal(err)
					}
					oldPayload["project"] = canonical
					if !reflect.DeepEqual(oldPayload, newPayload) {
						t.Fatalf("payload changed beyond ownership: %v -> %v", oldPayload, newPayload)
					}
					original.Project, original.Payload = canonical, got.Payload
					if !reflect.DeepEqual(original, got) {
						t.Fatalf("journal identity/metadata changed: %+v -> %+v", original, got)
					}
				}
				for _, key := range []string{"legacy-session", "legacy-obs", "legacy-prompt"} {
					m, ok := pending[key]
					if !ok || m.Project != canonical || payloadProject(t, m.Payload) != canonical {
						t.Fatalf("pending %q = %+v", key, m)
					}
				}
			} else {
				pendingAfter, err = s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
				if err != nil {
					t.Fatal(err)
				}
				if len(enrolledAfter) != 0 || !reflect.DeepEqual(pendingBefore, pendingAfter) {
					t.Fatal("local-only merge changed sync state")
				}
			}
			if _, err := s.MergeExplicitProjectVariants([]string{source}, canonical); err == nil {
				t.Fatal("repeat accepted missing source")
			}
			if !reflect.DeepEqual(historyBefore, readHistory()) {
				t.Fatal("acknowledged history changed")
			}
		})
	}
}
