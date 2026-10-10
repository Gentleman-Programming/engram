package store

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// seedAgedDeferredRow inserts one sync_apply_deferred row whose first_seen_at
// is ageDays in the past, so retention tests seed ages instead of faking clocks.
func seedAgedDeferredRow(t *testing.T, s *Store, syncID, entity, targetKey, project, reasonCode, status string, ageDays int) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, project, reason_code, apply_status, first_seen_at)
		VALUES (?, ?, '{}', ?, ?, ?, ?, datetime('now', ?))
	`, syncID, entity, targetKey, project, reasonCode, status, fmt.Sprintf("-%d days", ageDays)); err != nil {
		t.Fatalf("seedAgedDeferredRow %q: %v", syncID, err)
	}
}

// deferredSyncIDs returns every sync_id in sync_apply_deferred, sorted.
func deferredSyncIDs(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT sync_id FROM sync_apply_deferred ORDER BY sync_id`)
	if err != nil {
		t.Fatalf("deferredSyncIDs: %v", err)
	}
	defer func() { _ = rows.Close() }()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("deferredSyncIDs: scan: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("deferredSyncIDs: rows: %v", err)
	}
	return ids
}

func pruneOptions(maxAgeDays, maxPerScope int, apply bool) PruneDeadRowsOptions {
	return PruneDeadRowsOptions{MaxAgeDays: maxAgeDays, MaxPerScope: maxPerScope, Apply: apply}
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestPruneDeadRowsDefaultsAreNamedNominations(t *testing.T) {
	if DefaultDeadRowMaxAgeDays != 30 {
		t.Fatalf("DefaultDeadRowMaxAgeDays = %d, want 30", DefaultDeadRowMaxAgeDays)
	}
	if DefaultDeadRowMaxPerScope != 1000 {
		t.Fatalf("DefaultDeadRowMaxPerScope = %d, want 1000", DefaultDeadRowMaxPerScope)
	}
}

// S1 + S8: the TTL compares first_seen_at against SQL-side now, so a seeded
// age is the whole fixture. A dry run reports and deletes nothing.
func TestPruneDeadRowsTTLRespectsSeededAges(t *testing.T) {
	s := newTestStore(t)
	seedAgedDeferredRow(t, s, "dead-old", SyncEntityRelation, "cloud", "proj-a", "r", "dead", 40)
	seedAgedDeferredRow(t, s, "dead-edge", SyncEntityRelation, "cloud", "proj-a", "r", "dead", 29)
	seedAgedDeferredRow(t, s, "dead-new", SyncEntityRelation, "cloud", "proj-a", "r", "dead", 0)
	before := deferredSyncIDs(t, s)

	dry, err := s.PruneDeadRows(pruneOptions(30, 1000, false))
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Applied || dry.Total != 1 {
		t.Fatalf("dry run = applied %v total %d, want applied false total 1", dry.Applied, dry.Total)
	}
	if got := deferredSyncIDs(t, s); !reflect.DeepEqual(got, before) {
		t.Fatalf("dry run changed rows: got %v, want %v", got, before)
	}

	applied, err := s.PruneDeadRows(pruneOptions(30, 1000, true))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !applied.Applied || applied.Total != 1 {
		t.Fatalf("apply = applied %v total %d, want applied true total 1", applied.Applied, applied.Total)
	}
	if got, want := deferredSyncIDs(t, s), []string{"dead-edge", "dead-new"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows after TTL prune = %v, want %v", got, want)
	}
}

// S2 + S9: a churning peer re-emits one logically identical failing mutation
// with a moving timestamp. Every delivery lands on a distinct digest row; the
// cap keeps the newest rows of that scope and never touches another scope.
func TestPruneDeadRowsCapHoldsUnderDistinctPayloadDeadRows(t *testing.T) {
	s := newTestStore(t)
	const deliveries = 15
	for i := 0; i < deliveries; i++ {
		mutation := SyncMutation{
			Entity:    SyncEntitySession,
			EntityKey: "",
			Op:        SyncOpUpsert,
			Payload:   fmt.Sprintf(`{"id":"","project":"proj-a","updated_at":"2026-10-10T00:00:%02dZ"}`, i),
		}
		if err := s.withTx(func(tx *sql.Tx) error {
			return s.deadLetterPulledIdentityTx(tx, "cloud", mutation, SyncSessionIdentityInvalidReasonCode)
		}); err != nil {
			t.Fatalf("dead-letter delivery %d: %v", i, err)
		}
		// Spread ages so "oldest first" is observable: delivery 0 is oldest.
		if _, err := s.db.Exec(`UPDATE sync_apply_deferred SET first_seen_at = datetime('now', ?) WHERE sync_id = ?`,
			fmt.Sprintf("-%d minutes", deliveries-i), pulledIdentityDeadLetterSyncID("cloud", mutation)); err != nil {
			t.Fatalf("age delivery %d: %v", i, err)
		}
	}
	for i := 0; i < 3; i++ {
		seedAgedDeferredRow(t, s, fmt.Sprintf("other-scope-%d", i), SyncEntityRelation, "cloud", "proj-b", "r", "dead", 0)
	}

	var distinct int
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_apply_deferred WHERE project = 'proj-a' AND apply_status = 'dead'`).Scan(&distinct); err != nil {
		t.Fatalf("count proj-a: %v", err)
	}
	if distinct != deliveries {
		t.Fatalf("distinct-payload dead rows = %d, want %d (fixture must model the growth path)", distinct, deliveries)
	}

	result, err := s.PruneDeadRows(pruneOptions(30, 10, true))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Total != 5 {
		t.Fatalf("evicted = %d, want 5", result.Total)
	}
	wantScopes := []PruneDeadRowsScope{{TargetKey: "cloud", Project: "proj-a", ByAge: 0, ByCap: 5}}
	if !reflect.DeepEqual(result.Scopes, wantScopes) {
		t.Fatalf("scopes = %+v, want %+v", result.Scopes, wantScopes)
	}

	// The five survivors that left proj-a are the five oldest deliveries.
	for i := 0; i < deliveries; i++ {
		mutation := SyncMutation{Entity: SyncEntitySession, Op: SyncOpUpsert,
			Payload: fmt.Sprintf(`{"id":"","project":"proj-a","updated_at":"2026-10-10T00:00:%02dZ"}`, i)}
		var n int
		if err := s.db.QueryRow(`SELECT count(*) FROM sync_apply_deferred WHERE sync_id = ?`, pulledIdentityDeadLetterSyncID("cloud", mutation)).Scan(&n); err != nil {
			t.Fatalf("lookup delivery %d: %v", i, err)
		}
		if want := map[bool]int{true: 0, false: 1}[i < 5]; n != want {
			t.Fatalf("delivery %d present = %d, want %d", i, n, want)
		}
	}
	var other int
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_apply_deferred WHERE project = 'proj-b'`).Scan(&other); err != nil {
		t.Fatalf("count proj-b: %v", err)
	}
	if other != 3 {
		t.Fatalf("other scope rows = %d, want 3 (one noisy peer must not evict another scope)", other)
	}
}

