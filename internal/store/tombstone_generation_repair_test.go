package store

import "testing"

func TestAcknowledgedDeleteRepairPreservesRecreatedGeneration(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnrollProject("ack-delete"); err != nil {
		t.Fatal(err)
	}
	for generation := 1; generation <= 2; generation++ {
		if err := s.CreateSession("ack-session", "ack-delete", "/tmp/ack"); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteSession("ack-session"); err != nil {
			t.Fatal(err)
		}
		if count := scalarInt(t, s, `SELECT COUNT(*) FROM sync_mutations WHERE op = ?`, SyncOpDelete); count != generation {
			t.Fatalf("generation %d has %d deletes", generation, count)
		}
		max := scalarInt(t, s, `SELECT MAX(seq) FROM sync_mutations`)
		if err := s.AckSyncMutations(DefaultSyncTargetKey, int64(max)); err != nil {
			t.Fatal(err)
		}
		before := scalarInt(t, s, `SELECT COUNT(*) FROM sync_mutations WHERE op = ?`, SyncOpDelete)
		for range 3 {
			if err := s.repairEnrolledProjectSyncMutations(); err != nil {
				t.Fatal(err)
			}
		}
		if after := scalarInt(t, s, `SELECT COUNT(*) FROM sync_mutations WHERE op = ?`, SyncOpDelete); after != before {
			t.Fatalf("delete journal grew %d -> %d after acknowledgement", before, after)
		}

	}
}
