package main

import (
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCmdDoctorRepairCurrentLocalTitle(t *testing.T) {
	cfg := testConfig(t)
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("current-title", "engram", "/work/engram"); err != nil {
		t.Fatal(err)
	}
	title := "  Current local title \t"
	id, err := s.AddObservation(store.AddObservationParams{SessionID: "current-title", Type: "bugfix", Title: title, Content: "Not the title.", Project: "engram", Scope: "project"})
	if err != nil {
		t.Fatal(err)
	}
	// Capture trims titles; persist a padded legacy title before freezing the payload.
	if _, err := s.DB().Exec(`UPDATE observations SET title = ? WHERE id = ?`, title, id); err != nil {
		t.Fatal(err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE sync_mutations SET payload = json_remove(payload, '$.title') WHERE entity = ? AND entity_key = ?`, store.SyncEntityObservation, obs.SyncID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	runDoctor := func() map[string]any {
		t.Helper()
		withArgs(t, "engram", "doctor", "--json", "--project", "engram", "--check", "sync_mutation_required_fields")
		out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
		if stderr != "" {
			t.Fatalf("doctor stderr=%q", stderr)
		}
		var report map[string]any
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatal(err)
		}
		return report
	}
	if report := runDoctor(); report["status"] != "blocked" {
		t.Fatalf("before=%v", report)
	}
	for _, mode := range []string{"--plan", "--apply"} {
		withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "sync_mutation_required_fields", mode)
		out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
		if stderr != "" {
			t.Fatalf("%s stderr=%q", mode, stderr)
		}
		report := decodeRepairPlan(t, out)
		if len(report["repairs"].([]any)) != 1 || len(report["actions"].([]any)) != 0 {
			t.Fatalf("%s report=%v", mode, report)
		}
		if mode == "--plan" {
			if report := runDoctor(); report["status"] != "blocked" {
				t.Fatalf("preview changed diagnostics=%v", report)
			}
		}
	}
	if report := runDoctor(); report["status"] == "blocked" {
		t.Fatalf("after=%v", report)
	}
	reopened, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close repaired store: %v", err)
		}
	}()
	after, err := reopened.GetObservation(id)
	if err != nil || after.Title != obs.Title || after.Title != title {
		t.Fatalf("source=%+v err=%v", after, err)
	}
	pending, err := reopened.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mutation := range pending {
		if mutation.Entity == store.SyncEntityObservation && mutation.EntityKey == obs.SyncID {
			found = true
			var payload struct {
				Title string `json:"title"`
			}
			if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil || payload.Title != obs.Title {
				t.Fatalf("payload title=%q want persisted=%q err=%v", payload.Title, obs.Title, err)
			}
			if validation := store.ValidateSyncMutationPayload(mutation.Entity, mutation.Op, mutation.Payload, mutation.EntityKey); validation.ReasonCode != "" {
				t.Fatalf("pending still invalid=%+v", validation)
			}
		}
	}
	if !found {
		t.Fatal("repair lost pending observation")
	}
}
