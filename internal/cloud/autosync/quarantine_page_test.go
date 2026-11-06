package autosync

import (
	"context"
	"errors"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

type quarantinePageStore struct {
	*fakeLocalStore
	cursors []int64
	fail    bool
}

func (s *quarantinePageStore) QuarantineIrreparableSyncMutationsPage(target string, after int64, limit int) (store.SyncMutationQuarantineReport, int64, error) {
	if target != "replica" || limit != 100 {
		return store.SyncMutationQuarantineReport{}, 0, errors.New("unexpected target or unbounded limit")
	}
	s.cursors = append(s.cursors, after)
	if s.fail {
		return store.SyncMutationQuarantineReport{}, 0, errors.New("synthetic page failure")
	}
	if after == 0 {
		return store.SyncMutationQuarantineReport{}, 42, nil
	}
	return store.SyncMutationQuarantineReport{}, 0, nil
}

func TestManagerQuarantineCursorRetriesAndRestarts(t *testing.T) {
	s := &quarantinePageStore{fakeLocalStore: newFakeLocalStore()}
	cfg := DefaultConfig()
	cfg.TargetKey = "replica"
	cfg.PushBatchSize = 10000
	m := New(s, newFakeTransport(), cfg)
	if err := m.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.fail = true
	if err := m.push(context.Background()); err == nil {
		t.Fatal("expected failed page")
	}
	s.fail = false
	if err := m.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []int64{0, 42, 42, 0}
	for i, got := range s.cursors {
		if got != want[i] {
			t.Fatalf("cursors=%v want=%v", s.cursors, want)
		}
	}
	if len(s.cursors) != len(want) {
		t.Fatalf("cursors=%v want=%v", s.cursors, want)
	}
}
