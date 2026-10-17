package cloudstore

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
)

type dashboardFoldFixtureChunk struct {
	chunkID   string
	project   string
	createdBy string
	createdAt time.Time
	payload   string
}

// dashboardFoldFixture covers created_at ties broken by byte-wise chunk_id order
// ("B-tie" sorts before "a-tie" only under byte/C ordering), a same chunk_id in
// two projects, chunk deletes, mutation upserts/deletes and mutation pruning.
func dashboardFoldFixture() ([]dashboardFoldFixtureChunk, []dashboardMutationRow) {
	t0 := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	t2 := t0.Add(2 * time.Minute)
	chunks := []dashboardFoldFixtureChunk{
		{chunkID: "c-later", project: "proj-eq", createdBy: "carol", createdAt: t2, payload: `{
			"observations":[
				{"sync_id":"obs-gone","session_id":"sess-eq","project":"proj-eq","type":"decision","title":"gone","deleted_at":"2026-05-01T10:02:00Z","created_at":"2026-05-01T10:02:00Z"},
				{"sync_id":"obs-pruned","session_id":"sess-eq","project":"proj-eq","type":"decision","title":"chunk only","created_at":"2026-05-01T10:02:00Z"}
			],
			"prompts":[{"sync_id":"prompt-deleted","session_id":"sess-eq","project":"proj-eq","content":"bye","created_at":"2026-05-01T10:02:00Z"}]
		}`},
		{chunkID: "a-tie", project: "proj-tie", createdBy: "alice", createdAt: t0, payload: `{
			"sessions":[{"id":"sess-tie","project":"proj-tie","started_at":"2026-05-01T10:00:00Z"}],
			"observations":[{"sync_id":"obs-tie","session_id":"sess-tie","project":"proj-tie","type":"decision","title":"from a-tie","created_at":"2026-05-01T10:00:00Z"}]
		}`},
		{chunkID: "a-tie", project: "proj-eq", createdBy: "alice", createdAt: t0, payload: `{
			"sessions":[{"id":"sess-eq","project":"proj-eq","started_at":"2026-05-01T10:00:00Z"}],
			"observations":[
				{"sync_id":"obs-gone","session_id":"sess-eq","project":"proj-eq","type":"decision","title":"soon deleted","created_at":"2026-05-01T10:00:00Z"},
				{"sync_id":"obs-kept","session_id":"sess-eq","project":"proj-eq","type":"decision","title":"chunk title","created_at":"2026-05-01T10:00:00Z"}
			]
		}`},
		{chunkID: "B-tie", project: "proj-tie", createdBy: "bob", createdAt: t0, payload: `{
			"sessions":[{"id":"sess-tie","project":"proj-tie","summary":"closed by B"}],
			"observations":[{"sync_id":"obs-tie","session_id":"sess-tie","project":"proj-tie","type":"decision","title":"from B-tie","created_at":"2026-05-01T10:00:00Z"}]
		}`},
		{chunkID: "m-mid", project: "proj-tie", createdBy: "Bob", createdAt: t1, payload: `{
			"prompts":[{"sync_id":"prompt-tie","session_id":"sess-tie","project":"proj-tie","content":"hello","created_at":"2026-05-01T10:01:00Z"}]
		}`},
	}
	mutations := []dashboardMutationRow{
		{seq: 12, project: "proj-eq", entity: "prompt", entityKey: "prompt-deleted", op: "delete", payload: []byte(`{"sync_id":"prompt-deleted","project":"proj-eq","deleted":true}`), occurredAt: t2},
		{seq: 10, project: "proj-eq", entity: "observation", entityKey: "obs-kept", op: "upsert", payload: []byte(`{"sync_id":"obs-kept","session_id":"sess-eq","project":"proj-eq","type":"decision","title":"first mutation"}`), occurredAt: t2},
		{seq: 11, project: "proj-eq", entity: "observation", entityKey: "obs-kept", op: "upsert", payload: []byte(`  {"sync_id":"obs-kept","project":"proj-eq","title":"second mutation"}  `), occurredAt: t1},
		{seq: 13, project: "proj-eq", entity: "session", entityKey: "sess-new", op: "upsert", payload: []byte(`{"id":"sess-new","project":"proj-eq","started_at":"2026-05-01T10:03:00Z"}`), occurredAt: t2},
	}
	return chunks, mutations
}

