package store

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// relationDeferredPayloadIDs returns the payload sync_id of every relation row
// in sync_apply_deferred. The payload identity is used instead of the row key
// so the assertions describe which mutations were preserved rather than how
// they happen to be keyed.
func relationDeferredPayloadIDs(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query(`
		SELECT ifnull(json_extract(payload, '$.sync_id'), '')
		FROM sync_apply_deferred
		WHERE entity = 'relation'
	`)
	if err != nil {
		t.Fatalf("relationDeferredPayloadIDs: query: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("relationDeferredPayloadIDs: scan: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("relationDeferredPayloadIDs: rows: %v", err)
	}
	sort.Strings(ids)
	return ids
}

// countRelationDeferredRows returns how many relation rows exist in
// sync_apply_deferred regardless of key.
func countRelationDeferredRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM sync_apply_deferred WHERE entity = 'relation'`,
	).Scan(&n); err != nil {
		t.Fatalf("countRelationDeferredRows: %v", err)
	}
	return n
}

// countRelationDeferredRowsForPayload returns how many relation rows carry the
// given payload sync_id.
func countRelationDeferredRowsForPayload(t *testing.T, s *Store, relSyncID string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`
		SELECT count(*)
		FROM sync_apply_deferred
		WHERE entity = 'relation'
		  AND ifnull(json_extract(payload, '$.sync_id'), '') = ?
	`, relSyncID).Scan(&n); err != nil {
		t.Fatalf("countRelationDeferredRowsForPayload: %v", err)
	}
	return n
}

// relationPayloadJSON builds a well-formed relation payload for the given
// endpoints.
func relationPayloadJSON(t *testing.T, relSyncID, sourceID, targetID string) string {
	t.Helper()
	actor := "test-actor"
	kind := "test"
	raw, err := json.Marshal(syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       sourceID,
		TargetID:       targetID,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		MarkedByActor:  &actor,
		MarkedByKind:   &kind,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("relationPayloadJSON: marshal: %v", err)
	}
	return string(raw)
}

// blankEntityKeyRelationMutation builds a relation mutation whose payload is
// well-formed but whose entity_key is blank. applyRelationUpsertTx rejects it as
// terminal evidence, so it is the shape that used to collapse rows together.
func blankEntityKeyRelationMutation(t *testing.T, relSyncID, sourceID, targetID string) SyncMutation {
	t.Helper()
	return SyncMutation{
		Entity:    SyncEntityRelation,
		EntityKey: "",
		Op:        SyncOpUpsert,
		Payload:   relationPayloadJSON(t, relSyncID, sourceID, targetID),
		Source:    SyncSourceRemote,
		Project:   "proj-apply",
	}
}

// ─── Issue #838 — relation dead-letter identity ───────────────────────────────

// Two distinct failed relation mutations that both carry a blank entity_key must
// each keep their own evidence row. Keying the row on entity_key directly made
// the second mutation overwrite the first.
func TestRelationDeadLetter_DistinctBlankEntityKeysKeepSeparateRows(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	mutations := []SyncMutation{
		blankEntityKeyRelationMutation(t, "rel-a", syncA, syncB),
		blankEntityKeyRelationMutation(t, "rel-b", syncA, syncB),
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-blank-entity-keys", mutations); err != nil {
		t.Fatalf("ApplyPulledChunk: %v", err)
	}

	got := relationDeferredPayloadIDs(t, s)
	want := []string{"rel-a", "rel-b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("deferred payload identities: want %v, got %v", want, got)
	}
}

// Two distinct failed relation mutations that share the same non-blank entity_key
// but describe different relations must also keep their own rows.
func TestRelationDeadLetter_DistinctPayloadsUnderSharedEntityKeyKeepSeparateRows(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	mutations := []SyncMutation{
		{
			Entity:    SyncEntityRelation,
			EntityKey: "rel-shared",
			Op:        SyncOpUpsert,
			Payload:   relationPayloadJSON(t, "rel-c", syncA, syncB),
			Source:    SyncSourceRemote,
			Project:   "proj-apply",
		},
		{
			Entity:    SyncEntityRelation,
			EntityKey: "rel-shared",
			Op:        SyncOpUpsert,
			Payload:   relationPayloadJSON(t, "rel-d", syncA, syncB),
			Source:    SyncSourceRemote,
			Project:   "proj-apply",
		},
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-shared-entity-key", mutations); err != nil {
		t.Fatalf("ApplyPulledChunk: %v", err)
	}

	got := relationDeferredPayloadIDs(t, s)
	want := []string{"rel-c", "rel-d"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("deferred payload identities: want %v, got %v", want, got)
	}
}

// A genuine redelivery of the same discarded mutation must collapse onto one
// row, so evidence does not accumulate one row per delivery.
func TestRelationDeadLetter_RedeliveryOfSameMutationStaysOneRow(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	mutation := blankEntityKeyRelationMutation(t, "rel-redelivered", syncA, syncB)
	for _, chunkID := range []string{"chunk-first-delivery", "chunk-second-delivery"} {
		if err := s.ApplyPulledChunk(DefaultSyncTargetKey, chunkID, []SyncMutation{mutation}); err != nil {
			t.Fatalf("ApplyPulledChunk %s: %v", chunkID, err)
		}
	}

	if got := countRelationDeferredRows(t, s); got != 1 {
		t.Fatalf("rows after redelivery: want 1, got %d", got)
	}
	if got := countRelationDeferredRowsForPayload(t, s, "rel-redelivered"); got != 1 {
		t.Fatalf("rows for rel-redelivered: want 1, got %d", got)
	}
}

// A relation that first fails and later applies successfully must have its
// evidence row cleaned up. The row was written under the blank entity_key, so a
// cleanup keyed on the payload sync_id alone could never reach it.
func TestRelationDeadLetter_SuccessfulApplyCleansUpBlankKeyedRow(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	failed := blankEntityKeyRelationMutation(t, "rel-orphan", syncA, syncB)
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-orphan-failure", []SyncMutation{failed}); err != nil {
		t.Fatalf("ApplyPulledChunk (failure): %v", err)
	}
	if got := countRelationDeferredRowsForPayload(t, s, "rel-orphan"); got != 1 {
		t.Fatalf("evidence row for rel-orphan: want 1, got %d", got)
	}

	applied := failed
	applied.EntityKey = "rel-orphan"
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-orphan-success", []SyncMutation{applied}); err != nil {
		t.Fatalf("ApplyPulledChunk (success): %v", err)
	}

	if got := countRelationRows(t, s, "rel-orphan"); got != 1 {
		t.Fatalf("memory_relations rows for rel-orphan: want 1, got %d", got)
	}
	if got := countRelationDeferredRows(t, s); got != 0 {
		t.Fatalf("orphaned deferred rows after successful apply: want 0, got %d", got)
	}
}

// The retry contract is unchanged for retryable failures: the row is keyed on the
// relation's own sync_id, replay reaches it, and the successful apply removes it.
func TestRelationDeferred_FKMissThenReplaySucceedsWithoutOrphan(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)

	missingTarget := "obs-missing-" + newSyncID("x")
	relSyncID := newSyncID("rel")
	mutation := SyncMutation{
		Entity:    SyncEntityRelation,
		EntityKey: relSyncID,
		Op:        SyncOpUpsert,
		Payload:   relationPayloadJSON(t, relSyncID, syncA, missingTarget),
		Source:    SyncSourceRemote,
		Project:   "proj-apply",
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-fk-miss", []SyncMutation{mutation}); err != nil {
		t.Fatalf("ApplyPulledChunk: %v", err)
	}

	status, _ := getDeferredRow(t, s, relSyncID)
	if status != "deferred" {
		t.Fatalf("apply_status: want deferred, got %q", status)
	}

	// The missing endpoint arrives; rewrite the payload the way a later delivery
	// would and let replay drive the retry.
	if err := s.CreateSession("ses-late", "proj-apply", "/tmp/late"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, arrived := addTestObsSession(t, s, "ses-late", "Late Obs", "decision", "proj-apply", "project")
	if _, err := s.db.Exec(
		`UPDATE sync_apply_deferred SET payload = ? WHERE sync_id = ?`,
		relationPayloadJSON(t, relSyncID, syncA, arrived), relSyncID,
	); err != nil {
		t.Fatalf("update deferred payload: %v", err)
	}

	res, err := s.ReplayDeferredForScope(DefaultSyncTargetKey, "proj-apply")
	if err != nil {
		t.Fatalf("ReplayDeferredForScope: %v", err)
	}
	if res.Succeeded != 1 {
		t.Fatalf("succeeded: want 1, got %d", res.Succeeded)
	}
	if got := countRelationDeferredRows(t, s); got != 0 {
		t.Fatalf("orphaned deferred rows after replay: want 0, got %d", got)
	}
	if got := countRelationRows(t, s, relSyncID); got != 1 {
		t.Fatalf("memory_relations rows: want 1, got %d", got)
	}
}

// ─── Success-path cleanup scope ───────────────────────────────────────────────

// evidenceMutationUnderEntityKey builds a relation mutation whose entity_key
// names one relation while its payload describes another. applyRelationUpsertTx
// rejects the disagreement as terminal evidence, and the resulting dead row
// stores that foreign entity_key — so it is the shape a cleanup keyed on
// entity_key would destroy.
func evidenceMutationUnderEntityKey(t *testing.T, entityKey, relSyncID, sourceID, targetID string) SyncMutation {
	t.Helper()
	return SyncMutation{
		Entity:    SyncEntityRelation,
		EntityKey: entityKey,
		Op:        SyncOpUpsert,
		Payload:   relationPayloadJSON(t, relSyncID, sourceID, targetID),
		Source:    SyncSourceRemote,
		Project:   "proj-apply",
	}
}

// A dead row keeps the discarded mutation's raw entity_key, and that key may name
// a different relation entirely. Applying that other relation successfully must
// not delete the evidence, which is the only record that the first mutation's
// data was dropped.
func TestRelationApplyCleanup_EvidenceForOtherMutationSurvivesUnrelatedApply(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	discarded := evidenceMutationUnderEntityKey(t, "rel-applied", "rel-discarded", syncA, syncB)
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-foreign-entity-key", []SyncMutation{discarded}); err != nil {
		t.Fatalf("ApplyPulledChunk (evidence): %v", err)
	}
	if got := countRelationDeferredRowsForPayload(t, s, "rel-discarded"); got != 1 {
		t.Fatalf("evidence row for rel-discarded: want 1, got %d", got)
	}

	// An unrelated but perfectly valid relation that happens to be named by that
	// entity_key now applies.
	applied := SyncMutation{
		Entity:    SyncEntityRelation,
		EntityKey: "rel-applied",
		Op:        SyncOpUpsert,
		Payload:   relationPayloadJSON(t, "rel-applied", syncA, syncB),
		Source:    SyncSourceRemote,
		Project:   "proj-apply",
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-unrelated-success", []SyncMutation{applied}); err != nil {
		t.Fatalf("ApplyPulledChunk (success): %v", err)
	}

	if got := countRelationRows(t, s, "rel-applied"); got != 1 {
		t.Fatalf("memory_relations rows for rel-applied: want 1, got %d", got)
	}
	got := relationDeferredPayloadIDs(t, s)
	want := []string{"rel-discarded"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("deferred payload identities: want %v, got %v", want, got)
	}
}

// Distinct discarded mutations may share one entity_key. A successful apply of
// the relation that key names must erase neither of them.
func TestRelationApplyCleanup_SharedEntityKeyEvidenceRowsBothSurvive(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	discarded := []SyncMutation{
		evidenceMutationUnderEntityKey(t, "rel-applied", "rel-dropped-a", syncA, syncB),
		evidenceMutationUnderEntityKey(t, "rel-applied", "rel-dropped-b", syncA, syncB),
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-shared-foreign-key", discarded); err != nil {
		t.Fatalf("ApplyPulledChunk (evidence): %v", err)
	}
	if got := countRelationDeferredRows(t, s); got != 2 {
		t.Fatalf("evidence rows before apply: want 2, got %d", got)
	}

	applied := SyncMutation{
		Entity:    SyncEntityRelation,
		EntityKey: "rel-applied",
		Op:        SyncOpUpsert,
		Payload:   relationPayloadJSON(t, "rel-applied", syncA, syncB),
		Source:    SyncSourceRemote,
		Project:   "proj-apply",
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-shared-key-success", []SyncMutation{applied}); err != nil {
		t.Fatalf("ApplyPulledChunk (success): %v", err)
	}

	got := relationDeferredPayloadIDs(t, s)
	want := []string{"rel-dropped-a", "rel-dropped-b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("deferred payload identities: want %v, got %v", want, got)
	}
}

// Narrowing the cleanup must not leave the retry row behind: a relation that was
// deferred for a missing endpoint and then applies has no pending state left.
func TestRelationApplyCleanup_RetryRowForAppliedRelationIsRemoved(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)

	missingTarget := "obs-missing-" + newSyncID("x")
	relSyncID := newSyncID("rel")
	deferred := SyncMutation{
		Entity:    SyncEntityRelation,
		EntityKey: relSyncID,
		Op:        SyncOpUpsert,
		Payload:   relationPayloadJSON(t, relSyncID, syncA, missingTarget),
		Source:    SyncSourceRemote,
		Project:   "proj-apply",
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-retry-deferral", []SyncMutation{deferred}); err != nil {
		t.Fatalf("ApplyPulledChunk (deferral): %v", err)
	}
	if status, _ := getDeferredRow(t, s, relSyncID); status != "deferred" {
		t.Fatalf("apply_status: want deferred, got %q", status)
	}

	// The endpoint arrives and the same relation is redelivered with both
	// endpoints present, so the apply succeeds.
	if err := s.CreateSession("ses-endpoint", "proj-apply", "/tmp/endpoint"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, arrived := addTestObsSession(t, s, "ses-endpoint", "Arrived Obs", "decision", "proj-apply", "project")
	applied := deferred
	applied.Payload = relationPayloadJSON(t, relSyncID, syncA, arrived)
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-retry-success", []SyncMutation{applied}); err != nil {
		t.Fatalf("ApplyPulledChunk (success): %v", err)
	}

	if got := countRelationRows(t, s, relSyncID); got != 1 {
		t.Fatalf("memory_relations rows: want 1, got %d", got)
	}
	if got := countRelationDeferredRows(t, s); got != 0 {
		t.Fatalf("orphaned deferred rows after successful apply: want 0, got %d", got)
	}
}

// The cleanup runs inside the apply write transaction on every successful
// relation upsert, so its cost must not grow with the dead-letter backlog. A
// full table scan there lengthens the write-lock hold for every pulled chunk.
func TestRelationApplyCleanup_PlanNeverScansDeferredTable(t *testing.T) {
	s, _, _ := setupSyncApplyStore(t)

	rows, err := s.db.Query(
		"EXPLAIN QUERY PLAN "+relationApplyCleanupSQL,
		SyncEntityRelation, "rel-planned", "rel-planned", "rel-planned",
	)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	var plan []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	if len(plan) == 0 {
		t.Fatal("EXPLAIN QUERY PLAN returned no rows")
	}

	for _, detail := range plan {
		if strings.Contains(detail, "SCAN sync_apply_deferred") {
			t.Fatalf("relation apply cleanup scans sync_apply_deferred: %v", plan)
		}
	}
}

// A row written by the old scheme is keyed on the discarded mutation's raw
// entity_key, so its key can name a relation its payload does not describe. The
// migration recovers the relation it is really about, and a successful apply of
// the relation that key names must not delete it either.
func TestRelationApplyCleanup_LegacyEvidenceKeyedOnOtherRelationSurvives(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	// The shape the migration leaves behind: keyed on the mutation's entity_key,
	// with the payload's own relation identity recovered into payload_sync_id.
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, project, scope_class, entity_key, op, payload_sync_id, apply_status, retry_count, first_seen_at)
		VALUES ('rel-applied', 'relation', ?, ?, 'proj-apply', 'scoped', '', '', 'rel-dropped', 'dead', 0, datetime('now'))
	`, relationPayloadJSON(t, "rel-dropped", syncA, syncB), DefaultSyncTargetKey); err != nil {
		t.Fatalf("insert legacy dead row: %v", err)
	}

	applied := SyncMutation{
		Entity:    SyncEntityRelation,
		EntityKey: "rel-applied",
		Op:        SyncOpUpsert,
		Payload:   relationPayloadJSON(t, "rel-applied", syncA, syncB),
		Source:    SyncSourceRemote,
		Project:   "proj-apply",
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-legacy-key-success", []SyncMutation{applied}); err != nil {
		t.Fatalf("ApplyPulledChunk: %v", err)
	}

	if got := countRelationRows(t, s, "rel-applied"); got != 1 {
		t.Fatalf("memory_relations rows for rel-applied: want 1, got %d", got)
	}
	got := relationDeferredPayloadIDs(t, s)
	want := []string{"rel-dropped"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("deferred payload identities: want %v, got %v", want, got)
	}
}

