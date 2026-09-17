package store

import (
	"database/sql"
	"strings"
	"testing"
)

func TestRemirrorProjectReplaysCurrentStateWithoutRewritingHistory(t *testing.T) {
	s := newTestStore(t)
	const project = "remirror-project"
	if err := s.CreateSessionWithOwnershipMode("remirror-session", project, "/tmp/remirror", SessionOwnershipProjectOwned); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject(project); err != nil {
		t.Fatal(err)
	}
	_, sourceID := addTestObsSession(t, s, "remirror-session", "source", "decision", project, "project")
	_, targetID := addTestObsSession(t, s, "remirror-session", "target", "decision", project, "project")
	deletedID, deletedSyncID := addTestObsSession(t, s, "remirror-session", "deleted", "decision", project, "project")
	if err := s.DeleteObservation(deletedID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "remirror-session", Content: "live", Project: project}); err != nil {
		t.Fatal(err)
	}
	deletedPromptID, err := s.AddPrompt(AddPromptParams{SessionID: "remirror-session", Content: "deleted", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePrompt(deletedPromptID); err != nil {
		t.Fatal(err)
	}
	relationID := "remirror-relation"
	if _, err := s.SaveRelation(SaveRelationParams{SyncID: relationID, SourceID: sourceID, TargetID: targetID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.JudgeRelation(JudgeRelationParams{JudgmentID: relationID, Relation: RelationRelated, MarkedByActor: "test", MarkedByKind: "agent"}); err != nil {
		t.Fatal(err)
	}

	if err := s.CreateSession("unrelated-session", "unrelated-project", "/tmp/unrelated"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("unrelated-project"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "unrelated-session", Content: "unrelated", Project: "unrelated-project"}); err != nil {
		t.Fatal(err)
	}

	var maxSeq int64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM sync_mutations`).Scan(&maxSeq); err != nil {
		t.Fatal(err)
	}
	if err := s.AckSyncMutations(DefaultSyncTargetKey, maxSeq); err != nil {
		t.Fatal(err)
	}
	var beforeHistory, beforeAcked string
	if err := s.db.QueryRow(`SELECT group_concat(seq || ':' || acked_at), (SELECT last_acked_seq FROM sync_state WHERE target_key = ?) FROM sync_mutations WHERE acked_at IS NOT NULL`, DefaultSyncTargetKey).Scan(&beforeHistory, &beforeAcked); err != nil {
		t.Fatal(err)
	}

	if err := s.RemirrorProject(project); err != nil {
		t.Fatalf("remirror project: %v", err)
	}
	var afterHistory, afterAcked string
	if err := s.db.QueryRow(`SELECT group_concat(seq || ':' || acked_at), (SELECT last_acked_seq FROM sync_state WHERE target_key = ?) FROM sync_mutations WHERE acked_at IS NOT NULL`, DefaultSyncTargetKey).Scan(&afterHistory, &afterAcked); err != nil {
		t.Fatal(err)
	}
	if afterHistory != beforeHistory || afterAcked != beforeAcked {
		t.Fatalf("remirror rewrote delivery history: history %q -> %q, last_acked_seq %q -> %q", beforeHistory, afterHistory, beforeAcked, afterAcked)
	}

	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{SyncEntitySession: 1, SyncEntityObservation: 3, SyncEntityPrompt: 2, SyncEntityRelation: 1}
	var sessionMutation SyncMutation
	for _, mutation := range pending {
		if mutation.Project != project || !strings.HasPrefix(mutation.Source, "remirror:") {
			t.Fatalf("remirror included unrelated mutation: %+v", mutation)
		}
		if mutation.Entity == SyncEntitySession {
			sessionMutation = mutation
		}
		want[mutation.Entity]--
	}
	for entity, count := range want {
		if count != 0 {
			t.Fatalf("missing remirror %s mutations: %+v", entity, want)
		}
	}
	runTombstoneBackfill := func(source string) {
		t.Helper()
		if err := s.withTx(func(tx *sql.Tx) error { return s.backfillSyncDeleteTombstonesTx(tx, project, source) }); err != nil {
			t.Fatal(err)
		}
	}
	runTombstoneBackfill("remirror:same")
	runTombstoneBackfill("remirror:same")
	runTombstoneBackfill("remirror:new")
	for _, source := range []string{"remirror:same", "remirror:new"} {
		if got := scalarInt(t, s, `SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND source = ?`, SyncEntityObservation, deletedSyncID, source); got != 1 {
			t.Fatalf("tombstone mutations for %s = %d, want 1", source, got)
		}
		if got := scalarInt(t, s, `SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND source = ? AND op = ? AND COALESCE(json_extract(payload, '$.hard_delete'), 0) = 1`, SyncEntityObservation, deletedSyncID, source, SyncOpDelete); got != 1 {
			t.Fatalf("hard-delete tombstone mutations for %s = %d, want 1", source, got)
		}
	}
	peer := newTestStore(t)
	if err := peer.ApplyPulledMutation(DefaultSyncTargetKey, sessionMutation); err != nil {
		t.Fatalf("apply remirrored session on recreated peer: %v", err)
	}
	remirrored, err := peer.GetSession("remirror-session")
	if err != nil || remirrored.OwnershipMode != SessionOwnershipProjectOwned {
		t.Fatalf("remirrored session = %#v, %v; want project-owned ownership", remirrored, err)
	}
}

func TestBackfillSyncDeleteTombstonesReassertsUnknownRemoteFloorAfterAcknowledgement(t *testing.T) {
	s := newTestStore(t)
	const project = "reconcile-unknown-floor"
	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO sync_delete_tombstones (entity, entity_key, project, active, last_mutation_seq)
		VALUES (?, ?, ?, 1, 100)
	`, SyncEntitySession, "reconcile-session", project); err != nil {
		t.Fatalf("insert tombstone: %v", err)
	}
	for range 2 {
		if err := s.withTx(func(tx *sql.Tx) error {
			return s.backfillSyncDeleteTombstonesTx(tx, project)
		}); err != nil {
			t.Fatalf("reconcile tombstone: %v", err)
		}
	}
	if got := scalarInt(t, s, `
		SELECT COUNT(*) FROM sync_mutations
		WHERE target_key = ? AND entity = ? AND entity_key = ? AND op = ? AND source = ? AND acked_at IS NULL
	`, DefaultSyncTargetKey, SyncEntitySession, "reconcile-session", SyncOpDelete, SyncSourceLocal); got != 1 {
		t.Fatalf("pending reconciliation deletes = %d, want 1", got)
	}
	var remoteFloor sql.NullInt64
	if err := s.db.QueryRow(`SELECT last_remote_mutation_seq FROM sync_delete_tombstones WHERE entity_key = ?`, "reconcile-session").Scan(&remoteFloor); err != nil {
		t.Fatalf("read remote floor: %v", err)
	}
	if remoteFloor.Valid {
		t.Fatalf("remote floor = %d, want NULL before echo", remoteFloor.Int64)
	}
	var seq int64
	if err := s.db.QueryRow(`SELECT seq FROM sync_mutations WHERE entity_key = ? AND acked_at IS NULL`, "reconcile-session").Scan(&seq); err != nil {
		t.Fatalf("read reconciliation delete: %v", err)
	}
	if err := s.AckSyncMutations(DefaultSyncTargetKey, seq); err != nil {
		t.Fatalf("ack reconciliation delete: %v", err)
	}
	if err := s.withTx(func(tx *sql.Tx) error {
		return s.backfillSyncDeleteTombstonesTx(tx, project)
	}); err != nil {
		t.Fatalf("reconcile after acknowledgement: %v", err)
	}
	if got := scalarInt(t, s, `
		SELECT COUNT(*) FROM sync_mutations
		WHERE target_key = ? AND entity = ? AND entity_key = ? AND op = ? AND source = ?
	`, DefaultSyncTargetKey, SyncEntitySession, "reconcile-session", SyncOpDelete, SyncSourceLocal); got != 2 {
		t.Fatalf("total reconciliation deletes after acknowledgement = %d, want 2", got)
	}
	if got := scalarInt(t, s, `
		SELECT COUNT(*) FROM sync_mutations
		WHERE target_key = ? AND entity = ? AND entity_key = ? AND op = ? AND source = ? AND acked_at IS NULL
	`, DefaultSyncTargetKey, SyncEntitySession, "reconcile-session", SyncOpDelete, SyncSourceLocal); got != 1 {
		t.Fatalf("pending reconciliation deletes after acknowledgement = %d, want 1", got)
	}
}

func TestRemirrorProjectRequiresAnEnrolledProject(t *testing.T) {
	s := newTestStore(t)
	for _, project := range []string{"", "not-enrolled"} {
		if err := s.RemirrorProject(project); err == nil {
			t.Fatalf("expected remirror %q to fail", project)
		}
	}
}

func TestRemirrorProjectAvoidsRunSourceCollisions(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("collision-session", "collision-project", "/tmp/collision"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("collision-project"); err != nil {
		t.Fatal(err)
	}
	oldSource := newRemirrorSource
	newRemirrorSource = func() string { return "remirror:collision" }
	t.Cleanup(func() { newRemirrorSource = oldSource })

	if err := s.RemirrorProject("collision-project"); err != nil {
		t.Fatal(err)
	}
	var maxSeq int64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM sync_mutations`).Scan(&maxSeq); err != nil {
		t.Fatal(err)
	}
	if err := s.AckSyncMutations(DefaultSyncTargetKey, maxSeq); err != nil {
		t.Fatal(err)
	}
	if err := s.RemirrorProject("collision-project"); err != nil {
		t.Fatal(err)
	}

	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Source != "remirror:collision:1" {
		t.Fatalf("expected a fresh collision-safe replay mutation, got %+v", pending)
	}
}
