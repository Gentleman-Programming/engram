package store

import (
	"context"
	"errors"
	"testing"
)

// ─── Fake SemanticRunner ──────────────────────────────────────────────────────

// fakeSemanticRunner is a test-only SemanticRunner that returns a fixed verdict.
type fakeSemanticRunner struct {
	verdict SemanticVerdict
	err     error
}

func (f *fakeSemanticRunner) Compare(_ context.Context, _ string) (SemanticVerdict, error) {
	return f.verdict, f.err
}

// ─── C.2a — TestJudgeBySemantic_InsertsWithSystemProvenance ──────────────────

// TestJudgeBySemantic_InsertsWithSystemProvenance verifies that a call to
// JudgeBySemantic for two valid observations inserts a row with the expected
// system provenance fields and returns a non-empty sync_id.
func TestJudgeBySemantic_InsertsWithSystemProvenance(t *testing.T) {
	s := setupRelationsStore(t)

	_, syncA := addTestObs(t, s, "JWT auth sessions decision", "decision", "testproject", "project")
	_, syncB := addTestObs(t, s, "Session handling legacy approach", "decision", "testproject", "project")

	syncID, err := s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:   syncA,
		TargetID:   syncB,
		Relation:   "compatible",
		Confidence: 0.9,
		Reasoning:  "Both discuss auth session approaches",
		Model:      "claude-haiku-4-5",
	})
	if err != nil {
		t.Fatalf("JudgeBySemantic: unexpected error: %v", err)
	}
	if syncID == "" {
		t.Fatal("JudgeBySemantic: expected non-empty sync_id")
	}
	if !hasPrefix(syncID, "rel-") {
		t.Errorf("sync_id must start with 'rel-'; got %q", syncID)
	}

	// Verify row was inserted with correct provenance.
	rel, err := s.GetRelation(syncID)
	if err != nil {
		t.Fatalf("GetRelation(%q): %v", syncID, err)
	}

	if rel.Relation != "compatible" {
		t.Errorf("relation: want %q; got %q", "compatible", rel.Relation)
	}
	if rel.JudgmentStatus != "judged" {
		t.Errorf("judgment_status: want %q; got %q", "judged", rel.JudgmentStatus)
	}
	if rel.MarkedByKind == nil || *rel.MarkedByKind != "system" {
		t.Errorf("marked_by_kind: want %q; got %v", "system", rel.MarkedByKind)
	}
	if rel.MarkedByActor == nil || *rel.MarkedByActor != "engram" {
		t.Errorf("marked_by_actor: want %q; got %v", "engram", rel.MarkedByActor)
	}
	if rel.MarkedByModel == nil || *rel.MarkedByModel != "claude-haiku-4-5" {
		t.Errorf("marked_by_model: want %q; got %v", "claude-haiku-4-5", rel.MarkedByModel)
	}
	if rel.Confidence == nil || *rel.Confidence != 0.9 {
		t.Errorf("confidence: want 0.9; got %v", rel.Confidence)
	}
}

// ─── C.2b — TestJudgeBySemantic_UpsertIdempotency ────────────────────────────

// TestJudgeBySemantic_UpsertIdempotency verifies that calling JudgeBySemantic
// twice for the same (source_id, target_id) pair updates the existing row rather
// than inserting a second row. The returned sync_id must be the same both times.
func TestJudgeBySemantic_UpsertIdempotency(t *testing.T) {
	s := setupRelationsStore(t)

	_, syncA := addTestObs(t, s, "JWT token auth decision A", "decision", "testproject", "project")
	_, syncB := addTestObs(t, s, "Auth token handling B", "decision", "testproject", "project")

	// First call.
	syncID1, err := s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:   syncA,
		TargetID:   syncB,
		Relation:   "related",
		Confidence: 0.7,
		Reasoning:  "initial judgment",
		Model:      "haiku-v1",
	})
	if err != nil {
		t.Fatalf("JudgeBySemantic (first): %v", err)
	}

	// Second call — same pair, different verdict.
	syncID2, err := s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:   syncA,
		TargetID:   syncB,
		Relation:   "compatible",
		Confidence: 0.95,
		Reasoning:  "updated judgment",
		Model:      "haiku-v2",
	})
	if err != nil {
		t.Fatalf("JudgeBySemantic (second): %v", err)
	}

	if syncID1 != syncID2 {
		t.Errorf("sync_id must be same on upsert; got %q and %q", syncID1, syncID2)
	}

	// Only one row must exist for this pair.
	var count int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM memory_relations WHERE source_id = ? AND target_id = ?`,
		syncA, syncB,
	).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row for pair; got %d", count)
	}

	// Row must reflect the SECOND verdict.
	rel, err := s.GetRelation(syncID2)
	if err != nil {
		t.Fatalf("GetRelation: %v", err)
	}
	if rel.Relation != "compatible" {
		t.Errorf("relation after upsert: want %q; got %q", "compatible", rel.Relation)
	}
	if rel.MarkedByModel == nil || *rel.MarkedByModel != "haiku-v2" {
		t.Errorf("marked_by_model after upsert: want %q; got %v", "haiku-v2", rel.MarkedByModel)
	}
}

// ─── C.2c — TestJudgeBySemantic_NotConflictIsNoOp ────────────────────────────

// TestJudgeBySemantic_NotConflictIsNoOp verifies that passing Relation="not_conflict"
// inserts no row and returns an empty sync_id without error.
func TestJudgeBySemantic_NotConflictIsNoOp(t *testing.T) {
	s := setupRelationsStore(t)

	_, syncA := addTestObs(t, s, "Unrelated auth decision", "decision", "testproject", "project")
	_, syncB := addTestObs(t, s, "CSS grid layout decision", "decision", "testproject", "project")

	syncID, err := s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:   syncA,
		TargetID:   syncB,
		Relation:   "not_conflict",
		Confidence: 0.99,
		Reasoning:  "clearly unrelated",
		Model:      "haiku",
	})
	if err != nil {
		t.Fatalf("JudgeBySemantic(not_conflict): unexpected error: %v", err)
	}
	if syncID != "" {
		t.Errorf("JudgeBySemantic(not_conflict): expected empty sync_id; got %q", syncID)
	}

	// No row must exist for this pair.
	var count int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM memory_relations WHERE source_id = ? OR target_id = ?`,
		syncA, syncA,
	).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows for not_conflict pair; got %d", count)
	}
}