// A database written before payload_sync_id existed carries a row's relation
// identity only inside its payload. The migration derives it once, so the
// success-path cleanup still reaches such a row without parsing JSON.
func TestRelationApplyCleanup_MigrationBackfillsLegacyPayloadIdentity(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.CreateSession("ses-backfill", "proj-apply", "/tmp/backfill"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, syncA := addTestObsSession(t, s, "ses-backfill", "Obs A backfill", "decision", "proj-apply", "project")
	_, syncB := addTestObsSession(t, s, "ses-backfill", "Obs B backfill", "decision", "proj-apply", "project")
	payload := relationPayloadJSON(t, "rel-backfilled", syncA, syncB)

	// Return the table to the shape it had before the column existed, then write
	// the row the old scheme would have written: keyed on a blank entity_key.
	if _, err := s.db.Exec(`DROP INDEX idx_sad_payload_sync`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE sync_apply_deferred DROP COLUMN payload_sync_id`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, project, scope_class, entity_key, op, apply_status, retry_count, first_seen_at)
		VALUES ('', 'relation', ?, ?, 'proj-apply', 'scoped', '', '', 'dead', 0, datetime('now'))
	`, payload, DefaultSyncTargetKey); err != nil {
		t.Fatalf("insert legacy dead row: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := New(cfg)
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	var backfilled string
	if err := reopened.db.QueryRow(
		`SELECT payload_sync_id FROM sync_apply_deferred WHERE entity = 'relation'`,
	).Scan(&backfilled); err != nil {
		t.Fatalf("read backfilled identity: %v", err)
	}
	if backfilled != "rel-backfilled" {
		t.Fatalf("payload_sync_id: want %q, got %q", "rel-backfilled", backfilled)
	}

	applied := SyncMutation{
		Entity:    SyncEntityRelation,
		EntityKey: "rel-backfilled",
		Op:        SyncOpUpsert,
		Payload:   payload,
		Source:    SyncSourceRemote,
		Project:   "proj-apply",
	}
	if err := reopened.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-backfill-success", []SyncMutation{applied}); err != nil {
		t.Fatalf("ApplyPulledChunk: %v", err)
	}
	if got := countRelationDeferredRows(t, reopened); got != 0 {
		t.Fatalf("backfilled legacy row after successful apply: want 0, got %d", got)
	}
}

// ─── Interrupted migrations ───────────────────────────────────────────────────

// newRelationBackfillStore opens a store on its own data directory, seeds the two
// observations every relation payload in these tests points at, and returns the
// config so the caller can close the store and reopen the same database file.
func newRelationBackfillStore(t *testing.T, sessionID string) (cfg Config, s *Store, syncA, syncB string) {
	t.Helper()
	cfg = mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.CreateSession(sessionID, "proj-apply", "/tmp/"+sessionID); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, syncA = addTestObsSession(t, s, sessionID, "Obs A "+sessionID, "decision", "proj-apply", "project")
	_, syncB = addTestObsSession(t, s, sessionID, "Obs B "+sessionID, "decision", "proj-apply", "project")
	return cfg, s, syncA, syncB
}

// insertLegacyRelationRowWithBlankIdentity writes the row shape an interrupted
// migration leaves behind: payload_sync_id exists as a column but the row still
// carries nothing in it, so the row's relation identity lives only in its payload.
func insertLegacyRelationRowWithBlankIdentity(t *testing.T, s *Store, key, payload string) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, project, scope_class, entity_key, op, payload_sync_id, apply_status, retry_count, first_seen_at)
		VALUES (?, 'relation', ?, ?, 'proj-apply', 'scoped', '', '', '', 'dead', 0, datetime('now'))
	`, key, payload, DefaultSyncTargetKey); err != nil {
		t.Fatalf("insert legacy row with blank identity: %v", err)
	}
}

// reopenRelationBackfillStore reopens the database the config points at and runs
// the migration again.
func reopenRelationBackfillStore(t *testing.T, cfg Config) *Store {
	t.Helper()
	reopened, err := New(cfg)
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	return reopened
}

// relationDeferredStoredIdentities returns the payload_sync_id column of every
// relation row, keyed by the row's sync_id.
func relationDeferredStoredIdentities(t *testing.T, s *Store) map[string]string {
	t.Helper()
	rows, err := s.db.Query(
		`SELECT sync_id, payload_sync_id FROM sync_apply_deferred WHERE entity = 'relation'`,
	)
	if err != nil {
		t.Fatalf("relationDeferredStoredIdentities: query: %v", err)
	}
	defer rows.Close()

	stored := map[string]string{}
	for rows.Next() {
		var key, identity string
		if err := rows.Scan(&key, &identity); err != nil {
			t.Fatalf("relationDeferredStoredIdentities: scan: %v", err)
		}
		stored[key] = identity
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("relationDeferredStoredIdentities: rows: %v", err)
	}
	return stored
}

// The ALTER that adds payload_sync_id and the UPDATE that fills it are separate
// auto-committed statements. A transient SQLITE_BUSY, an I/O error, or the process
// dying between them leaves a database where the column exists and every legacy
// row is still blank. Deriving the identity only in the open that added the column
// would skip that database forever, so the backfill must converge on any later
// open instead.
func TestRelationMigration_InterruptedBackfillConvergesOnReopen(t *testing.T) {
	cfg, s, syncA, syncB := newRelationBackfillStore(t, "ses-interrupted")
	insertLegacyRelationRowWithBlankIdentity(t, s, "rel-legacy-key", relationPayloadJSON(t, "rel-interrupted", syncA, syncB))
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := reopenRelationBackfillStore(t, cfg)
	t.Cleanup(func() { _ = reopened.Close() })

	stored := relationDeferredStoredIdentities(t, reopened)
	if got := stored["rel-legacy-key"]; got != "rel-interrupted" {
		t.Fatalf("payload_sync_id after reopen: want %q, got %q", "rel-interrupted", got)
	}
}

// The blank payload_sync_id an interrupted migration leaves behind is what makes
// the success-path cleanup dangerous: the row is reachable by the key it is stored
// under, and a blank identity claims the row carries none of its own. Once the
// backfill has converged, a legacy row whose key names one relation while its
// payload describes another must survive a successful apply of the relation its
// key names.
func TestRelationApplyCleanup_EvidenceSurvivesRecoveryFromInterruptedBackfill(t *testing.T) {
	cfg, s, syncA, syncB := newRelationBackfillStore(t, "ses-interrupted-evidence")
	insertLegacyRelationRowWithBlankIdentity(t, s, "rel-applied", relationPayloadJSON(t, "rel-dropped", syncA, syncB))
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := reopenRelationBackfillStore(t, cfg)
	t.Cleanup(func() { _ = reopened.Close() })

	applied := SyncMutation{
		Entity:    SyncEntityRelation,
		EntityKey: "rel-applied",
		Op:        SyncOpUpsert,
		Payload:   relationPayloadJSON(t, "rel-applied", syncA, syncB),
		Source:    SyncSourceRemote,
		Project:   "proj-apply",
	}
	if err := reopened.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-interrupted-evidence", []SyncMutation{applied}); err != nil {
		t.Fatalf("ApplyPulledChunk: %v", err)
	}

	if got := countRelationRows(t, reopened, "rel-applied"); got != 1 {
		t.Fatalf("memory_relations rows for rel-applied: want 1, got %d", got)
	}
	got := relationDeferredPayloadIDs(t, reopened)
	want := []string{"rel-dropped"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("deferred payload identities: want %v, got %v", want, got)
	}
}

// An unconditional backfill runs on every open, so repeating it must be a no-op.
// A row whose payload carries no identity of its own has a genuinely blank
// payload_sync_id and must stay that way, and no open may duplicate, drop, or
// rewrite a row that was already derived.
func TestRelationMigration_BackfillIsIdempotentAcrossRepeatedOpens(t *testing.T) {
	cfg, s, syncA, syncB := newRelationBackfillStore(t, "ses-idempotent")
	insertLegacyRelationRowWithBlankIdentity(t, s, "rel-derivable", relationPayloadJSON(t, "rel-derived", syncA, syncB))
	// A payload that cannot be decoded has no identity to derive; that missing
	// identity is itself one of the reasons its row is dead.
	insertLegacyRelationRowWithBlankIdentity(t, s, "rel-undecodable", "{not json")
	// A well-formed payload can still omit sync_id entirely.
	insertLegacyRelationRowWithBlankIdentity(t, s, "rel-no-identity", `{"source_id":"a","target_id":"b"}`)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	want := map[string]string{
		"rel-derivable":   "rel-derived",
		"rel-undecodable": "",
		"rel-no-identity": "",
	}
	for open := 1; open <= 3; open++ {
		reopened := reopenRelationBackfillStore(t, cfg)
		stored := relationDeferredStoredIdentities(t, reopened)
		if len(stored) != len(want) {
			t.Fatalf("open %d: relation rows: want %d, got %d (%v)", open, len(want), len(stored), stored)
		}
		for key, identity := range want {
			if got, ok := stored[key]; !ok || got != identity {
				t.Fatalf("open %d: payload_sync_id for %q: want %q, got %q (present=%v)", open, key, identity, got, ok)
			}
		}
		if err := reopened.Close(); err != nil {
			t.Fatalf("open %d: Close: %v", open, err)
		}
	}
}

// ─── Rows written by the previous identity scheme ─────────────────────────────

// Legacy rows carry no entity_key or op, because the old writer only stored the
// key in sync_id. Replay must still reconstruct a valid mutation from them.
func TestReplayDeferred_LegacyRowWithoutEntityKeyStillApplies(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	relSyncID := newSyncID("rel-legacy")
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, project, scope_class, entity_key, op, apply_status, retry_count, first_seen_at)
		VALUES (?, 'relation', ?, ?, 'proj-apply', 'scoped', '', '', 'deferred', 0, datetime('now'))
	`, relSyncID, relationPayloadJSON(t, relSyncID, syncA, syncB), DefaultSyncTargetKey); err != nil {
		t.Fatalf("insert legacy deferred row: %v", err)
	}

	res, err := s.ReplayDeferredForScope(DefaultSyncTargetKey, "proj-apply")
	if err != nil {
		t.Fatalf("ReplayDeferredForScope: %v", err)
	}
	if res.Succeeded != 1 {
		t.Fatalf("succeeded: want 1, got %d", res.Succeeded)
	}
	if got := countRelationDeferredRows(t, s); got != 0 {
		t.Fatalf("legacy row still present after successful replay: got %d", got)
	}
	if got := countRelationRows(t, s, relSyncID); got != 1 {
		t.Fatalf("memory_relations rows: want 1, got %d", got)
	}
}

