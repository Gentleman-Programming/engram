package store

import (
	"strings"
	"testing"
)

func TestRemirrorProjectReplaysCurrentStateWithoutRewritingHistory(t *testing.T) {
	s := newTestStore(t)
	const project = "remirror-project"
	if err := s.CreateSession("remirror-session", project, "/tmp/remirror"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject(project); err != nil {
		t.Fatal(err)
	}
	_, sourceID := addTestObsSession(t, s, "remirror-session", "source", "decision", project, "project")
	_, targetID := addTestObsSession(t, s, "remirror-session", "target", "decision", project, "project")
	deletedID, _ := addTestObsSession(t, s, "remirror-session", "deleted", "decision", project, "project")
	if err := s.DeleteObservation(deletedID, false); err != nil {
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
	for _, mutation := range pending {
		if mutation.Project != project || !strings.HasPrefix(mutation.Source, "remirror:") {
			t.Fatalf("remirror included unrelated mutation: %+v", mutation)
		}
		want[mutation.Entity]--
	}
	for entity, count := range want {
		if count != 0 {
			t.Fatalf("missing remirror %s mutations: %+v", entity, want)
		}
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
