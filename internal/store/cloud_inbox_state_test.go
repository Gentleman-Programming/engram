package store

import (
	"reflect"
	"testing"
	"time"
)

func TestCloudInboxStateReadDoesNotMintUnenrolledTarget(t *testing.T) {
	s := newTestStore(t)
	before, err := s.ListSyncStates()
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.GetSyncState("cloud:unregistered-state")
	if err != nil {
		t.Fatal(err)
	}
	if state.TargetKey != "cloud:unregistered-state" || state.Lifecycle != SyncLifecycleIdle || state.LastAckedSeq != 0 || state.LastEnqueuedSeq != 0 {
		t.Fatalf("absent target should return transient idle state: %+v", state)
	}
	after, err := s.ListSyncStates()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("read persisted state: before=%+v after=%+v", before, after)
	}
}

func TestCloudInboxStateAckRefreshDoesNotMintUnenrolledTarget(t *testing.T) {
	for _, specific := range []bool{false, true} {
		t.Run(map[bool]string{false: "range", true: "specific"}[specific], func(t *testing.T) {
			s := newTestStore(t)
			// A legacy transport row may survive without a project state row.
			result, err := s.db.Exec(`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud','observation','legacy-state','upsert','{}','local','refresh-only')`)
			if err != nil {
				t.Fatal(err)
			}
			seq, err := result.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
			if specific {
				err = s.AckSyncMutationSeqs(DefaultSyncTargetKey, []int64{seq})
			} else {
				err = s.AckSyncMutations(DefaultSyncTargetKey, seq)
			}
			if err != nil {
				t.Fatal(err)
			}
			states, err := s.ListSyncStates()
			if err != nil {
				t.Fatal(err)
			}
			for _, state := range states {
				if state.TargetKey == "cloud:refresh-only" {
					t.Fatalf("ack refresh created unenrolled target: %+v", state)
				}
			}
			cloud, err := s.GetSyncState(DefaultSyncTargetKey)
			if err != nil {
				t.Fatal(err)
			}
			if cloud.LastAckedSeq != seq {
				t.Fatalf("ordinary transport acknowledgement lost: %+v", cloud)
			}
		})
	}
}

