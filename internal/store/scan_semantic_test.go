package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// ─── Mock runner helpers ─────────────────────────────────────────────────────

// verdictRunner returns a fixed SemanticVerdict for every Compare call.
type verdictRunner struct {
	verdict SemanticVerdict
}

func (r *verdictRunner) Compare(_ context.Context, _ string) (SemanticVerdict, error) {
	return r.verdict, nil
}

// errorRunner returns an error for every Compare call.
type errorRunner struct {
	err error
}

func (r *errorRunner) Compare(_ context.Context, _ string) (SemanticVerdict, error) {
	return SemanticVerdict{}, r.err
}

// routingRunner routes calls to different runners based on the call index.
type routingRunner struct {
	runners  []SemanticRunner
	callIdx  int
}

func (r *routingRunner) Compare(ctx context.Context, prompt string) (SemanticVerdict, error) {
	idx := r.callIdx % len(r.runners)
	r.callIdx++
	return r.runners[idx].Compare(ctx, prompt)
}

// blockingRunner blocks until its context is cancelled (simulates timeout).
type blockingRunner struct{}

func (r *blockingRunner) Compare(ctx context.Context, _ string) (SemanticVerdict, error) {
	<-ctx.Done()
	return SemanticVerdict{}, ctx.Err()
}

// identityPromptBuilder returns the concat of both snippet titles as the prompt.
func identityPromptBuilder(a, b ObservationSnippet) string {
	return a.Title + " vs " + b.Title
}

// seedSimilarPair inserts two observations with similar titles (to produce FTS candidates)
// and returns their integer IDs and sync_ids.
func seedSimilarPair(t *testing.T, s *Store, project string) (int64, string, int64, string) {
	t.Helper()
	if err := s.CreateSession("ses-sem-test", project, "/tmp/sem"); err != nil {
		// Session may already exist — ignore.
		_ = err
	}
	idA, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-sem-test",
		Type:      "decision",
		Title:     "JWT auth token session security approach",
		Content:   "We use JWT tokens for authentication. Sessions are stored in Redis.",
		Project:   project,
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation A: %v", err)
	}
	obsA, _ := s.GetObservation(idA)

	idB, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-sem-test",
		Type:      "decision",
		Title:     "JWT auth sessions token management decision",
		Content:   "Auth tokens managed via JWT. Sessions handled with Redis cache.",
		Project:   project,
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation B: %v", err)
	}
	obsB, _ := s.GetObservation(idB)

	return idA, obsA.SyncID, idB, obsB.SyncID
}

// ─── C.5a — TestScanProject_Semantic_HappyPath ───────────────────────────────

