package store

import (
	"encoding/json"
	"testing"
)

func TestLegacyIsolatedSessionRepairPersistsOwnership(t *testing.T) {
	s := newTestStore(t)
	enrollTestProject(t, s, "target")
	id, err := s.RegisterIsolatedSession("legacy@target", "target", true)
	if err != nil {
		t.Fatal(err)
	}
	// Older journal rows can omit mode even though their local owner is known.
	if _, err := s.db.Exec(`UPDATE sync_mutations SET payload = ? WHERE entity = 'session' AND entity_key = ?`, `{"id":"legacy@target","project":"target"}`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.applyCloudUpgradeLegacyMutationRepairs("target"); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	var payload Session
	if err := json.Unmarshal([]byte(pending[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.OwnershipMode != SessionOwnershipProjectOwned || payload.Directory != "" {
		t.Fatalf("repair omitted authoritative ownership: %+v", payload)
	}
	if err := validatePulledSessionDirectory([]byte(pending[0].Payload)); err != nil {
		t.Fatalf("repaired session cannot be imported: %v", err)
	}
	for _, raw := range []string{
		`{"id":"legacy@target","project":"target","ownership_mode":"shared"}`,
		`{"id":"legacy@target","project":"target","ownership_mode":""}`,
		`{"id":"legacy@target","project":"target","ownership_mode":null}`,
		`{"id":"legacy@target","project":"target","directory":null}`,
		`{"id":"legacy@target","project":"foreign"}`,
	} {
		if _, err := s.db.Exec(`UPDATE sync_mutations SET payload = ? WHERE entity_key = ?`, raw, id); err != nil {
			t.Fatal(err)
		}
		if err := s.applyCloudUpgradeLegacyMutationRepairs("target"); err != nil {
			t.Fatal(err)
		}
		var after string
		if err := s.db.QueryRow(`SELECT payload FROM sync_mutations WHERE entity_key = ?`, id).Scan(&after); err != nil || after != raw {
			t.Fatalf("repair rewrote explicit/conflicting evidence: %s -> %s, %v", raw, after, err)
		}
	}
}