func TestCloudInboxStateConsumersPreserveRetainedEvidence(t *testing.T) {
	cases := []struct {
		name string
		run  func(*testing.T, *Store)
	}{
		{"pending", func(t *testing.T, s *Store) {
			rows, err := s.ListPendingSyncMutations(SyncInboxTargetKey, 100)
			if err != nil || len(rows) != 0 {
				t.Fatalf("inbox entered push page: rows=%+v err=%v", rows, err)
			}
		}},
		{"pagination", func(t *testing.T, s *Store) {
			rows, err := s.ListPendingSyncMutationsAfterSeq(SyncInboxTargetKey, 0, 100)
			if err != nil || len(rows) != 0 {
				t.Fatalf("inbox entered paginated push: rows=%+v err=%v", rows, err)
			}
		}},
		{"high_water", func(t *testing.T, s *Store) {
			seq, err := s.MaxPendingSyncMutationSeq(SyncInboxTargetKey)
			if err != nil || seq != 0 {
				t.Fatalf("inbox entered push high-water: seq=%d err=%v", seq, err)
			}
		}},
		{"unenrolled_count", func(t *testing.T, s *Store) {
			rows, err := s.CountPendingNonEnrolledSyncMutations(SyncInboxTargetKey)
			if err != nil || len(rows) != 0 {
				t.Fatalf("inbox entered non-enrollment counts: rows=%+v err=%v", rows, err)
			}
		}},
		{"skip_ack", func(t *testing.T, s *Store) {
			count, err := s.SkipAckNonEnrolledMutations(SyncInboxTargetKey)
			if err != nil || count != 0 {
				t.Fatalf("skip-ack consumed inbox: count=%d err=%v", count, err)
			}
		}},
		{"range_ack", func(t *testing.T, s *Store) {
			if err := s.AckSyncMutations(SyncInboxTargetKey, 999999); err != nil {
				t.Fatal(err)
			}
		}},
		{"specific_ack", func(t *testing.T, s *Store) {
			if err := s.AckSyncMutationSeqs(SyncInboxTargetKey, []int64{1, 2}); err != nil {
				t.Fatal(err)
			}
		}},
		{"repair_dry_run", func(t *testing.T, s *Store) {
			report, err := s.QuarantineIrreparableSyncMutations(SyncInboxTargetKey, "", false)
			if err != nil || len(report.Actions) != 0 {
				t.Fatalf("repair inspected retained inbox: report=%+v err=%v", report, err)
			}
		}},
		{"repair_apply", func(t *testing.T, s *Store) {
			report, err := s.QuarantineIrreparableSyncMutations(SyncInboxTargetKey, "", true)
			if err != nil || len(report.Actions) != 0 {
				t.Fatalf("repair changed retained inbox: report=%+v err=%v", report, err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.EnrollProject("state-enrolled"); err != nil {
				t.Fatal(err)
			}
			// Fixture injection only: these pre-existing inbox rows must stay
			// retained even when one project becomes eligible for delivery.
			for _, project := range []string{"state-enrolled", "state-unenrolled"} {
				if _, err := s.db.Exec(`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES (?, 'observation', ?, 'upsert', '{}', 'local', ?)`, SyncInboxTargetKey, project, project); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.ListSyncStates()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, state := range before {
				if state.TargetKey == SyncInboxTargetKey {
					found = true
					if state.UnackedMutations != 2 || state.Lifecycle != SyncLifecycleInbox {
						t.Fatalf("unexpected public fixture state: %+v", state)
					}
				}
			}
			if !found {
				t.Fatal("missing public inbox state")
			}
			tc.run(t, s)
			after, err := s.ListSyncStates()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("consumer changed retained rows or counters: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestCloudInboxStateExplicitDiagnosticsAndReservedCompatibility(t *testing.T) {
	s := newTestStore(t)
	const target = "cloud:diagnostic-only"
	until := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	if err := s.MarkSyncFailureWithReason(target, "policy_forbidden", "denied diagnostic", until); err != nil {
		t.Fatal(err)
	}
	failed, err := s.GetSyncState(target)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Lifecycle != SyncLifecycleDegraded || failed.ConsecutiveFailures != 1 || failed.ReasonCode == nil || *failed.ReasonCode != "policy_forbidden" || failed.LastError == nil || *failed.LastError != "denied diagnostic" || failed.BackoffUntil == nil {
		t.Fatalf("explicit failure disappeared: %+v", failed)
	}
	if err := s.MarkSyncPending(target); err != nil {
		t.Fatal(err)
	}
	pending, err := s.GetSyncState(target)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Lifecycle != SyncLifecyclePending || pending.ReasonCode != nil || pending.LastError != nil || pending.BackoffUntil != nil {
		t.Fatalf("explicit pending disappeared: %+v", pending)
	}
	if err := s.MarkSyncHealthy(target); err != nil {
		t.Fatal(err)
	}
	healthy, err := s.GetSyncState(target)
	if err != nil {
		t.Fatal(err)
	}
	if healthy.Lifecycle != SyncLifecycleHealthy || healthy.LastSuccessAt == nil {
		t.Fatalf("explicit success disappeared: %+v", healthy)
	}
	enrolled, err := s.IsProjectEnrolled("diagnostic-only")
	if err != nil || enrolled {
		t.Fatalf("diagnostic enrolled project: enrolled=%v err=%v", enrolled, err)
	}
	for _, key := range []string{DefaultSyncTargetKey, LocalChunkTargetKey} {
		if _, err := s.GetSyncState(key); err != nil {
			t.Fatal(err)
		}
	}
	states, err := s.ListSyncStates()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{target, DefaultSyncTargetKey, LocalChunkTargetKey} {
		found := false
		for _, state := range states {
			found = found || state.TargetKey == key
		}
		if !found {
			t.Fatalf("explicit/reserved state %s did not persist: %+v", key, states)
		}
	}
}
