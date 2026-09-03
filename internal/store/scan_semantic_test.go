package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

// ─── Mock runner helpers ─────────────────────────────────────────────────────

// verdictRunner returns a fixed SemanticVerdict for every Compare call.
type verdictRunner struct {
	verdict SemanticVerdict
	mu      sync.Mutex
	calls   int
}

func (r *verdictRunner) Compare(_ context.Context, _ string) (SemanticVerdict, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
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
	runners []SemanticRunner
	callIdx int
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

// TestScanProject_Semantic_DryRunDoesNotPersist verifies that semantic dry-runs
// evaluate valid verdicts without changing relation or sync state.
func TestScanProject_Semantic_DryRunDoesNotPersist(t *testing.T) {
	for _, tc := range []struct {
		name              string
		confidence        float64
		wantJudged        bool
		wantSemanticError bool
	}{
		{name: "valid confidence", confidence: 0.9, wantJudged: true},
		{name: "NaN confidence", confidence: math.NaN(), wantSemanticError: true},
		{name: "positive infinite confidence", confidence: math.Inf(1), wantSemanticError: true},
		{name: "negative infinite confidence", confidence: math.Inf(-1), wantSemanticError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			_, syncA, _, syncB := seedSimilarPair(t, s, "semantic-dry-run-project")

			if _, err := s.db.Exec(`
		INSERT INTO memory_relations
			(sync_id, source_id, target_id, relation, judgment_status, confidence, reason, created_at, updated_at)
		VALUES (?, ?, ?, 'related', 'pending', 0.2, 'awaiting judgment', datetime('now'), datetime('now'))
			`, "rel-semantic-dry-run", syncA, syncB); err != nil {
				t.Fatalf("seed pending relation: %v", err)
			}
			if err := s.EnrollProject("semantic-dry-run-project"); err != nil {
				t.Fatalf("EnrollProject: %v", err)
			}

			var relationsBefore, mutationsBefore int
			if err := s.db.QueryRow(`SELECT count(*) FROM memory_relations`).Scan(&relationsBefore); err != nil {
				t.Fatalf("count relations before scan: %v", err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsBefore); err != nil {
				t.Fatalf("count sync mutations before scan: %v", err)
			}
			syncStateBefore, err := s.GetSyncState(DefaultSyncTargetKey)
			if err != nil {
				t.Fatalf("GetSyncState before scan: %v", err)
			}

			runner := &verdictRunner{
				verdict: SemanticVerdict{
					Relation:   "compatible",
					Confidence: tc.confidence,
					Reasoning:  "both discuss JWT auth",
					Model:      "haiku",
				},
			}
			result, err := s.ScanProject(ScanOptions{
				Project:        "semantic-dry-run-project",
				Apply:          false,
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
			if tc.wantJudged && (result.SemanticJudged == 0 || result.SemanticErrors != 0) {
				t.Errorf("semantic counters = judged:%d errors:%d, want judged > 0 and errors = 0", result.SemanticJudged, result.SemanticErrors)
			}
			if tc.wantSemanticError && (result.SemanticJudged != 0 || result.SemanticErrors == 0) {
				t.Errorf("semantic counters = judged:%d errors:%d, want judged = 0 and errors > 0", result.SemanticJudged, result.SemanticErrors)
			}
			if runner.calls == 0 {
				t.Error("semantic runner was not called during dry-run")
			}

			var relationsAfter, mutationsAfter int
			if err := s.db.QueryRow(`SELECT count(*) FROM memory_relations`).Scan(&relationsAfter); err != nil {
				t.Fatalf("count relations after scan: %v", err)
			}
			if relationsAfter != relationsBefore {
				t.Errorf("relation count = %d, want unchanged %d", relationsAfter, relationsBefore)
			}

			var relation, status, reason string
			var confidence float64
			if err := s.db.QueryRow(`
		SELECT relation, judgment_status, confidence, reason
		FROM memory_relations WHERE sync_id = ?
			`, "rel-semantic-dry-run").Scan(&relation, &status, &confidence, &reason); err != nil {
				t.Fatalf("load pending relation after scan: %v", err)
			}
			if relation != "related" || status != "pending" || confidence != 0.2 || reason != "awaiting judgment" {
				t.Errorf("pending relation mutated: relation=%q status=%q confidence=%v reason=%q", relation, status, confidence, reason)
			}

			if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsAfter); err != nil {
				t.Fatalf("count sync mutations after scan: %v", err)
			}
			if mutationsAfter != mutationsBefore {
				t.Errorf("sync mutation count = %d, want unchanged %d", mutationsAfter, mutationsBefore)
			}
			syncStateAfter, err := s.GetSyncState(DefaultSyncTargetKey)
			if err != nil {
				t.Fatalf("GetSyncState after scan: %v", err)
			}
			if syncStateAfter.LastEnqueuedSeq != syncStateBefore.LastEnqueuedSeq || syncStateAfter.Lifecycle != syncStateBefore.Lifecycle {
				t.Errorf("sync state changed: before last_enqueued_seq=%d lifecycle=%q; after last_enqueued_seq=%d lifecycle=%q", syncStateBefore.LastEnqueuedSeq, syncStateBefore.Lifecycle, syncStateAfter.LastEnqueuedSeq, syncStateAfter.Lifecycle)
			}
		})
	}
}

// ─── C.5b — TestScanProject_Semantic_NotConflictSkipped ──────────────────────

// TestScanProject_Semantic_NotConflictDryRunIsSkipped verifies that dry-run
// not_conflict verdicts are counted in SemanticSkipped without relation rows.
func TestScanProject_Semantic_NotConflictDryRunIsSkipped(t *testing.T) {
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
		Apply:          false,
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

func TestScanProject_Semantic_NotConflictPersistsAndSuppressesRepeat(t *testing.T) {
	s := newTestStore(t)
	_, syncA, _, syncB := seedSimilarPair(t, s, "sem-persist-project")
	if err := s.EnrollProject("sem-persist-project"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	runner := &verdictRunner{verdict: SemanticVerdict{Relation: RelationNotConflict, Confidence: 0.99}}
	opts := ScanOptions{
		Project: "sem-persist-project", Apply: true, Semantic: true, Concurrency: 1,
		TimeoutPerCall: 5 * time.Second, MaxSemantic: 10, Runner: runner, BuildPrompt: identityPromptBuilder,
	}
	result, err := s.ScanProject(opts)
	if err != nil {
		t.Fatalf("first ScanProject: %v", err)
	}
	if result.SemanticJudged != 1 || result.SemanticSkipped != 0 {
		t.Fatalf("first semantic counters = judged:%d skipped:%d, want judged:1 skipped:0", result.SemanticJudged, result.SemanticSkipped)
	}
	var persisted, mutations int
	if err := s.db.QueryRow(`SELECT count(*) FROM memory_relations WHERE relation = ? AND ((source_id = ? AND target_id = ?) OR (source_id = ? AND target_id = ?))`, RelationNotConflict, syncA, syncB, syncB, syncA).Scan(&persisted); err != nil {
		t.Fatalf("count persisted relation: %v", err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity = ?`, SyncEntityRelation).Scan(&mutations); err != nil {
		t.Fatalf("count relation mutations: %v", err)
	}
	if persisted != 1 || mutations == 0 {
		t.Fatalf("persisted=%d mutations=%d, want one persisted relation and a sync mutation", persisted, mutations)
	}

	repeatRunner := &verdictRunner{verdict: SemanticVerdict{Relation: RelationNotConflict, Confidence: 0.99}}
	opts.Runner = repeatRunner
	repeat, err := s.ScanProject(opts)
	if err != nil {
		t.Fatalf("repeat ScanProject: %v", err)
	}
	if repeat.SemanticJudged != 0 || repeatRunner.calls != 0 {
		t.Fatalf("repeat scan re-evaluated persisted pair: judged=%d calls=%d", repeat.SemanticJudged, repeatRunner.calls)
	}
}

// TestScanProject_Semantic_InvalidNotConflictConfidence verifies that invalid
// not_conflict verdicts are rejected without changing relation or sync state.
func TestScanProject_Semantic_InvalidNotConflictConfidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		apply      bool
		confidence float64
	}{
		{name: "apply/NaN", apply: true, confidence: math.NaN()},
		{name: "apply/positive infinity", apply: true, confidence: math.Inf(1)},
		{name: "apply/negative infinity", apply: true, confidence: math.Inf(-1)},
		{name: "dry-run/NaN", confidence: math.NaN()},
		{name: "dry-run/positive infinity", confidence: math.Inf(1)},
		{name: "dry-run/negative infinity", confidence: math.Inf(-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			_, _, _, _ = seedSimilarPair(t, s, "invalid-not-conflict-project")
			if err := s.EnrollProject("invalid-not-conflict-project"); err != nil {
				t.Fatalf("EnrollProject: %v", err)
			}

			var relationsBefore, mutationsBefore int
			if err := s.db.QueryRow(`SELECT count(*) FROM memory_relations`).Scan(&relationsBefore); err != nil {
				t.Fatalf("count relations before scan: %v", err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsBefore); err != nil {
				t.Fatalf("count sync mutations before scan: %v", err)
			}
			syncStateBefore, err := s.GetSyncState(DefaultSyncTargetKey)
			if err != nil {
				t.Fatalf("GetSyncState before scan: %v", err)
			}

			runner := &verdictRunner{verdict: SemanticVerdict{
				Relation:   RelationNotConflict,
				Confidence: tc.confidence,
			}}
			result, err := s.ScanProject(ScanOptions{
				Project:        "invalid-not-conflict-project",
				Apply:          tc.apply,
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
			if result.SemanticErrors == 0 || result.SemanticSkipped != 0 || result.SemanticJudged != 0 {
				t.Errorf("semantic counters = judged:%d skipped:%d errors:%d, want judged:0 skipped:0 errors:>0", result.SemanticJudged, result.SemanticSkipped, result.SemanticErrors)
			}
			if runner.calls == 0 {
				t.Fatal("semantic runner was not called")
			}

			var relationsAfter, mutationsAfter int
			if err := s.db.QueryRow(`SELECT count(*) FROM memory_relations`).Scan(&relationsAfter); err != nil {
				t.Fatalf("count relations after scan: %v", err)
			}
			if relationsAfter != relationsBefore {
				t.Errorf("relation count = %d, want unchanged %d", relationsAfter, relationsBefore)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsAfter); err != nil {
				t.Fatalf("count sync mutations after scan: %v", err)
			}
			if mutationsAfter != mutationsBefore {
				t.Errorf("sync mutation count = %d, want unchanged %d", mutationsAfter, mutationsBefore)
			}
			syncStateAfter, err := s.GetSyncState(DefaultSyncTargetKey)
			if err != nil {
				t.Fatalf("GetSyncState after scan: %v", err)
			}
			if syncStateAfter.LastEnqueuedSeq != syncStateBefore.LastEnqueuedSeq || syncStateAfter.Lifecycle != syncStateBefore.Lifecycle {
				t.Errorf("sync state changed: before last_enqueued_seq=%d lifecycle=%q; after last_enqueued_seq=%d lifecycle=%q", syncStateBefore.LastEnqueuedSeq, syncStateBefore.Lifecycle, syncStateAfter.LastEnqueuedSeq, syncStateAfter.Lifecycle)
			}
		})
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
	if runner.calls > 2 {
		t.Errorf("MaxSemantic=2: runner calls = %d, want <= 2", runner.calls)
	}
}

func TestScanProject_Semantic_MaxSemanticCapIgnoresDuplicateTail(t *testing.T) {
	s := newTestStore(t)
	seedSimilarPair(t, s, "cap-duplicate-tail-project")
	runner := &verdictRunner{verdict: SemanticVerdict{Relation: RelationCompatible, Confidence: 0.8}}
	result, err := s.ScanProject(ScanOptions{
		Project: "cap-duplicate-tail-project", Apply: true, Semantic: true, Concurrency: 1,
		TimeoutPerCall: 5 * time.Second, MaxSemantic: 1, Runner: runner, BuildPrompt: identityPromptBuilder,
	})
	if err != nil {
		t.Fatalf("ScanProject: %v", err)
	}
	if result.Capped {
		t.Fatal("duplicate-only semantic remainder must not cap the scan")
	}
	if runner.calls != 1 || result.SemanticJudged != 1 {
		t.Fatalf("runner calls=%d judged=%d, want 1", runner.calls, result.SemanticJudged)
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
