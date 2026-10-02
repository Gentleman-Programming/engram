package sync

// Regression tests for issue #1494: a pulled chunk that deletes and then
// recreates the same session must apply in one pass and converge to the
// source's final state. The chunk shape and payloads come from the
// reporter's minimal reproduction (quirozino, engram#1494), with convergence
// assertions added on top.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func newChunkOrderingStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func chunkSessionUpsert(key string) store.SyncMutation {
	return store.SyncMutation{Entity: store.SyncEntitySession, Op: store.SyncOpUpsert, EntityKey: key, Project: "p",
		Payload: fmt.Sprintf(`{"id":%q,"project":"p","directory":"/work/p","started_at":"2026-08-13 14:47:51"}`, key)}
}

func chunkSessionDelete(key string) store.SyncMutation {
	return store.SyncMutation{Entity: store.SyncEntitySession, Op: store.SyncOpDelete, EntityKey: key, Project: "p",
		Payload: fmt.Sprintf(`{"id":%q,"project":"p","deleted_at":"2026-08-13 14:48:00"}`, key)}
}

func chunkObservationUpsert(key, sessionID string) store.SyncMutation {
	return store.SyncMutation{Entity: store.SyncEntityObservation, Op: store.SyncOpUpsert, EntityKey: key, Project: "p",
		Payload: fmt.Sprintf(`{"sync_id":%q,"session_id":%q,"type":"note","title":"t","content":"c","project":"p","scope":"project","created_at":"2026-08-13 14:49:59","updated_at":"2026-08-13 14:49:59"}`, key, sessionID)}
}

func chunkObservationHardDelete(key string) store.SyncMutation {
	return store.SyncMutation{Entity: store.SyncEntityObservation, Op: store.SyncOpDelete, EntityKey: key, Project: "p",
		Payload: fmt.Sprintf(`{"sync_id":%q,"hard_delete":true,"deleted_at":"2026-08-13 14:50:00"}`, key)}
}

// applyOrderedChunk runs one chunk through the exact import path
// importMutationChunk uses: orderMutationsForApply plus the store's pulled
// apply, so the assertions cover the production ordering rather than a
// hand-arranged sequence.
func applyOrderedChunk(t *testing.T, s *store.Store, chunkID string, mutations []store.SyncMutation) {
	t.Helper()
	if err := s.ApplyPulledChunkForDomain("cloud:p", chunkID, orderMutationsForApply(mutations), true); err != nil {
		t.Fatalf("chunk apply failed: %v", err)
	}
}