// S3 + S9: deferred rows are retry state, never retention material, however old
// or numerous they are. A re-armed row is deferred and survives too.
func TestPruneDeadRowsLeavesDeferredRowsUntouched(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 4; i++ {
		seedAgedDeferredRow(t, s, fmt.Sprintf("retry-%d", i), SyncEntityRelation, "cloud", "proj-a", "", "deferred", 400)
	}
	seedAgedDeferredRow(t, s, "applied-old", SyncEntityRelation, "cloud", "proj-a", "", "applied", 400)
	seedAgedDeferredRow(t, s, "dead-old", SyncEntityRelation, "cloud", "proj-a", "r", "dead", 400)

	result, err := s.PruneDeadRows(pruneOptions(1, 1, true))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("evicted = %d, want 1 (only the dead row)", result.Total)
	}
	want := []string{"applied-old", "retry-0", "retry-1", "retry-2", "retry-3"}
	if got := deferredSyncIDs(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows after prune = %v, want %v", got, want)
	}
	deferred, dead, err := s.CountDeferredAndDead()
	if err != nil {
		t.Fatalf("CountDeferredAndDead: %v", err)
	}
	if deferred != 4 || dead != 0 {
		t.Fatalf("counts = deferred %d dead %d, want 4 and 0", deferred, dead)
	}
}

