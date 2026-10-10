package store

import (
	"strings"
	"testing"
)

// Tests for ConsolidateObservations (issue #242, design 2026-09-27).
// One test per rule: plan-only no-writes (S5), atomicity (S1), journal order
// survivor-first (S2), unenrolled supersede path (S2), refusals (S3),
// fingerprint drift (S3/S5), provenance cycle (S3), survivor defaults (S4).

// consolidateTestStore returns an enrolled store with three live observations
// in one project, created newest-last (id monotonic). The newest source is the
// last one added.
func consolidateTestStore(t *testing.T) (s *Store, ids []int64, syncIDs []string) {
	t.Helper()
	s = newTestStore(t)
	if err := s.CreateSession("ses-cons", "proj-cons", "/tmp/cons"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.EnrollProject("proj-cons"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	for _, title := range []string{"Oldest decision", "Middle decision", "Newest decision"} {
		id, syncID := addTestObsSession(t, s, "ses-cons", title, "decision", "proj-cons", "project")
		ids = append(ids, id)
		syncIDs = append(syncIDs, syncID)
	}
	return s, ids, syncIDs
}

type mutationRow struct {
	seq       int64
	entity    string
	entityKey string
	op        string
}

// consolidateMutations returns sync_mutations rows with seq above the given
// floor for the given entity keys (the floor excludes seeding rows).
func consolidateMutations(t *testing.T, s *Store, floor int64, keys ...string) []mutationRow {
	t.Helper()
	rows, err := s.db.Query(
		`SELECT seq, entity, entity_key, op FROM sync_mutations WHERE seq > ? AND entity_key IN (?,?,?,?) ORDER BY seq`,
		floor, keys[0], keys[1], keys[2], keys[3],
	)
	if err != nil {
		t.Fatalf("query sync_mutations: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []mutationRow
	for rows.Next() {
		var r mutationRow
		if err := rows.Scan(&r.seq, &r.entity, &r.entityKey, &r.op); err != nil {
			t.Fatalf("scan sync_mutations: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func maxSyncMutationSeq(t *testing.T, s *Store) int64 {
	t.Helper()
	var floor int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM sync_mutations`).Scan(&floor); err != nil {
		t.Fatalf("max sync_mutations seq: %v", err)
	}
	return floor
}

func countAllSyncMutations(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&n); err != nil {
		t.Fatalf("count sync_mutations: %v", err)
	}
	return n
}

func observationLiveCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	return n
}

// S5: dry-run is the default contract — Confirm=false must return the full
// plan (sources newest-first, synthesized survivor, provenance preview,
// fingerprint) and change nothing.
func TestConsolidateObservations_PlanOnly_NoWrites(t *testing.T) {
	s, ids, syncIDs := consolidateTestStore(t)
	beforeObs, beforeMut := observationLiveCount(t, s), countAllSyncMutations(t, s)

	res, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if res.Survivor != nil {
		t.Fatal("dry-run must not return a survivor")
	}
	plan := res.Plan
	if plan == nil {
		t.Fatal("dry-run must return a plan")
	}
	if len(plan.Sources) != 3 {
		t.Fatalf("plan sources: got %d, want 3", len(plan.Sources))
	}
	if plan.Sources[0].Title != "Newest decision" || plan.Sources[2].Title != "Oldest decision" {
		t.Fatalf("sources must be newest-first, got [%s, %s, %s]",
			plan.Sources[0].Title, plan.Sources[1].Title, plan.Sources[2].Title)
	}
	if plan.Fingerprint == "" {
		t.Fatal("plan fingerprint must be non-empty")
	}
	// Synthesized defaults come newest-first; provenance always rides content.
	if plan.Survivor.SessionID != "ses-cons" {
		t.Fatalf("survivor session: got %q, want ses-cons (newest source)", plan.Survivor.SessionID)
	}
	if !strings.Contains(plan.Survivor.Title, "Newest decision") {
		t.Fatalf("synthesized title must lead with newest source, got %q", plan.Survivor.Title)
	}
	for _, sid := range syncIDs {
		if !strings.Contains(plan.Survivor.Content, sid) {
			t.Fatalf("survivor content must list source %s in the consolidated_from section", sid)
		}
	}
	if !strings.Contains(plan.Survivor.Content, "consolidated_from") {
		t.Fatal("survivor content must carry the consolidated_from marker (provenance option a)")
	}
	if plan.Survivor.TopicKey != "" {
		t.Fatalf("topic_key must be explicit-only, synthesized got %q", plan.Survivor.TopicKey)
	}
	if got := observationLiveCount(t, s); got != beforeObs {
		t.Fatalf("dry-run mutated observations: %d -> %d", beforeObs, got)
	}
	if got := countAllSyncMutations(t, s); got != beforeMut {
		t.Fatalf("dry-run enqueued mutations: %d -> %d", beforeMut, got)
	}
}

// S1 + S2 (enrolled): execute consolidates atomically and journals the
// survivor upsert BEFORE the source deletes.
func TestConsolidateObservations_Execute_JournalOrderSurvivorFirst(t *testing.T) {
	s, ids, syncIDs := consolidateTestStore(t)

	plan, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	floor := maxSyncMutationSeq(t, s)
	res, err := s.ConsolidateObservations(ConsolidateParams{
		IDs:         ids,
		Confirm:     true,
		Fingerprint: plan.Plan.Fingerprint,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	surv := res.Survivor
	if surv == nil {
		t.Fatal("execute must return the survivor")
	}
	if surv.SyncID == "" || surv.RevisionCount != 1 || surv.DuplicateCount != 1 {
		t.Fatalf("survivor identity/defaults wrong: %+v", surv)
	}
	if surv.SessionID != "ses-cons" {
		t.Fatalf("survivor session attribution must be the newest source's session, got %q", surv.SessionID)
	}
	if surv.DeletedAt != nil {
		t.Fatal("survivor must be live")
	}
	// Sources soft-deleted, survivor live.
	if got := observationLiveCount(t, s); got != 1 {
		t.Fatalf("live observations after consolidation: got %d, want 1 (survivor)", got)
	}

	rows := consolidateMutations(t, s, floor, append(syncIDs, surv.SyncID)...)
	if len(rows) != 4 {
		t.Fatalf("journal rows for consolidation: got %d, want 4 (1 upsert + 3 deletes)", len(rows))
	}
	first := rows[0]
	if first.entityKey != surv.SyncID || first.op != SyncOpUpsert {
		t.Fatalf("first journal row must be the survivor upsert, got key=%s op=%s", first.entityKey, first.op)
	}
	seenDeletes := map[string]bool{}
	for _, r := range rows[1:] {
		if r.op != SyncOpDelete {
			t.Fatalf("rows after the survivor must be deletes, got op=%s for %s", r.op, r.entityKey)
		}
		seenDeletes[r.entityKey] = true
	}
	for _, sid := range syncIDs {
		if !seenDeletes[sid] {
			t.Fatalf("missing delete journal row for source %s", sid)
		}
	}
}

// S1: a failed survivor insert rolls back mutations AND journal rows — a
// failed insert can never leave the deletes applied.
func TestConsolidateObservations_AtomicRollback_InsertFailure(t *testing.T) {
	s, ids, _ := consolidateTestStore(t)
	plan, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// Deterministic mid-transaction failure: abort any survivor insert whose
	// title starts with BOOM.
	if _, err := s.db.Exec(`
		CREATE TRIGGER fail_consolidate_survivor
		BEFORE INSERT ON observations
		WHEN NEW.title LIKE 'BOOM%'
		BEGIN SELECT RAISE(ABORT, 'forced survivor insert failure'); END
	`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	beforeMut := countAllSyncMutations(t, s)
	_, err = s.ConsolidateObservations(ConsolidateParams{
		IDs:         ids,
		Confirm:     true,
		Fingerprint: plan.Plan.Fingerprint,
		Overrides:   ConsolidateOverrides{Title: "BOOM survivor"},
	})
	if err == nil {
		t.Fatal("expected the forced insert failure to surface")
	}
	if got := observationLiveCount(t, s); got != 3 {
		t.Fatalf("rollback failed: live observations got %d, want 3 (sources must stay live)", got)
	}
	if got := countAllSyncMutations(t, s); got != beforeMut {
		t.Fatalf("rollback failed: journal rows changed %d -> %d", beforeMut, got)
	}
}

// S2 (unenrolled): no cloud mutation rows at all; the sources take the
// supersede-pending path like DeleteObservation does.
func TestConsolidateObservations_Unenrolled_NoJournalRows(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-unen", "proj-unen", "/tmp/unen"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var ids []int64
	for _, title := range []string{"Unenrolled A", "Unenrolled B"} {
		id, _ := addTestObsSession(t, s, "ses-unen", title, "decision", "proj-unen", "project")
		ids = append(ids, id)
	}

	plan, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids, Confirm: true, Fingerprint: plan.Plan.Fingerprint})
	if err != nil {
		t.Fatalf("execute unenrolled: %v", err)
	}
	if res.Survivor == nil {
		t.Fatal("execute must return the survivor even when unenrolled")
	}
	if got := observationLiveCount(t, s); got != 1 {
		t.Fatalf("live observations: got %d, want 1", got)
	}
	if got := countAllSyncMutations(t, s); got != 0 {
		t.Fatalf("unenrolled project must journal nothing, got %d rows", got)
	}
}

// S3: refusal set — missing ID, single ID, duplicate ID, already-soft-deleted
// ID, cross-project sources. Every refusal must leave data and journal
// untouched.
func TestConsolidateObservations_Refuses(t *testing.T) {
	s, ids, _ := consolidateTestStore(t)

	cases := []struct {
		name string
		ids  []int64
		want error
	}{
		{"missing id", []int64{ids[0], 99999}, ErrConsolidateMissingObservation},
		{"single id", []int64{ids[0]}, ErrConsolidateNeedTwoSources},
		{"duplicate id", []int64{ids[0], ids[0]}, ErrConsolidateDuplicateID},
	}
	for _, tc := range cases {
		beforeObs, beforeMut := observationLiveCount(t, s), countAllSyncMutations(t, s)
		res, err := s.ConsolidateObservations(ConsolidateParams{IDs: tc.ids})
		if err != tc.want {
			t.Fatalf("%s: got err=%v, want %v", tc.name, err, tc.want)
		}
		if res != nil && res.Plan != nil {
			t.Fatalf("%s: refusal must not return a plan", tc.name)
		}
		if got := observationLiveCount(t, s); got != beforeObs {
			t.Fatalf("%s: mutated observations", tc.name)
		}
		if got := countAllSyncMutations(t, s); got != beforeMut {
			t.Fatalf("%s: enqueued mutations", tc.name)
		}
	}

	// Already-soft-deleted source.
	if err := s.DeleteObservation(ids[2], false); err != nil {
		t.Fatalf("seed delete: %v", err)
	}
	beforeObs, beforeMut := observationLiveCount(t, s), countAllSyncMutations(t, s)
	if _, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids}); err != ErrConsolidateMissingObservation {
		t.Fatalf("already-deleted: got err=%v, want %v", err, ErrConsolidateMissingObservation)
	}
	if got := observationLiveCount(t, s); got != beforeObs || countAllSyncMutations(t, s) != beforeMut {
		t.Fatal("already-deleted refusal must not write")
	}

	// Cross-project sources.
	if err := s.CreateSession("ses-other", "proj-other", "/tmp/other"); err != nil {
		t.Fatalf("CreateSession other: %v", err)
	}
	idOther, _ := addTestObsSession(t, s, "ses-other", "Other project obs", "decision", "proj-other", "project")
	beforeObs, beforeMut = observationLiveCount(t, s), countAllSyncMutations(t, s)
	if _, err := s.ConsolidateObservations(ConsolidateParams{IDs: []int64{ids[0], idOther}}); err != ErrConsolidateCrossProject {
		t.Fatalf("cross-project: got err=%v, want %v", err, ErrConsolidateCrossProject)
	}
	if got := observationLiveCount(t, s); got != beforeObs || countAllSyncMutations(t, s) != beforeMut {
		t.Fatal("cross-project refusal must not write")
	}
}

// S3/S5: a source change between plan and confirm is refused by the
// fingerprint mismatch and re-planned, never guessed.
func TestConsolidateObservations_FingerprintDrift_RefusesAndReplans(t *testing.T) {
	s, ids, _ := consolidateTestStore(t)
	plan, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// Local edit to a source between plan and confirm.
	if _, err := s.db.Exec(
		`UPDATE observations SET updated_at = datetime('now', '+1 hour'), revision_count = revision_count + 1 WHERE id = ?`,
		ids[1],
	); err != nil {
		t.Fatalf("seed drift: %v", err)
	}

	beforeObs, beforeMut := observationLiveCount(t, s), countAllSyncMutations(t, s)
	res, err := s.ConsolidateObservations(ConsolidateParams{
		IDs:         ids,
		Confirm:     true,
		Fingerprint: plan.Plan.Fingerprint,
	})
	if err != ErrConsolidateFingerprintMismatch {
		t.Fatalf("drift: got err=%v, want %v", err, ErrConsolidateFingerprintMismatch)
	}
	if res == nil || res.Plan == nil {
		t.Fatal("drift refusal must return a fresh plan for re-confirmation")
	}
	if res.Plan.Fingerprint == plan.Plan.Fingerprint {
		t.Fatal("fresh plan fingerprint must differ after drift")
	}
	if got := observationLiveCount(t, s); got != beforeObs {
		t.Fatalf("drift refusal mutated observations: %d -> %d", beforeObs, got)
	}
	if got := countAllSyncMutations(t, s); got != beforeMut {
		t.Fatalf("drift refusal enqueued mutations: %d -> %d", beforeMut, got)
	}
}

// S3: a provenance cycle (a live source whose consolidated_from section
// already lists another live source of the same batch) is refused.
func TestConsolidateObservations_ProvenanceCycle_Refuses(t *testing.T) {
	s, ids, syncIDs := consolidateTestStore(t)
	// Tamper: source 0 claims it was already consolidated from source 2.
	if _, err := s.db.Exec(
		`UPDATE observations SET content = content || char(10) || char(10) || '## consolidated_from' || char(10) || '- ' || ? WHERE id = ?`,
		syncIDs[2], ids[0],
	); err != nil {
		t.Fatalf("seed cycle: %v", err)
	}

	beforeObs, beforeMut := observationLiveCount(t, s), countAllSyncMutations(t, s)
	res, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != ErrConsolidateProvenanceCycle {
		t.Fatalf("cycle: got err=%v, want %v", err, ErrConsolidateProvenanceCycle)
	}
	if res != nil && res.Plan != nil {
		t.Fatal("cycle refusal must not return a plan")
	}
	if got := observationLiveCount(t, s); got != beforeObs {
		t.Fatalf("cycle refusal mutated observations: %d -> %d", beforeObs, got)
	}
	if got := countAllSyncMutations(t, s); got != beforeMut {
		t.Fatalf("cycle refusal enqueued mutations: %d -> %d", beforeMut, got)
	}
}

// S4: explicit overrides win; synthesized defaults are newest-first and
// revision_count starts at 1; topic_key stays explicit-only.
func TestConsolidateObservations_SurvivorDefaults(t *testing.T) {
	s, ids, _ := consolidateTestStore(t)
	overrides := ConsolidateOverrides{
		Title:    "Explicit survivor title",
		Content:  "Explicit survivor content.",
		Type:     "decision",
		TopicKey: "architecture/auth-model",
	}
	plan, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids, Overrides: overrides})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := s.ConsolidateObservations(ConsolidateParams{
		IDs:         ids,
		Overrides:   overrides,
		Confirm:     true,
		Fingerprint: plan.Plan.Fingerprint,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	surv := res.Survivor
	if surv.Title != "Explicit survivor title" || surv.Content == "Explicit survivor content." {
		t.Fatalf("explicit title/content must win (content still carries provenance), got title=%q", surv.Title)
	}
	if !strings.Contains(surv.Content, "Explicit survivor content.") || !strings.Contains(surv.Content, "consolidated_from") {
		t.Fatalf("explicit content must be preserved with provenance appended, got %q", surv.Content)
	}
	if derefString(surv.TopicKey) != "architecture/auth-model" {
		t.Fatalf("explicit topic_key must be honored, got %q", derefString(surv.TopicKey))
	}
	if surv.Type != "decision" {
		t.Fatalf("survivor type: got %q, want decision", surv.Type)
	}
}
