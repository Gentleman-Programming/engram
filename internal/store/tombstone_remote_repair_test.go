package store

import "testing"

func TestRepairDoesNotEchoDefaultCloudDelete(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnrollProject("remote-delete"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{Seq: 7, Entity: SyncEntitySession, EntityKey: "remote-session", Op: SyncOpDelete, Payload: `{"id":"remote-session","project":"remote-delete","deleted":true,"hard_delete":true}`}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.repairEnrolledProjectSyncMutations(); err != nil {
			t.Fatal(err)
		}
	}
	if count := scalarInt(t, s, `SELECT COUNT(*) FROM sync_mutations WHERE source = ? AND entity_key = ? AND op = ?`, SyncSourceLocal, "remote-session", SyncOpDelete); count != 0 {
		t.Fatalf("remote delete echoed as %d local deletes", count)
	}
}