func dashboardFoldFixtureRows(t *testing.T, chunks []dashboardFoldFixtureChunk) []dashboardChunkRow {
	t.Helper()
	rows := make([]dashboardChunkRow, 0, len(chunks))
	for _, chunk := range chunks {
		rows = append(rows, dashboardChunkRow{
			chunkID:   chunk.chunkID,
			project:   chunk.project,
			createdBy: chunk.createdBy,
			createdAt: chunk.createdAt,
			parsed:    parseMustChunk(t, []byte(chunk.payload)),
		})
	}
	return rows
}

// sortDashboardChunksLikeSQL orders rows as dashboardChunkStreamQuery does:
// created_at, then COLLATE "C" (byte-wise) chunk_id, project_name, created_by.
func sortDashboardChunksLikeSQL(rows []dashboardChunkRow, compareText func(a, b string) int) []dashboardChunkRow {
	ordered := append([]dashboardChunkRow(nil), rows...)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if !a.createdAt.Equal(b.createdAt) {
			return a.createdAt.Before(b.createdAt)
		}
		if c := compareText(a.chunkID, b.chunkID); c != 0 {
			return c < 0
		}
		if c := compareText(a.project, b.project); c != 0 {
			return c < 0
		}
		return compareText(a.createdBy, b.createdBy) < 0
	})
	return ordered
}

func compareDashboardTextBytewise(a, b string) int { return bytes.Compare([]byte(a), []byte(b)) }

func foldDashboardRowsInOrder(t *testing.T, chunks []dashboardChunkRow, mutations []dashboardMutationRow) dashboardReadModel {
	t.Helper()
	fold := newDashboardReadModelFold()
	for _, chunk := range chunks {
		if err := fold.applyChunk(chunk); err != nil {
			t.Fatalf("applyChunk %q: %v", chunk.chunkID, err)
		}
	}
	for _, mutation := range mutations {
		if err := fold.applyMutation(mutation); err != nil {
			t.Fatalf("applyMutation seq %d: %v", mutation.seq, err)
		}
	}
	return fold.finish()
}

func sortDashboardMutationsBySeq(rows []dashboardMutationRow) []dashboardMutationRow {
	ordered := append([]dashboardMutationRow(nil), rows...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].seq < ordered[j].seq })
	return ordered
}

func TestDashboardReadModelStreamingFoldMatchesSortedRows(t *testing.T) {
	fixtureChunks, mutations := dashboardFoldFixture()
	rows := dashboardFoldFixtureRows(t, fixtureChunks)

	want, err := buildDashboardReadModelFromRows(rows, mutations)
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRows: %v", err)
	}
	got := foldDashboardRowsInOrder(t, sortDashboardChunksLikeSQL(rows, compareDashboardTextBytewise), sortDashboardMutationsBySeq(mutations))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streaming fold diverged from sorted rows\n got: %#v\nwant: %#v", got, want)
	}

	tie := want.projectDetails["proj-tie"]
	if len(tie.Observations) != 1 || tie.Observations[0].Title != "from a-tie" || tie.Observations[0].ChunkID != "a-tie" {
		t.Fatalf("byte-wise tie break must apply B-tie before a-tie, got %+v", tie.Observations)
	}
	if len(tie.Sessions) != 1 || tie.Sessions[0].Summary != "closed by B" || tie.Sessions[0].StartedAt != "2026-05-01T10:00:00Z" {
		t.Fatalf("tied session fold mismatch: %+v", tie.Sessions)
	}
	eq := want.projectDetails["proj-eq"]
	if len(eq.Observations) != 1 || eq.Observations[0].SyncID != "obs-kept" || eq.Observations[0].Title != "second mutation" || eq.Observations[0].ChunkID != "a-tie" {
		t.Fatalf("mutation fold/pruning mismatch: %+v", eq.Observations)
	}
	if len(eq.Prompts) != 0 {
		t.Fatalf("deleted prompt must stay deleted: %+v", eq.Prompts)
	}
	if len(eq.Sessions) != 2 {
		t.Fatalf("referenced chunk session and mutation session must remain: %+v", eq.Sessions)
	}
}