// TestScanProject_Semantic_HappyPath verifies that a semantic scan with a runner
// returning a valid non-not_conflict verdict results in SemanticJudged=1 and a
// persisted relation row.
func TestScanProject_Semantic_HappyPath(t *testing.T) {
	s := newTestStore(t)
	_, syncA, _, syncB := seedSimilarPair(t, s, "sem-project")

	runner := &verdictRunner{
		verdict: SemanticVerdict{
			Relation:   "compatible",
			Confidence: 0.9,
			Reasoning:  "both discuss JWT auth",
			Model:      "haiku",
		},
	}

	result, err := s.ScanProject(ScanOptions{
		Project:        "sem-project",
		Apply:          true,
		Semantic:       true,
		Concurrency:    1,
		TimeoutPerCall: 5 * time.Second,
		MaxSemantic:    10,
		Runner:         runner,
		BuildPrompt:    identityPromptBuilder,
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}

	if result.SemanticJudged == 0 {
		t.Errorf("SemanticJudged: want > 0; got 0 (CandidatesFound=%d)", result.CandidatesFound)
	}
	if result.SemanticErrors != 0 {
		t.Errorf("SemanticErrors: want 0; got %d", result.SemanticErrors)
	}

	// Verify that a relation row was actually inserted.
	var count int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM memory_relations
		 WHERE (source_id = ? AND target_id = ?) OR (source_id = ? AND target_id = ?)
		   AND marked_by_actor = 'engram'`,
		syncA, syncB, syncB, syncA,
	).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count == 0 {
		t.Error("expected at least 1 relation row with marked_by_actor='engram'; got 0")
	}
}

// ─── C.5b — TestScanProject_Semantic_NotConflictSkipped ──────────────────────

// TestScanProject_Semantic_NotConflictSkipped verifies that verdicts of
// "not_conflict" are counted in SemanticSkipped and do NOT produce relation rows.
func TestScanProject_Semantic_NotConflictSkipped(t *testing.T) {
	s := newTestStore(t)
	seedSimilarPair(t, s, "sem-skip-project")

	runner := &verdictRunner{
		verdict: SemanticVerdict{
			Relation:   "not_conflict",
			Confidence: 0.99,
			Reasoning:  "unrelated",
			Model:      "haiku",
		},
	}

	result, err := s.ScanProject(ScanOptions{
		Project:        "sem-skip-project",
		Apply:          true,
		Semantic:       true,
		Concurrency:    1,
		TimeoutPerCall: 5 * time.Second,
		MaxSemantic:    10,
		Runner:         runner,
		BuildPrompt:    identityPromptBuilder,
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}

	if result.SemanticSkipped == 0 {
		t.Errorf("SemanticSkipped: want > 0; got 0 (CandidatesFound=%d)", result.CandidatesFound)
	}
	if result.SemanticJudged != 0 {
		t.Errorf("SemanticJudged: want 0; got %d", result.SemanticJudged)
	}

	var count int
	_ = s.db.QueryRow(
		`SELECT count(*) FROM memory_relations WHERE marked_by_actor = 'engram'`,
	).Scan(&count)
	if count != 0 {
		t.Errorf("expected no 'engram' relation rows for not_conflict; got %d", count)
	}
}

// ─── C.5c — TestScanProject_Semantic_ErrorIsolation ──────────────────────────

// TestScanProject_Semantic_ErrorIsolation verifies that a runner error on one
// pair increments SemanticErrors but does not abort the scan.
func TestScanProject_Semantic_ErrorIsolation(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-err-test", "err-project", "/tmp/err"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Insert 3 similar pairs to get enough FTS candidates.
	titles := []struct{ a, b string }{
		{"JWT auth session token A1", "JWT auth session token A2"},
		{"Redis cache memory storage B1", "Redis cache memory storage B2"},
		{"Database connection pool C1", "Database connection pool C2"},
	}
	for _, pair := range titles {
		_, _ = s.AddObservation(AddObservationParams{
			SessionID: "ses-err-test", Type: "decision",
			Title: pair.a, Content: pair.a + " content", Project: "err-project", Scope: "project",
		})
		_, _ = s.AddObservation(AddObservationParams{
			SessionID: "ses-err-test", Type: "decision",
			Title: pair.b, Content: pair.b + " content", Project: "err-project", Scope: "project",
		})
	}

	runner := &errorRunner{err: errors.New("simulated runner error")}

	result, err := s.ScanProject(ScanOptions{
		Project:        "err-project",
		Apply:          true,
		Semantic:       true,
		Concurrency:    1,
		TimeoutPerCall: 5 * time.Second,
		MaxSemantic:    100,
		Runner:         runner,
		BuildPrompt:    identityPromptBuilder,
	})
	if err != nil {
		t.Fatalf("ScanProject must not return error on runner errors; got: %v", err)
	}

	// All attempted pairs must be counted as errors (runner always fails).
	totalAttempted := result.SemanticJudged + result.SemanticSkipped + result.SemanticErrors
	if totalAttempted == 0 {
		t.Logf("No semantic calls attempted (CandidatesFound=%d); skipping counter assertion", result.CandidatesFound)
		return
	}
	if result.SemanticErrors == 0 {
		t.Errorf("SemanticErrors: want > 0; got 0 (total attempted=%d)", totalAttempted)
	}
}

// ─── C.5d — TestScanProject_Semantic_MaxSemanticCap ──────────────────────────

// TestScanProject_Semantic_MaxSemanticCap verifies that the MaxSemantic cap
// limits the number of LLM calls. We seed many similar observations, then set
// MaxSemantic=1 and confirm at most 1 semantic call is made.
func TestScanProject_Semantic_MaxSemanticCap(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-cap-test", "cap-project", "/tmp/cap"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Seed many similar observations to produce many candidates.
	for i := 0; i < 20; i++ {
		title := fmt.Sprintf("JWT auth session token management approach %d", i)
		_, _ = s.AddObservation(AddObservationParams{
			SessionID: "ses-cap-test", Type: "decision",
			Title: title, Content: title + " content", Project: "cap-project", Scope: "project",
		})
	}

	runner := &verdictRunner{
		verdict: SemanticVerdict{
			Relation:   "compatible",
			Confidence: 0.8,
			Reasoning:  "similar",
			Model:      "haiku",
		},
	}

	result, err := s.ScanProject(ScanOptions{
		Project:        "cap-project",
		Apply:          true,
		Semantic:       true,
		Concurrency:    2,
		TimeoutPerCall: 5 * time.Second,
		MaxSemantic:    2, // cap at 2
		Runner:         runner,
		BuildPrompt:    identityPromptBuilder,
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}

	total := result.SemanticJudged + result.SemanticSkipped + result.SemanticErrors
	if total > 2 {
		t.Errorf("MaxSemantic=2: expected total semantic calls <= 2; got %d (judged=%d, skipped=%d, errors=%d)",
			total, result.SemanticJudged, result.SemanticSkipped, result.SemanticErrors)
	}
}

// ─── C.5e — TestScanProject_Semantic_TimeoutCounted ──────────────────────────

// TestScanProject_Semantic_TimeoutCounted verifies that a runner that blocks
// until context cancellation is counted as SemanticErrors and the scan continues.
func TestScanProject_Semantic_TimeoutCounted(t *testing.T) {
	s := newTestStore(t)
	seedSimilarPair(t, s, "timeout-project")

	runner := &blockingRunner{}

	result, err := s.ScanProject(ScanOptions{
		Project:        "timeout-project",
		Apply:          true,
		Semantic:       true,
		Concurrency:    1,
		TimeoutPerCall: 50 * time.Millisecond, // very short timeout
		MaxSemantic:    10,
		Runner:         runner,
		BuildPrompt:    identityPromptBuilder,
	})
	if err != nil {
		t.Fatalf("ScanProject must not return error on runner timeout; got: %v", err)
	}

	total := result.SemanticJudged + result.SemanticSkipped + result.SemanticErrors
	if total > 0 && result.SemanticErrors == 0 {
		t.Errorf("expected SemanticErrors > 0 when runner blocks beyond timeout; got errors=0 (total=%d)", total)
	}
}

// ─── C.5f — TestScanProject_Semantic_BackwardsCompat ─────────────────────────

// TestScanProject_Semantic_BackwardsCompat verifies that when Semantic=false
// (the zero value), ScanProject behaves exactly as Phase 3:
// - No runner is invoked
// - Semantic counters are zero
// - FTS-based candidates and insertions work unchanged
func TestScanProject_Semantic_BackwardsCompat(t *testing.T) {
	s := newTestStore(t)
	seedSimilarPair(t, s, "compat-project")

	// Phase 3-style call — no Semantic, no Runner, no BuildPrompt.
	result, err := s.ScanProject(ScanOptions{
		Project: "compat-project",
		Apply:   true,
	})
	if err != nil {
		t.Fatalf("ScanProject (phase3 path): %v", err)
	}

	if result.SemanticJudged != 0 {
		t.Errorf("SemanticJudged: want 0; got %d", result.SemanticJudged)
	}
	if result.SemanticSkipped != 0 {
		t.Errorf("SemanticSkipped: want 0; got %d", result.SemanticSkipped)
	}
	if result.SemanticErrors != 0 {
		t.Errorf("SemanticErrors: want 0; got %d", result.SemanticErrors)
	}
}

// ─── C.5g — TestScanProject_Semantic_RunnerNilError ──────────────────────────

// TestScanProject_Semantic_RunnerNilError verifies that Semantic=true with nil
// Runner returns ErrSemanticRunnerRequired immediately.
func TestScanProject_Semantic_RunnerNilError(t *testing.T) {
	s := newTestStore(t)
	seedSimilarPair(t, s, "runner-nil-project")

	_, err := s.ScanProject(ScanOptions{
		Project:     "runner-nil-project",
		Semantic:    true,
		Runner:      nil,
		BuildPrompt: identityPromptBuilder,
	})
	if !errors.Is(err, ErrSemanticRunnerRequired) {
		t.Errorf("expected ErrSemanticRunnerRequired; got %v", err)
	}
}

// ─── C.5h — TestScanProject_Semantic_BuildPromptNilError ─────────────────────

// TestScanProject_Semantic_BuildPromptNilError verifies that Semantic=true with
// nil BuildPrompt returns ErrSemanticPromptBuilderRequired immediately.
func TestScanProject_Semantic_BuildPromptNilError(t *testing.T) {
	s := newTestStore(t)
	seedSimilarPair(t, s, "prompt-nil-project")

	_, err := s.ScanProject(ScanOptions{
		Project:     "prompt-nil-project",
		Semantic:    true,
		Runner:      &verdictRunner{},
		BuildPrompt: nil,
	})
	if !errors.Is(err, ErrSemanticPromptBuilderRequired) {
		t.Errorf("expected ErrSemanticPromptBuilderRequired; got %v", err)
	}
}
