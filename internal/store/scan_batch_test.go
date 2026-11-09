package store

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// scanBatchSeed describes one observation inserted by scanBatchStore.
type scanBatchSeed struct {
	title   string
	content string
	project string
	scope   string
	deleted bool
}

// scanBatchStore builds a mixed corpus for page-batched candidate ranking
// fixtures (#955): multiple projects, scopes, a soft-deleted row, judged and
// pending relations, all-short-term sources, and an over-limit candidate pool.
func scanBatchStore(t *testing.T) *Store {
	t.Helper()
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateSession("batch-session", "batch-alpha", "/tmp/batch"); err != nil {
		t.Fatal(err)
	}
	return s
}

func scanBatchAdd(t *testing.T, s *Store, seed scanBatchSeed) int64 {
	t.Helper()
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "batch-session",
		Type:      "decision",
		Title:     seed.title,
		Content:   seed.content,
		Project:   seed.project,
		Scope:     seed.scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if seed.deleted {
		if _, err := s.db.Exec(`UPDATE observations SET deleted_at = datetime('now') WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func scanBatchInsertRelation(t *testing.T, s *Store, syncID, sourceID, targetID, status string) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO memory_relations
			(sync_id, source_id, target_id, relation, judgment_status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, datetime('now'), datetime('now'))
	`, syncID, sourceID, targetID, status); err != nil {
		t.Fatal(err)
	}
}

func scanBatchSources(t *testing.T, s *Store, ids ...int64) []scanSourceRow {
	t.Helper()
	sources := make([]scanSourceRow, 0, len(ids))
	for _, id := range ids {
		var row scanSourceRow
		row.id = id
		if err := s.db.QueryRow(
			`SELECT ifnull(sync_id,''), scope, ifnull(project,''), title FROM observations WHERE id = ?`, id,
		).Scan(&row.syncID, &row.scope, &row.project, &row.title); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, row)
	}
	return sources
}

func scanBatchCandidateIDs(candidates []Candidate) []int64 {
	ids := make([]int64, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.ID)
	}
	return ids
}

func scanBatchSortedIDs(ids []int64) []int64 {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	return sorted
}