func TestDashboardReadModelStreamingFoldIsOrderSensitive(t *testing.T) {
	fixtureChunks, mutations := dashboardFoldFixture()
	rows := dashboardFoldFixtureRows(t, fixtureChunks)
	want, err := buildDashboardReadModelFromRows(rows, mutations)
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRows: %v", err)
	}
	// A locale-like, case-insensitive collation puts a-tie before B-tie; the
	// fixture must detect that so the SQL COLLATE "C" requirement is guarded.
	caseInsensitive := func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }
	got := foldDashboardRowsInOrder(t, sortDashboardChunksLikeSQL(rows, caseInsensitive), sortDashboardMutationsBySeq(mutations))
	if reflect.DeepEqual(got, want) {
		t.Fatal("fixture does not distinguish byte-wise from case-insensitive chunk ordering")
	}
}

func TestDashboardReadModelPruneUsesStreamedMutationKeys(t *testing.T) {
	fixtureChunks, _ := dashboardFoldFixture()
	rows := dashboardFoldFixtureRows(t, fixtureChunks)
	// Whitespace around keys is trimmed exactly as the old slice-based pruning did.
	mutations := []dashboardMutationRow{{seq: 1, project: " proj-eq ", entity: " observation ", entityKey: " obs-kept ", op: "upsert", payload: []byte(`{"sync_id":"obs-kept","project":"proj-eq","title":"kept"}`)}}
	model, err := buildDashboardReadModelFromRows(rows, mutations)
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRows: %v", err)
	}
	eq := model.projectDetails["proj-eq"]
	if len(eq.Observations) != 1 || eq.Observations[0].SyncID != "obs-kept" {
		t.Fatalf("pruning must keep only mutation-backed observations: %+v", eq.Observations)
	}
	if len(model.projectDetails["proj-tie"].Observations) != 1 {
		t.Fatalf("pruning must stay project scoped: %+v", model.projectDetails["proj-tie"].Observations)
	}
}

// openDashboardFoldPostgres opens an isolated schema on CLOUDSTORE_TEST_DSN.
func openDashboardFoldPostgres(t *testing.T) *CloudStore {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN so a per-test search_path can be attached")
	}
	schema := fmt.Sprintf("cloudstore_fold_%d", time.Now().UnixNano())
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	t.Cleanup(func() { adminDB.Close() })
	if _, err := adminDB.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	testDSN := dsn + "?search_path=" + schema
	if strings.Contains(dsn, "?") {
		testDSN = dsn + "&search_path=" + schema
	}
	cs, err := New(cloud.Config{DSN: testDSN})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestDashboardReadModelPostgresStreamingMatchesSortedRows(t *testing.T) {
	cs := openDashboardFoldPostgres(t)
	ctx := context.Background()
	fixtureChunks, mutations := dashboardFoldFixture()

	// Insert in fixture (non-canonical) order so physical order cannot mask SQL ordering.
	for _, chunk := range fixtureChunks {
		if _, err := cs.db.ExecContext(ctx, `INSERT INTO cloud_chunks (project_name, chunk_id, created_by, created_at, payload) VALUES ($1, $2, $3, $4, $5::jsonb)`,
			chunk.project, chunk.chunkID, chunk.createdBy, chunk.createdAt, chunk.payload); err != nil {
			t.Fatalf("insert chunk %q: %v", chunk.chunkID, err)
		}
	}
	for _, mutation := range mutations {
		if _, err := cs.db.ExecContext(ctx, `INSERT INTO cloud_mutations (seq, project, entity, entity_key, op, payload, occurred_at) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)`,
			mutation.seq, mutation.project, mutation.entity, mutation.entityKey, mutation.op, string(mutation.payload), mutation.occurredAt); err != nil {
			t.Fatalf("insert mutation %d: %v", mutation.seq, err)
		}
	}

	want, err := buildDashboardReadModelFromRows(dashboardFoldFixtureRows(t, fixtureChunks), mutations)
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRows: %v", err)
	}
	want = want.withRegisteredProjects(nil)
	got, err := cs.buildDashboardReadModel()
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PostgreSQL streaming build diverged from sorted rows\n got: %#v\nwant: %#v", got, want)
	}
}
