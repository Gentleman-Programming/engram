package store

import (
	"encoding/json"
	"testing"
)

func TestRepairPendingSessionProjectsGuards(t *testing.T) {
	for _, name := range []string{"matching", "local-only", "foreign-target", "acked", "quarantined", "wrong-scope", "wrong-project", "wrong-directory", "wrong-id", "missing-content", "padded-project"} {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("host", "demo", "/synthetic"); err != nil {
				t.Fatal(err)
			}
			if name != "local-only" {
				enrollTestProject(t, s, "demo")
			}
			if _, err := s.DB().Exec(`DELETE FROM sync_mutations`); err != nil {
				t.Fatal(err)
			}
			body := map[string]string{"id": "host", "project": "demo", "directory": "/synthetic", "ownership_mode": ""}
			target, project, disposition, scope := DefaultSyncTargetKey, "", SyncMutationDispositionPending, ""
			var ack any
			switch name {
			case "foreign-target":
				target = "foreign"
				if _, err := s.DB().Exec(`INSERT INTO sync_state(target_key) VALUES (?)`, target); err != nil {
					t.Fatal(err)
				}
			case "acked":
				ack = "2026-01-01 00:00:00"
			case "quarantined":
				disposition = SyncMutationDispositionQuarantined
			case "wrong-scope":
				scope = "other"
			case "wrong-project":
				body["project"] = "other"
			case "wrong-directory":
				body["directory"] = "/other"
			case "wrong-id":
				body["id"] = "other"
			case "missing-content":
				delete(body, "directory")
			case "padded-project":
				project = " demo "
			}
			payload, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.DB().Exec(`INSERT INTO sync_mutations (target_key,entity,entity_key,op,payload,source,project,acked_at,disposition) VALUES (?, 'session', 'host', 'upsert', ?, 'local', ?, ?, ?)`, target, string(payload), project, ack, disposition)
			if err != nil {
				t.Fatal(err)
			}
			seq, err := result.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
			for _, apply := range []bool{false, true, true} {
				actions, err := s.RepairPendingSessionProjects(scope, apply)
				if err != nil {
					t.Fatal(err)
				}
				if !apply && name == "matching" && len(actions) != 1 {
					t.Fatalf("dry plan missing: %+v", actions)
				}
			}
			var gotProject, gotPayload, gotDisposition string
			var gotAck any
			if err := s.DB().QueryRow(`SELECT project,payload,disposition,acked_at FROM sync_mutations WHERE seq=?`, seq).Scan(&gotProject, &gotPayload, &gotDisposition, &gotAck); err != nil {
				t.Fatal(err)
			}
			if name == "matching" {
				project = "demo"
			}
			if gotProject != project || gotPayload != string(payload) || gotDisposition != disposition || gotAck != ack {
				t.Fatalf("unexpected repair: project=%q payload=%q disposition=%q ack=%v", gotProject, gotPayload, gotDisposition, gotAck)
			}
		})
	}
}