// S6: the result names what it would evict per scope and by entity,
// reason_code, and age bucket, so eviction is never silent.
func TestPruneDeadRowsSummarizesByScopeEntityReasonAndAge(t *testing.T) {
	s := newTestStore(t)
	seedAgedDeferredRow(t, s, "a-old-1", SyncEntityRelation, "cloud", "proj-a", "relation_dead", "dead", 45)
	seedAgedDeferredRow(t, s, "a-old-2", SyncEntitySession, "cloud", "proj-a", SyncSessionIdentityInvalidReasonCode, "dead", 31)
	seedAgedDeferredRow(t, s, "a-new-1", SyncEntityRelation, "cloud", "proj-a", "relation_dead", "dead", 10)
	seedAgedDeferredRow(t, s, "a-new-2", SyncEntityRelation, "cloud", "proj-a", "relation_dead", "dead", 3)
	seedAgedDeferredRow(t, s, "a-new-3", SyncEntityRelation, "cloud", "proj-a", "relation_dead", "dead", 1)
	seedAgedDeferredRow(t, s, "legacy-old", SyncEntityRelation, "", "", "", "dead", 90)

	result, err := s.PruneDeadRows(pruneOptions(30, 2, false))
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if result.Applied || result.Total != 4 {
		t.Fatalf("dry run = applied %v total %d, want applied false total 4", result.Applied, result.Total)
	}
	wantScopes := []PruneDeadRowsScope{
		{TargetKey: "", Project: "", ByAge: 1, ByCap: 0},
		{TargetKey: "cloud", Project: "proj-a", ByAge: 2, ByCap: 1},
	}
	if !reflect.DeepEqual(result.Scopes, wantScopes) {
		t.Fatalf("scopes = %+v, want %+v", result.Scopes, wantScopes)
	}
	wantReasons := []PruneDeadRowsReason{
		{Entity: SyncEntityRelation, ReasonCode: "", AgeBucket: DeadRowAgeBucket30DaysPlus, Count: 1},
		{Entity: SyncEntityRelation, ReasonCode: "relation_dead", AgeBucket: DeadRowAgeBucket7To29Days, Count: 1},
		{Entity: SyncEntityRelation, ReasonCode: "relation_dead", AgeBucket: DeadRowAgeBucket30DaysPlus, Count: 1},
		{Entity: SyncEntitySession, ReasonCode: SyncSessionIdentityInvalidReasonCode, AgeBucket: DeadRowAgeBucket30DaysPlus, Count: 1},
	}
	if !reflect.DeepEqual(result.Reasons, wantReasons) {
		t.Fatalf("reasons = %+v, want %+v", result.Reasons, wantReasons)
	}
	if got := len(deferredSyncIDs(t, s)); got != 6 {
		t.Fatalf("dry run left %d rows, want 6", got)
	}
}

func TestPruneDeadRowsRejectsNonPositiveBoundsWithoutDeleting(t *testing.T) {
	s := newTestStore(t)
	seedAgedDeferredRow(t, s, "dead-old", SyncEntityRelation, "cloud", "proj-a", "r", "dead", 400)
	cases := []struct {
		name string
		opts PruneDeadRowsOptions
		want string
	}{
		{"zero age", pruneOptions(0, 10, true), "max age days must be positive"},
		{"negative age", pruneOptions(-1, 10, true), "max age days must be positive"},
		{"zero cap", pruneOptions(30, 0, true), "max per scope must be positive"},
		{"negative cap", pruneOptions(30, -5, true), "max per scope must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.PruneDeadRows(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if got := deferredSyncIDs(t, s); !reflect.DeepEqual(got, []string{"dead-old"}) {
				t.Fatalf("rows = %v, want untouched [dead-old]", got)
			}
		})
	}
}

// S9: the candidate query rides the existing indexes instead of a bare table scan.
func TestPruneDeadRowsCandidateQueryUsesIndex(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+pruneDeadRowCandidatesSQL, "-30 days", "-30 days", 1000)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("explain scan: %v", err)
		}
		details = append(details, detail)
	}
	searchesIndex := false
	for _, detail := range details {
		if detail == "SCAN sync_apply_deferred" {
			t.Fatalf("candidate query does a bare table scan; plan: %v", details)
		}
		if strings.HasPrefix(detail, "SEARCH sync_apply_deferred USING INDEX idx_sad_") {
			searchesIndex = true
		}
	}
	if !searchesIndex {
		t.Fatalf("candidate query does not search an existing sync_apply_deferred index; plan: %v", details)
	}
}
