package autosync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// pagedPullTransport replays a fixed sequence of pull pages so tests control
// cursor progression, duplicate redelivery, and has_more boundaries.
type pagedPullTransport struct {
	pages []*PullMutationsResponse
	calls int
}

// PushMutations is unused by pull tests; satisfies CloudTransport.
func (t *pagedPullTransport) PushMutations([]MutationEntry) (*PushMutationsResult, error) {
	return &PushMutationsResult{}, nil
}

// PullMutations pops the next configured page, or an empty terminal page once
// the configured sequence is exhausted.
func (t *pagedPullTransport) PullMutations(_ context.Context, _ int64, _ int) (*PullMutationsResponse, error) {
	if t.calls >= len(t.pages) {
		return &PullMutationsResponse{}, nil
	}
	p := t.pages[t.calls]
	t.calls++
	return p, nil
}

// emptyPullTransport always answers one empty terminal page.
type emptyPullTransport struct{ pagedPullTransport }

// deferredListFailureStore wraps a real store whose deferred-project
// enumeration fails, to prove PullMutations surfaces the failure.
type deferredListFailureStore struct {
	*store.Store
	err error
}

// ListDeferredProjectsForTarget injects the configured failure.
func (s *deferredListFailureStore) ListDeferredProjectsForTarget(string) ([]string, error) {
	return nil, s.err
}

// deferredReplayFailureStore wraps a real store whose deferred replay fails,
// to prove PullMutations surfaces the failure instead of suppressing it.
type deferredReplayFailureStore struct {
	*store.Store
	err error
}

// ReplayDeferredForScope injects the configured failure.
func (s *deferredReplayFailureStore) ReplayDeferredForScope(string, string) (store.ReplayDeferredResult, error) {
	return store.ReplayDeferredResult{}, s.err
}

// applyFailStore wraps a real store and fails application of one specific
// sequence, so tests can exercise partial-apply failure and cursor resume.
type applyFailStore struct {
	*store.Store
	failSeq int64
}

// ApplyPulledMutation fails deterministically at the injected sequence.
func (s *applyFailStore) ApplyPulledMutation(targetKey string, mutation store.SyncMutation) error {
	if mutation.Seq == s.failSeq {
		return fmt.Errorf("injected apply failure at seq=%d", mutation.Seq)
	}
	return s.Store.ApplyPulledMutation(targetKey, mutation)
}

// newPullTestStore opens a real store in a temp dir for pull tests.
func newPullTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("store default config: %v", err)
	}
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// pullTestSessionMutation builds an appliable session upsert mutation as the
// cloud would deliver it.
func pullTestSessionMutation(seq int64, id, project string) PulledMutation {
	payload, _ := json.Marshal(map[string]any{
		"id":        id,
		"project":   project,
		"directory": "/tmp/" + project,
	})
	return PulledMutation{
		Seq:        seq,
		Project:    project,
		Entity:     store.SyncEntitySession,
		EntityKey:  id,
		Op:         store.SyncOpUpsert,
		Payload:    payload,
		OccurredAt: "2026-05-01T00:00:00Z",
	}
}