// ─── C.2d — TestJudgeBySemantic_ValidationErrors ─────────────────────────────

// TestJudgeBySemantic_ValidationErrors verifies that invalid inputs return
// errors without inserting any rows.
func TestJudgeBySemantic_ValidationErrors(t *testing.T) {
	s := setupRelationsStore(t)

	_, syncA := addTestObs(t, s, "Some decision", "decision", "testproject", "project")
	_, syncB := addTestObs(t, s, "Another decision", "decision", "testproject", "project")

	cases := []struct {
		name   string
		params JudgeBySemanticParams
		errMsg string
	}{
		{
			name: "empty SourceID",
			params: JudgeBySemanticParams{
				SourceID:   "",
				TargetID:   syncB,
				Relation:   "compatible",
				Confidence: 0.9,
			},
			errMsg: "SourceID",
		},
		{
			name: "empty TargetID",
			params: JudgeBySemanticParams{
				SourceID:   syncA,
				TargetID:   "",
				Relation:   "related",
				Confidence: 0.8,
			},
			errMsg: "TargetID",
		},
		{
			name: "invalid relation verb",
			params: JudgeBySemanticParams{
				SourceID:   syncA,
				TargetID:   syncB,
				Relation:   "unknown_verb",
				Confidence: 0.8,
			},
			errMsg: "relation",
		},
		{
			name: "confidence above 1",
			params: JudgeBySemanticParams{
				SourceID:   syncA,
				TargetID:   syncB,
				Relation:   "related",
				Confidence: 1.5,
			},
			errMsg: "confidence",
		},
		{
			name: "confidence below 0",
			params: JudgeBySemanticParams{
				SourceID:   syncA,
				TargetID:   syncB,
				Relation:   "related",
				Confidence: -0.1,
			},
			errMsg: "confidence",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			syncID, err := s.JudgeBySemantic(tc.params)
			if err == nil {
				t.Errorf("expected error for %q; got nil (sync_id=%q)", tc.name, syncID)
			}
			if syncID != "" {
				t.Errorf("expected empty sync_id on validation error; got %q", syncID)
			}
		})
	}
}

// ─── C.2e — TestJudgeBySemantic_CrossProjectRejected ─────────────────────────

// TestJudgeBySemantic_CrossProjectRejected verifies that observations from
// different projects are rejected with ErrCrossProjectRelation.
func TestJudgeBySemantic_CrossProjectRejected(t *testing.T) {
	s := newTestStore(t)
	// Create two sessions in different projects.
	if err := s.CreateSession("ses-proj-x", "project-x", "/tmp/x"); err != nil {
		t.Fatalf("CreateSession x: %v", err)
	}
	if err := s.CreateSession("ses-proj-y", "project-y", "/tmp/y"); err != nil {
		t.Fatalf("CreateSession y: %v", err)
	}

	idX, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-proj-x",
		Type:      "decision",
		Title:     "Auth decision for project X",
		Content:   "some content",
		Project:   "project-x",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation X: %v", err)
	}
	obsX, _ := s.GetObservation(idX)

	idY, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-proj-y",
		Type:      "decision",
		Title:     "Auth decision for project Y",
		Content:   "some content",
		Project:   "project-y",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation Y: %v", err)
	}
	obsY, _ := s.GetObservation(idY)

	_, err = s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:   obsX.SyncID,
		TargetID:   obsY.SyncID,
		Relation:   "conflicts_with",
		Confidence: 0.9,
		Reasoning:  "cross-project test",
		Model:      "haiku",
	})
	if !errors.Is(err, ErrCrossProjectRelation) {
		t.Errorf("expected ErrCrossProjectRelation; got %v", err)
	}
}

// ─── C.2f — TestJudgeBySemantic_AllValidRelations ────────────────────────────

// TestJudgeBySemantic_AllValidRelations verifies that all 5 non-not_conflict
// relation verbs are accepted by JudgeBySemantic.
func TestJudgeBySemantic_AllValidRelations(t *testing.T) {
	validRelations := []string{
		RelationRelated,
		RelationCompatible,
		RelationScoped,
		RelationConflictsWith,
		RelationSupersedes,
	}

	for _, rel := range validRelations {
		t.Run(rel, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("ses-rel-test", "testproject", "/tmp/rel"); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			_, syncA := addTestObs(t, s, "Decision A for "+rel, "decision", "testproject", "project")
			_, syncB := addTestObs(t, s, "Decision B for "+rel, "decision", "testproject", "project")

			syncID, err := s.JudgeBySemantic(JudgeBySemanticParams{
				SourceID:   syncA,
				TargetID:   syncB,
				Relation:   rel,
				Confidence: 0.8,
				Reasoning:  "test for " + rel,
				Model:      "haiku",
			})
			if err != nil {
				t.Errorf("JudgeBySemantic(%q): unexpected error: %v", rel, err)
			}
			if syncID == "" {
				t.Errorf("JudgeBySemantic(%q): expected non-empty sync_id", rel)
			}
		})
	}
}
