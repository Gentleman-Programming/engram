package store

import (
	"reflect"
	"testing"
)

func TestIsolatedSessionCloudDiagnosticsPreserveDirectory(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnrollProject("target"); err != nil {
		t.Fatal(err)
	}
	id, err := s.RegisterIsolatedSession("runtime@target", "target", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemirrorProject("target"); err != nil {
		t.Fatal(err)
	}
	before, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil || len(before) == 0 {
		t.Fatalf("pending mutations = %+v, %v", before, err)
	}
	for _, mutation := range before {
		got := ValidateSyncMutationPayload(mutation.Entity, mutation.Op, mutation.Payload, mutation.EntityKey)
		if got.ReasonCode != "" || len(got.MissingFields) != 0 {
			t.Fatalf("current isolated mutation classified invalid: %+v", got)
		}
	}
	report, err := s.DiagnoseCloudUpgradeLegacyMutations("target")
	if err != nil || report.BlockedCount != 0 || report.RepairableCount != 0 {
		t.Fatalf("isolated session upgrade diagnosis = %+v, %v", report, err)
	}
	for _, apply := range []bool{false, true} {
		actions, err := s.RepairPendingSessionDirectories("target", apply)
		if err != nil || len(actions) != 0 {
			t.Fatalf("directory repair apply=%t = %+v, %v", apply, actions, err)
		}
	}
	after, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("diagnosis/repair changed pending mutations: %+v, %v", after, err)
	}
	session, err := s.GetSession(id)
	if err != nil || session.Directory != "" || session.OwnershipMode != SessionOwnershipProjectOwned {
		t.Fatalf("isolated session changed: %+v, %v", session, err)
	}
	if resumed, err := s.RegisterIsolatedSession(id, "target", true); err != nil || resumed != id {
		t.Fatalf("isolated resume = %q, %v", resumed, err)
	}
}