// TestScanCandidateBatch_MatchesLegacyRecall pins the recall contract: the
// page-batched path must surface exactly the same candidate set per source as
// the legacy per-source FindCandidates query, including every filter
// (project, scope, deleted, self, judged relations in either direction) and
// the all-short-term edge case. Recall semantics are ANY-term overlap across
// every indexed FTS column — a source term like "alpha" matches any row whose
// project column is "batch-alpha" — so this fixture exercises cross-column
// matching, not just title overlap.
func TestScanCandidateBatch_MatchesLegacyRecall(t *testing.T) {
	s := scanBatchStore(t)

	// Source S1 first so its sync_id is available for relation seeds.
	s1 := scanBatchAdd(t, s, scanBatchSeed{title: "alpha deploy pipeline notes", content: "source row", project: "batch-alpha", scope: "project"})
	var s1Sync string
	if err := s.db.QueryRow(`SELECT ifnull(sync_id,'') FROM observations WHERE id = ?`, s1).Scan(&s1Sync); err != nil {
		t.Fatal(err)
	}

	twin := scanBatchAdd(t, s, scanBatchSeed{title: "alpha deploy pipeline twin", content: "title overlap", project: "batch-alpha", scope: "project"})
	judgedPartner := scanBatchAdd(t, s, scanBatchSeed{title: "alpha deploy rollback guide", content: "judged pair", project: "batch-alpha", scope: "project"})
	gardening := scanBatchAdd(t, s, scanBatchSeed{title: "unrelated gardening tips", content: "no title overlap", project: "batch-alpha", scope: "project"})
	scanBatchAdd(t, s, scanBatchSeed{title: "alpha deploy scope mismatch row", content: "scope mismatch", project: "batch-alpha", scope: "personal"})
	scanBatchAdd(t, s, scanBatchSeed{title: "alpha deploy pipeline notes", content: "other project", project: "batch-beta", scope: "project"})
	scanBatchAdd(t, s, scanBatchSeed{title: "alpha deploy pipeline notes", content: "deleted row", project: "batch-alpha", scope: "project", deleted: true})
	pendingRow := scanBatchAdd(t, s, scanBatchSeed{title: "alpha deploy pending pair row", content: "pending pair", project: "batch-alpha", scope: "project"})
	shortTerm := scanBatchAdd(t, s, scanBatchSeed{title: "ab cd", content: "short terms only", project: "batch-alpha", scope: "project"})

	var judgedSync, pendingSync string
	if err := s.db.QueryRow(`SELECT ifnull(sync_id,'') FROM observations WHERE id = ?`, judgedPartner).Scan(&judgedSync); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT ifnull(sync_id,'') FROM observations WHERE id = ?`, pendingRow).Scan(&pendingSync); err != nil {
		t.Fatal(err)
	}
	scanBatchInsertRelation(t, s, "rel-batch-judged", s1Sync, judgedSync, "judged")
	scanBatchInsertRelation(t, s, "rel-batch-pending", s1Sync, pendingSync, "pending")

	sources := scanBatchSources(t, s, s1, shortTerm)
	batch, err := s.scanCandidateBatch(sources, 10)
	if err != nil {
		t.Fatalf("scanCandidateBatch: %v", err)
	}

	for _, src := range sources {
		legacy, err := s.FindCandidates(src.id, CandidateOptions{Project: "batch-alpha", Scope: src.scope, Limit: 10, SkipInsert: true})
		if err != nil {
			t.Fatalf("FindCandidates id=%d: %v", src.id, err)
		}
		got := scanBatchSortedIDs(scanBatchCandidateIDs(batch[src.id]))
		want := scanBatchSortedIDs(scanBatchCandidateIDs(legacy))
		if !slices.Equal(got, want) {
			t.Errorf("source id=%d title=%q: batch candidates %v, legacy %v", src.id, src.title, got, want)
		}
	}

	// S1 must see exactly: the title twin, the gardening row (matched via the
	// indexed project column "batch-alpha" containing the term "alpha"), the
	// pending pair row, and the short-term row (also matched via the project
	// column). Excluded: itself, the judged partner, the scope-mismatch row,
	// the other-project row, and the soft-deleted row.
	gotIDs := scanBatchSortedIDs(scanBatchCandidateIDs(batch[s1]))
	wantIDs := scanBatchSortedIDs([]int64{twin, gardening, pendingRow, shortTerm})
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("S1 candidate ids = %v, want %v", gotIDs, wantIDs)
	}
	if len(batch[shortTerm]) != 0 {
		t.Fatalf("all-short-term source must match nothing, got %v", scanBatchCandidateIDs(batch[shortTerm]))
	}
}

// TestScanCandidateBatch_DeterministicOrderAndScore pins the replacement
// scoring contract: candidates are ordered by matched-term count descending
// with candidate id ascending as the deterministic tie-break, the per-source
// limit is honored, and scores are deterministic integers reported as floats.
func TestScanCandidateBatch_DeterministicOrderAndScore(t *testing.T) {
	s := scanBatchStore(t)

	// Twelve mutual candidates that all share the full term set.
	pool := make([]int64, 0, 12)
	for i := 0; i < 12; i++ {
		pool = append(pool, scanBatchAdd(t, s, scanBatchSeed{
			title:   fmt.Sprintf("shared ranking corpus alpha omega %02d", i),
			content: "shared ranking corpus alpha omega body",
			project: "batch-alpha",
			scope:   "project",
		}))
	}
	// One weaker candidate sharing only two terms.
	weak := scanBatchAdd(t, s, scanBatchSeed{title: "shared ranking something else", content: "weak overlap", project: "batch-alpha", scope: "project"})
	source := scanBatchAdd(t, s, scanBatchSeed{title: "shared ranking corpus alpha omega unique", content: "source row", project: "batch-alpha", scope: "project"})

	sources := scanBatchSources(t, s, source)
	batch, err := s.scanCandidateBatch(sources, 10)
	if err != nil {
		t.Fatalf("scanCandidateBatch: %v", err)
	}
	got := batch[source]
	if len(got) != 10 {
		t.Fatalf("limit: got %d candidates, want 10", len(got))
	}
	// The weak candidate (fewer matched terms) must lose every seat when the
	// pool alone already fills the limit.
	for _, c := range got {
		if c.ID == weak {
			t.Fatalf("weak candidate must not displace a full-term candidate: %+v", got)
		}
	}
	// All winners share the full term set: order must be id-ascending.
	for i := 1; i < len(got); i++ {
		if got[i-1].ID >= got[i].ID {
			t.Fatalf("deterministic order violated: %+v", scanBatchCandidateIDs(got))
		}
	}
	wantScore := float64(5) // shared, ranking, corpus, alpha, omega
	for _, c := range got {
		if c.Score != wantScore {
			t.Fatalf("candidate id=%d score = %f, want %f", c.ID, c.Score, wantScore)
		}
	}
	// Determinism: a second batch run returns the identical slice.
	again, err := s.scanCandidateBatch(sources, 10)
	if err != nil {
		t.Fatalf("scanCandidateBatch (2nd): %v", err)
	}
	if !slices.Equal(scanBatchCandidateIDs(got), scanBatchCandidateIDs(again[source])) {
		t.Fatalf("non-deterministic batch: %v vs %v", scanBatchCandidateIDs(got), scanBatchCandidateIDs(again[source]))
	}
}

// TestScanCandidateBatch_NonPositiveLimitMirrorsLegacyDefault pins the R3
// follow-up from the PR #1017 review: a non-positive candidate limit mirrors
// the legacy FindCandidates default instead of silently returning nothing.
func TestScanCandidateBatch_NonPositiveLimitMirrorsLegacyDefault(t *testing.T) {
	s := scanBatchStore(t)
	source := scanBatchAdd(t, s, scanBatchSeed{title: "limit probe mutual vocabulary row", content: "source", project: "batch-alpha", scope: "project"})
	for i := 0; i < 5; i++ {
		scanBatchAdd(t, s, scanBatchSeed{
			title:   fmt.Sprintf("limit probe mutual vocabulary twin %d", i),
			content: "twin",
			project: "batch-alpha",
			scope:   "project",
		})
	}
	sources := scanBatchSources(t, s, source)
	batch, err := s.scanCandidateBatch(sources, 0)
	if err != nil {
		t.Fatalf("scanCandidateBatch: %v", err)
	}
	if len(batch[source]) != defaultCandidateLimit {
		t.Fatalf("zero limit: got %d candidates, want the legacy default %d", len(batch[source]), defaultCandidateLimit)
	}
}

// TestScanCandidateBatch_ErrorsOmitTitleTerms pins the R1 follow-up from the
// PR #1017 review: batch error strings never embed title-derived FTS terms,
// because those errors reach the store log via the fallback path and the
// legacy path logged only observation sync IDs.
func TestScanCandidateBatch_ErrorsOmitTitleTerms(t *testing.T) {
	s := scanBatchStore(t)
	source := scanBatchAdd(t, s, scanBatchSeed{title: "secretword unshared token", content: "source", project: "batch-alpha", scope: "project"})
	if _, err := s.db.Exec(`DROP TABLE observations_fts`); err != nil {
		t.Fatalf("drop fts: %v", err)
	}
	sources := scanBatchSources(t, s, source)
	_, err := s.scanCandidateBatch(sources, 10)
	if err == nil {
		t.Fatal("expected an error with the FTS table dropped")
	}
	for _, term := range []string{"secretword", "unshared", "token"} {
		if strings.Contains(err.Error(), term) {
			t.Fatalf("batch error leaks title-derived term %q: %v", term, err)
		}
	}
}

// TestScanProject_BatchFallbackWhenFTSBroken pins the failure contract: when
// the batched path cannot run, the scan falls back to the legacy per-source
// path, which logs and skips sources — the scan still succeeds with inspected
// counters intact and zero candidates, exactly as on main.
func TestScanProject_BatchFallbackWhenFTSBroken(t *testing.T) {
	s := scanBatchStore(t)
	ids := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		ids = append(ids, scanBatchAdd(t, s, scanBatchSeed{
			title:   fmt.Sprintf("fallback corpus probe %d", i),
			content: "fallback corpus body",
			project: "batch-alpha",
			scope:   "project",
		}))
	}
	if _, err := s.db.Exec(`DROP TABLE observations_fts`); err != nil {
		t.Fatalf("drop fts: %v", err)
	}
	result, err := s.ScanProject(ScanOptions{Project: "batch-alpha", Limit: 2})
	if err != nil {
		t.Fatalf("ScanProject with broken FTS: %v", err)
	}
	if result.Inspected != 2 || result.RankedQueries != 2 {
		t.Fatalf("counters with broken FTS: %+v", result)
	}
	if result.CandidatesFound != 0 {
		t.Fatalf("candidates with broken FTS: %+v", result)
	}
	if result.NextCursor == nil || *result.NextCursor != ids[1] {
		t.Fatalf("pagination with broken FTS: %+v", result)
	}
}