// seedDeferredRow inserts one pending deferred relation row for a project so
// replay work exists without having to exercise a real relation FK miss.
func seedDeferredRow(t *testing.T, s *store.Store, project string) {
	t.Helper()
	if _, err := s.DB().Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, project, scope_class, apply_status, first_seen_at)
		VALUES (?, 'relation', '{}', ?, ?, 'scoped', 'deferred', datetime('now'))
	`, "seed-"+project, store.DefaultSyncTargetKey, project); err != nil {
		t.Fatalf("seed deferred row: %v", err)
	}
}

// TestPullMutations_MultiPagePullAdvancesCursor covers a real multi-page pull:
// every page applies, the persisted cursor advances to the last page's max
// sequence, and touched projects are reported sorted.
func TestPullMutations_MultiPagePullAdvancesCursor(t *testing.T) {
	s := newPullTestStore(t)
	transport := &pagedPullTransport{pages: []*PullMutationsResponse{
		{
			Mutations: []PulledMutation{
				pullTestSessionMutation(1, "s1", "proj-b"),
				pullTestSessionMutation(2, "s2", "proj-b"),
			},
			HasMore:   true,
			LatestSeq: 2,
		},
		{
			Mutations: []PulledMutation{pullTestSessionMutation(3, "s3", "proj-a")},
			HasMore:   false,
			LatestSeq: 3,
		},
	}}

	report, err := PullMutations(context.Background(), s, transport, store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("PullMutations: %v", err)
	}
	if report.Applied != 3 {
		t.Fatalf("Applied: want 3, got %d", report.Applied)
	}
	if report.LastPulledSeq != 3 {
		t.Fatalf("LastPulledSeq: want 3, got %d", report.LastPulledSeq)
	}
	wantProjects := []string{"proj-a", "proj-b"}
	if len(report.ProjectsTouched) != 2 || report.ProjectsTouched[0] != wantProjects[0] || report.ProjectsTouched[1] != wantProjects[1] {
		t.Fatalf("ProjectsTouched: want %v, got %v", wantProjects, report.ProjectsTouched)
	}

	state, err := s.GetSyncState(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if state.LastPulledSeq != 3 {
		t.Fatalf("persisted cursor: want 3, got %d", state.LastPulledSeq)
	}
}

// TestPullMutations_NonProgressPageFailsClosed covers the pagination protocol
// contract: a continued page (has_more=true) that makes no cursor progress —
// empty or duplicate-only — must fail the pull instead of re-requesting the
// same page until the caller timeout.
func TestPullMutations_NonProgressPageFailsClosed(t *testing.T) {
	cases := []struct {
		name  string
		pages []*PullMutationsResponse
	}{
		{
			name: "empty page with has_more",
			pages: []*PullMutationsResponse{
				{
					Mutations: []PulledMutation{pullTestSessionMutation(1, "s1", "proj-a")},
					HasMore:   true,
					LatestSeq: 1,
				},
				{Mutations: nil, HasMore: true, LatestSeq: 1},
			},
		},
		{
			name: "duplicate-only page with has_more",
			pages: []*PullMutationsResponse{
				{
					Mutations: []PulledMutation{pullTestSessionMutation(1, "s1", "proj-a")},
					HasMore:   true,
					LatestSeq: 1,
				},
				{
					Mutations: []PulledMutation{pullTestSessionMutation(1, "s1", "proj-a")},
					HasMore:   true,
					LatestSeq: 1,
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newPullTestStore(t)
			transport := &pagedPullTransport{pages: tc.pages}
			_, err := PullMutations(context.Background(), s, transport, store.DefaultSyncTargetKey, 100)
			if err == nil {
				t.Fatal("expected protocol error on a page that does not advance the cursor")
			}
			if !strings.Contains(err.Error(), "has_more=true without advancing") {
				t.Fatalf("expected cursor-advance protocol error, got: %v", err)
			}
		})
	}
}

// TestPullMutations_CountsOnlyNewlyApplied exercises duplicate redelivery: a
// mutation whose sequence was already consumed must be skipped and must not
// inflate Applied or re-touch its project.
func TestPullMutations_CountsOnlyNewlyApplied(t *testing.T) {
	s := newPullTestStore(t)

	transport1 := &pagedPullTransport{pages: []*PullMutationsResponse{{
		Mutations: []PulledMutation{
			pullTestSessionMutation(1, "s1", "proj-a"),
			pullTestSessionMutation(2, "s2", "proj-a"),
		},
		HasMore: false,
	}}}
	report1, err := PullMutations(context.Background(), s, transport1, store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("first pull: %v", err)
	}
	if report1.Applied != 2 {
		t.Fatalf("first pull Applied: want 2, got %d", report1.Applied)
	}

	// Second pull redelivers seq 2 (already consumed) plus one new seq 3.
	transport2 := &pagedPullTransport{pages: []*PullMutationsResponse{{
		Mutations: []PulledMutation{
			pullTestSessionMutation(2, "s2", "proj-a"),
			pullTestSessionMutation(3, "s3", "proj-a"),
		},
		HasMore: false,
	}}}
	report2, err := PullMutations(context.Background(), s, transport2, store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("second pull: %v", err)
	}
	if report2.Applied != 1 {
		t.Fatalf("second pull Applied: want 1 (only seq 3 is new), got %d", report2.Applied)
	}
	if report2.LastPulledSeq != 3 {
		t.Fatalf("second pull LastPulledSeq: want 3, got %d", report2.LastPulledSeq)
	}
}

// TestPullMutations_DeferredListFailureSurfaces proves the pull fails when
// deferred-project enumeration fails, instead of logging and clearing the
// degraded state behind the caller's back.
func TestPullMutations_DeferredListFailureSurfaces(t *testing.T) {
	s := &deferredListFailureStore{Store: newPullTestStore(t), err: errors.New("injected list failure")}
	transport := &pagedPullTransport{pages: []*PullMutationsResponse{{Mutations: nil, HasMore: false}}}

	_, err := PullMutations(context.Background(), s, transport, store.DefaultSyncTargetKey, 100)
	if err == nil {
		t.Fatal("expected error when deferred-project enumeration fails")
	}
	if !strings.Contains(err.Error(), "list deferred projects") {
		t.Fatalf("expected deferred list error to surface, got: %v", err)
	}
}

// TestPullMutations_DeferredReplayFailureSurfaces proves a replay execution
// failure halts the pull with the project named, so the caller never reports a
// healthy pull when deferred relation work could not be replayed.
func TestPullMutations_DeferredReplayFailureSurfaces(t *testing.T) {
	s := &deferredReplayFailureStore{Store: newPullTestStore(t), err: errors.New("injected replay failure")}
	seedDeferredRow(t, s.Store, "proj-a")
	transport := &pagedPullTransport{pages: []*PullMutationsResponse{{Mutations: nil, HasMore: false}}}

	_, err := PullMutations(context.Background(), s, transport, store.DefaultSyncTargetKey, 100)
	if err == nil {
		t.Fatal("expected error when deferred replay fails")
	}
	if !strings.Contains(err.Error(), `replay deferred project "proj-a"`) {
		t.Fatalf("expected deferred replay error naming the project, got: %v", err)
	}
}

// TestPullMutations_EmptyPullReplaysPendingDeferredWork covers the empty-remote
// case with pending deferred work: zero new mutations are applied but the pull
// still executes and reports the pending replay.
func TestPullMutations_EmptyPullReplaysPendingDeferredWork(t *testing.T) {
	s := newPullTestStore(t)
	seedDeferredRow(t, s, "proj-a")
	transport := &pagedPullTransport{pages: []*PullMutationsResponse{{Mutations: nil, HasMore: false}}}

	report, err := PullMutations(context.Background(), s, transport, store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("PullMutations: %v", err)
	}
	if report.Applied != 0 {
		t.Fatalf("Applied: want 0 on empty pull, got %d", report.Applied)
	}
	if len(report.Replays) != 1 {
		t.Fatalf("expected 1 replay entry, got %d", len(report.Replays))
	}
	replay := report.Replays[0]
	if replay.Project != "proj-a" {
		t.Fatalf("replay project: want proj-a, got %q", replay.Project)
	}
	if replay.Retried != 1 {
		t.Fatalf("replay Retried: want 1 (the seeded row), got %d", replay.Retried)
	}
}

// TestPullMutations_PartialApplyFailureKeepsCursorAndResumes covers partial
// apply failure: the pull halts on the failing sequence, the persisted cursor
// stays at the last successfully applied sequence, and a resumed pull applies
// the remaining mutations without redelivery loss.
func TestPullMutations_PartialApplyFailureKeepsCursorAndResumes(t *testing.T) {
	realStore := newPullTestStore(t)
	s := &applyFailStore{Store: realStore, failSeq: 2}
	transport1 := &pagedPullTransport{pages: []*PullMutationsResponse{{
		Mutations: []PulledMutation{
			pullTestSessionMutation(1, "s1", "proj-a"),
			pullTestSessionMutation(2, "s2", "proj-a"),
		},
		HasMore: false,
	}}}

	_, err := PullMutations(context.Background(), s, transport1, store.DefaultSyncTargetKey, 100)
	if err == nil {
		t.Fatal("expected error on partial apply failure")
	}
	if !strings.Contains(err.Error(), "apply pulled mutation seq=2") {
		t.Fatalf("expected error naming the failing sequence, got: %v", err)
	}

	state, err := realStore.GetSyncState(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if state.LastPulledSeq != 1 {
		t.Fatalf("cursor after partial failure: want 1, got %d", state.LastPulledSeq)
	}

	// Resume with the real store: seq 2 and 3 must both apply.
	transport2 := &pagedPullTransport{pages: []*PullMutationsResponse{{
		Mutations: []PulledMutation{
			pullTestSessionMutation(2, "s2", "proj-a"),
			pullTestSessionMutation(3, "s3", "proj-a"),
		},
		HasMore: false,
	}}}
	report, err := PullMutations(context.Background(), realStore, transport2, store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("resumed pull: %v", err)
	}
	if report.Applied != 2 {
		t.Fatalf("resumed Applied: want 2, got %d", report.Applied)
	}
	if report.LastPulledSeq != 3 {
		t.Fatalf("resumed cursor: want 3, got %d", report.LastPulledSeq)
	}
}