// A legacy dead row is rekeyed rather than duplicated when the very same
// mutation is redelivered.
func TestRelationDeadLetter_RedeliveryRetiresLegacyRowForSameMutation(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	mutation := blankEntityKeyRelationMutation(t, "rel-legacy-dead", syncA, syncB)
	// The old writer keyed the row on the blank entity_key and stored no entity_key.
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, project, scope_class, entity_key, op, apply_status, retry_count, first_seen_at)
		VALUES ('', 'relation', ?, ?, 'proj-apply', 'scoped', '', '', 'dead', 0, datetime('now'))
	`, mutation.Payload, DefaultSyncTargetKey); err != nil {
		t.Fatalf("insert legacy dead row: %v", err)
	}

	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-legacy-redelivery", []SyncMutation{mutation}); err != nil {
		t.Fatalf("ApplyPulledChunk: %v", err)
	}

	if got := countRelationDeferredRows(t, s); got != 1 {
		t.Fatalf("rows after legacy redelivery: want 1, got %d", got)
	}
	var syncID string
	if err := s.db.QueryRow(`SELECT sync_id FROM sync_apply_deferred WHERE entity = 'relation'`).Scan(&syncID); err != nil {
		t.Fatalf("read surviving row: %v", err)
	}
	if syncID == "" {
		t.Fatal("surviving row is still keyed on the blank entity_key")
	}
}

// A legacy row that holds a different mutation's payload is never retired by an
// unrelated redelivery: it is the only remaining evidence of that mutation.
func TestRelationDeadLetter_LegacyRowForOtherMutationIsPreserved(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	// The collapsed row left behind by the old scheme holds rel-b's payload.
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, project, scope_class, entity_key, op, apply_status, retry_count, first_seen_at)
		VALUES ('', 'relation', ?, ?, 'proj-apply', 'scoped', '', '', 'dead', 0, datetime('now'))
	`, relationPayloadJSON(t, "rel-b", syncA, syncB), DefaultSyncTargetKey); err != nil {
		t.Fatalf("insert legacy collapsed row: %v", err)
	}

	mutation := blankEntityKeyRelationMutation(t, "rel-a", syncA, syncB)
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-legacy-unrelated", []SyncMutation{mutation}); err != nil {
		t.Fatalf("ApplyPulledChunk: %v", err)
	}

	got := relationDeferredPayloadIDs(t, s)
	want := []string{"rel-a", "rel-b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("deferred payload identities: want %v, got %v", want, got)
	}
}

// Legacy rows stay reachable through the audit surface after the identity change.
func TestGetDeferred_LegacyRelationRowStaysReachable(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	relSyncID := newSyncID("rel-audit")
	insertDeferredRow(t, s, relSyncID, SyncEntityRelation, relationPayloadJSON(t, relSyncID, syncA, syncB), 0, "dead")

	row, err := s.GetDeferred(relSyncID)
	if err != nil {
		t.Fatalf("GetDeferred: %v", err)
	}
	if row.SyncID != relSyncID || row.ApplyStatus != "dead" {
		t.Fatalf("GetDeferred: got sync_id=%q status=%q", row.SyncID, row.ApplyStatus)
	}
}