func assertSessionConverged(t *testing.T, s *store.Store, sessionID, observationKey string) {
	t.Helper()
	session, err := s.GetSession(sessionID)
	if err != nil {
		t.Fatalf("session %s did not survive the chunk: %v", sessionID, err)
	}
	if strings.TrimSpace(session.ID) != sessionID {
		t.Fatalf("session id = %q, want %q", session.ID, sessionID)
	}
	observations, err := s.SessionObservations(sessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, observation := range observations {
		if observation.SyncID == observationKey {
			found = true
		}
	}
	if !found {
		t.Fatalf("observation %s is not attached to session %s (%d observations)", observationKey, sessionID, len(observations))
	}
}

// TestChunkAppliesCreateDeleteRecreateAttachChunk is the reporter's case: one
// chunk carrying create S1, delete S1, recreate S1, attach O1. The phase
// reorder used to apply the session delete after the observation attach, the
// foreign key rejected it, and every import retry rolled the chunk back
// (FOREIGN KEY constraint failed (787), Pending import: 1 forever).
func TestChunkAppliesCreateDeleteRecreateAttachChunk(t *testing.T) {
	s := newChunkOrderingStore(t)
	applyOrderedChunk(t, s, "chunk-1494", []store.SyncMutation{
		chunkSessionUpsert("S1"),
		chunkSessionDelete("S1"),
		chunkSessionUpsert("S1"),
		chunkObservationUpsert("obs-1", "S1"),
	})
	assertSessionConverged(t, s, "S1", "obs-1")
}

// TestChunkDeletesChildObservationBeforeSupersededSessionDelete pins the
// review finding on PR #1520: with a pre-existing observation attached to a
// session, a chunk carrying hard-delete O1, delete S1, recreate S1, attach O2
// must apply in one pass. The superseded S1 delete rides before its own
// recreate, and the O1 hard delete must run BEFORE that session delete, or
// the sessions foreign key rejects the chunk while O1 still references it
// (child-before-parent).
func TestChunkDeletesChildObservationBeforeSupersededSessionDelete(t *testing.T) {
	s := newChunkOrderingStore(t)
	applyOrderedChunk(t, s, "chunk-seed", []store.SyncMutation{
		chunkSessionUpsert("S1"),
		chunkObservationUpsert("obs-1", "S1"),
	})
	assertSessionConverged(t, s, "S1", "obs-1")

	applyOrderedChunk(t, s, "chunk-child-before-parent", []store.SyncMutation{
		chunkObservationHardDelete("obs-1"),
		chunkSessionDelete("S1"),
		chunkSessionUpsert("S1"),
		chunkObservationUpsert("obs-2", "S1"),
	})

	if _, err := s.GetSession("S1"); err != nil {
		t.Fatalf("session S1 did not survive the chunk: %v", err)
	}
	observations, err := s.SessionObservations("S1", 10)
	if err != nil {
		t.Fatal(err)
	}
	hasObs1, hasObs2 := false, false
	for _, observation := range observations {
		if observation.SyncID == "obs-1" {
			hasObs1 = true
		}
		if observation.SyncID == "obs-2" {
			hasObs2 = true
		}
	}
	if hasObs1 {
		t.Fatalf("obs-1 survived its hard delete (%d observations)", len(observations))
	}
	if !hasObs2 {
		t.Fatalf("obs-2 is not attached to the recreated S1 (%d observations)", len(observations))
	}
}

// TestChunkKeepsSameEntityUpsertBeforeItsDrainedDelete pins the CodeRabbit
// finding on PR #1520's child-drain: a non-session upsert that PRECEDES its
// own delete in the chunk's original order must stay before it when the
// delete is drained ahead of a relocated session delete. Reordering them
// resurrects the entity: the source history ends with O1 deleted, an
// inverted pair ends with the late upsert recreating it.
func TestChunkKeepsSameEntityUpsertBeforeItsDrainedDelete(t *testing.T) {
	s := newChunkOrderingStore(t)
	applyOrderedChunk(t, s, "chunk-seed", []store.SyncMutation{
		chunkSessionUpsert("S1"),
	})

	applyOrderedChunk(t, s, "chunk-interleaved", []store.SyncMutation{
		chunkObservationUpsert("obs-1", "S1"),
		chunkObservationHardDelete("obs-1"),
		chunkSessionDelete("S1"),
		chunkSessionUpsert("S1"),
	})

	observations, err := s.SessionObservations("S1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 0 {
		t.Fatalf("interleaved chunk left %d observations, want 0 (the source history ends with obs-1 deleted)", len(observations))
	}
}

// TestChunkFreshImportAppliesInterleavedObservationViaDeferral pins the
// behavior behind the CodeRabbit follow-up on PR #1520: on a store where the
// referenced session does NOT exist yet and the chunk creates it only after
// the observation's own ops, the drained observation upsert emits before its
// parent session upsert in ordering terms. The chunk still applies, because
// the store defers pulled observations whose session is missing (#1274) and
// the session upsert later in the same chunk satisfies them. This pins that
// tolerance so a future ordering change cannot silently turn the interleaved
// fresh import into a hard failure.
func TestChunkFreshImportAppliesInterleavedObservationViaDeferral(t *testing.T) {
	s := newChunkOrderingStore(t)

	// No seed: S1 exists nowhere when the chunk starts applying.
	applyOrderedChunk(t, s, "chunk-fresh-interleaved", []store.SyncMutation{
		chunkObservationUpsert("obs-1", "S1"),
		chunkObservationHardDelete("obs-1"),
		chunkSessionDelete("S1"),
		chunkSessionUpsert("S1"),
	})

	if _, err := s.GetSession("S1"); err != nil {
		t.Fatalf("session S1 was not recreated on the fresh import: %v", err)
	}

	// The deferred obs-1 upsert must not outlive its own hard delete: the
	// delete ran after the upsert parked, so a replay that resurrected obs-1
	// would converge against the source's final state (obs-1 deleted).
	replay, err := s.ReplayDeferredForScope("cloud:p", "p")
	if err != nil {
		t.Fatalf("replay deferred: %v", err)
	}
	if replay.Retried != 0 {
		t.Fatalf("replay retried %d deferred rows, want 0 (the hard delete cancels the parked upsert)", replay.Retried)
	}
	observations, err := s.SessionObservations("S1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 0 {
		t.Fatalf("replay resurrected %d observations, want 0", len(observations))
	}
}

// TestChunkDeleteThenRecreateConvergesToRecreatedState covers the sharper
// convergence shape: a chunk whose first mutation deletes the session and a
// later one recreates it. The final phase order used to end with the delete,
// so the chunk "succeeded" while converging to the wrong final state.
func TestChunkDeleteThenRecreateConvergesToRecreatedState(t *testing.T) {
	s := newChunkOrderingStore(t)
	applyOrderedChunk(t, s, "chunk-1494-b", []store.SyncMutation{
		chunkSessionDelete("S1"),
		chunkSessionUpsert("S1"),
		chunkObservationUpsert("obs-2", "S1"),
	})
	assertSessionConverged(t, s, "S1", "obs-2")
}

func orderedMutationSummary(mutations []store.SyncMutation) string {
	parts := make([]string, 0, len(mutations))
	for _, mutation := range mutations {
		parts = append(parts, mutation.Op+" "+mutation.EntityKey)
	}
	return strings.Join(parts, " | ")
}

// TestOrderMutationsSupersededSessionDeleteRidesItsPosition pins the ordering
// contract directly: a session delete superseded by a later upsert of the same
// entity rides between that entity's upserts, every other session delete keeps
// the final phase, and unrelated entities keep their phase grouping.
func TestOrderMutationsSupersededSessionDeleteRidesItsPosition(t *testing.T) {
	t.Run("create delete recreate attach stays in history order", func(t *testing.T) {
		ordered := orderMutationsForApply([]store.SyncMutation{
			chunkSessionUpsert("S1"), chunkSessionDelete("S1"), chunkSessionUpsert("S1"), chunkObservationUpsert("obs-1", "S1"),
		})
		if want := "upsert S1 | delete S1 | upsert S1 | upsert obs-1"; orderedMutationSummary(ordered) != want {
			t.Fatalf("ordered = %q, want %q", orderedMutationSummary(ordered), want)
		}
	})
	t.Run("child hard delete rides before the relocated session delete", func(t *testing.T) {
		ordered := orderMutationsForApply([]store.SyncMutation{
			chunkObservationHardDelete("obs-1"), chunkSessionDelete("S1"), chunkSessionUpsert("S1"), chunkObservationUpsert("obs-2", "S1"),
		})
		if want := "delete obs-1 | delete S1 | upsert S1 | upsert obs-2"; orderedMutationSummary(ordered) != want {
			t.Fatalf("ordered = %q, want %q", orderedMutationSummary(ordered), want)
		}
	})
	t.Run("delete without recreate keeps the final phase", func(t *testing.T) {
		ordered := orderMutationsForApply([]store.SyncMutation{
			chunkSessionUpsert("S1"), chunkSessionDelete("S2"), chunkObservationUpsert("obs-1", "S1"),
		})
		if want := "upsert S1 | upsert obs-1 | delete S2"; orderedMutationSummary(ordered) != want {
			t.Fatalf("ordered = %q, want %q", orderedMutationSummary(ordered), want)
		}
	})
	t.Run("repeated deletes before one recreate keep their relative order", func(t *testing.T) {
		ordered := orderMutationsForApply([]store.SyncMutation{
			chunkSessionUpsert("S1"), chunkSessionDelete("S1"), chunkSessionDelete("S1"), chunkSessionUpsert("S1"),
		})
		if want := "upsert S1 | delete S1 | delete S1 | upsert S1"; orderedMutationSummary(ordered) != want {
			t.Fatalf("ordered = %q, want %q", orderedMutationSummary(ordered), want)
		}
	})
	t.Run("delete of another session does not ride an unrelated recreate", func(t *testing.T) {
		ordered := orderMutationsForApply([]store.SyncMutation{
			chunkSessionDelete("S2"), chunkSessionUpsert("S1"),
		})
		if want := "upsert S1 | delete S2"; orderedMutationSummary(ordered) != want {
			t.Fatalf("ordered = %q, want %q", orderedMutationSummary(ordered), want)
		}
	})
}
