package cloudstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud"
	"github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNewRequiresDSN(t *testing.T) {
	_, err := New(cloud.Config{})
	if err == nil {
		t.Fatal("expected error when DSN is empty")
	}
}

func TestSummarizeChunkCountsEntities(t *testing.T) {
	counts := summarizeChunk([]byte(`{"sessions":[{"id":"s1"}],"observations":[{"id":1},{"id":2}],"prompts":[{"id":3}]}`))
	if counts.sessions != 1 || counts.observations != 2 || counts.prompts != 1 {
		t.Fatalf("unexpected counts: %+v", counts)
	}

	empty := summarizeChunk([]byte(`{`))
	if empty.sessions != 0 || empty.observations != 0 || empty.prompts != 0 {
		t.Fatalf("invalid json must return zero counts, got %+v", empty)
	}
}

func TestChunkIDFromPayloadStable(t *testing.T) {
	payload := []byte(`{"sessions":[{"id":"s1"}],"observations":[],"prompts":[]}`)
	if got := chunkIDFromPayload(payload); got == "" || len(got) != 8 {
		t.Fatalf("expected 8-char chunk id, got %q", got)
	}
}

func TestMigrateAcceptsModernCloudChunksWithoutLegacyColumns(t *testing.T) {
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN so a per-test search_path can be attached")
	}

	schema := fmt.Sprintf("cloudstore_modern_%d", time.Now().UnixNano())
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	defer adminDB.Close()
	if _, err := adminDB.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })

	testDSN := dsn + "?search_path=" + schema
	if strings.Contains(dsn, "?") {
		testDSN = dsn + "&search_path=" + schema
	}
	db, err := sql.Open("pgx", testDSN)
	if err != nil {
		t.Fatalf("open schema db: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE cloud_chunks (
		project_name TEXT NOT NULL DEFAULT 'default',
		chunk_id TEXT NOT NULL,
		created_by TEXT NOT NULL,
		client_created_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		payload JSONB NOT NULL,
		sessions_count INTEGER NOT NULL DEFAULT 0,
		observations_count INTEGER NOT NULL DEFAULT 0,
		prompts_count INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		db.Close()
		t.Fatalf("create modern cloud_chunks: %v", err)
	}
	db.Close()

	cs, err := New(cloud.Config{DSN: testDSN})
	if err != nil {
		t.Fatalf("New should migrate modern cloud_chunks without imported_at/sessions/memories/prompts: %v", err)
	}
	cs.Close()
}

func TestMaterializedMutationBatchChunkIncludesObservationAlongsidePromptAndSession(t *testing.T) {
	obsPayload := json.RawMessage(`{
		"sync_id":"obs-04081be99000bdf5",
		"session_id":"sess-newer",
		"type":"decision",
		"title":"newer observation",
		"content":"must be present in cloud_chunks",
		"project":"sias-app",
		"scope":"project",
		"created_at":"2026-05-04T01:49:52Z"
	}`)
	promptPayload := json.RawMessage(`{"sync_id":"prompt-newer","session_id":"sess-newer","content":"newer prompt","project":"sias-app","created_at":"2026-05-04T01:50:00Z"}`)
	sessionPayload := json.RawMessage(`{"id":"sess-newer","project":"sias-app","directory":"/work/sias-app","started_at":"2026-05-04T01:45:00Z"}`)

	payload, counts, err := materializedMutationBatchChunk([]MutationEntry{
		{Project: "sias-app", Entity: store.SyncEntitySession, EntityKey: "sess-newer", Op: store.SyncOpUpsert, Payload: sessionPayload},
		{Project: "sias-app", Entity: store.SyncEntityPrompt, EntityKey: "prompt-newer", Op: store.SyncOpUpsert, Payload: promptPayload},
		{Project: "sias-app", Entity: store.SyncEntityObservation, EntityKey: "obs-04081be99000bdf5", Op: store.SyncOpUpsert, Payload: obsPayload},
	})
	if err != nil {
		t.Fatalf("materializedMutationBatchChunk: %v", err)
	}
	if counts.sessions != 1 || counts.prompts != 1 || counts.observations != 1 {
		t.Fatalf("expected one session, prompt, and observation in chunk counts, got %+v", counts)
	}

	var chunk engramsync.ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		t.Fatalf("decode materialized chunk: %v", err)
	}
	if len(chunk.Prompts) != 1 || len(chunk.Sessions) != 1 {
		t.Fatalf("expected prompt/session materialized, got sessions=%d prompts=%d", len(chunk.Sessions), len(chunk.Prompts))
	}
	if len(chunk.Observations) != 1 {
		t.Fatalf("expected newer observation mutation to be materialized into payload.observations, got %d", len(chunk.Observations))
	}
	if chunk.Observations[0].SyncID != "obs-04081be99000bdf5" {
		t.Fatalf("expected missing observation sync_id to be present, got %q", chunk.Observations[0].SyncID)
	}
	if len(chunk.Mutations) != 3 {
		t.Fatalf("expected mutation journal payloads retained for replay, got %d", len(chunk.Mutations))
	}
}

func TestMaterializedMutationBatchChunksKeepProjectsSeparate(t *testing.T) {
	chunks, err := materializedMutationBatchChunks([]MutationEntry{
		{Project: "proj-a", Entity: store.SyncEntityPrompt, EntityKey: "prompt-a", Op: store.SyncOpUpsert, Payload: json.RawMessage(`{"sync_id":"prompt-a","session_id":"sess-a","content":"a"}`)},
		{Project: "proj-b", Entity: store.SyncEntityObservation, EntityKey: "obs-b", Op: store.SyncOpUpsert, Payload: json.RawMessage(`{"sync_id":"obs-b","session_id":"sess-b","type":"decision","title":"b","content":"b","scope":"project"}`)},
	})
	if err != nil {
		t.Fatalf("materializedMutationBatchChunks: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected one materialized chunk per project, got %d", len(chunks))
	}
	if chunks[0].project != "proj-a" || chunks[1].project != "proj-b" {
		t.Fatalf("expected project order preserved, got %q then %q", chunks[0].project, chunks[1].project)
	}
}

func TestMutationEntrySignatureMatchesCanonicalChunkMutation(t *testing.T) {
	entry := MutationEntry{
		Project:   "proj-signature",
		Entity:    store.SyncEntityObservation,
		EntityKey: "obs-signature",
		Op:        store.SyncOpUpsert,
		Payload:   json.RawMessage(`{"sync_id":"obs-signature","session_id":"sess-signature","type":"decision","title":"Signature","content":"canonical","scope":"project"}`),
	}
	entrySig, err := mutationEntrySignature(entry)
	if err != nil {
		t.Fatalf("mutationEntrySignature: %v", err)
	}
	payload, _, err := materializedMutationBatchChunk([]MutationEntry{entry})
	if err != nil {
		t.Fatalf("materializedMutationBatchChunk: %v", err)
	}
	var chunk engramsync.ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		t.Fatalf("decode materialized chunk: %v", err)
	}
	if len(chunk.Mutations) != 1 {
		t.Fatalf("expected one materialized mutation, got %d", len(chunk.Mutations))
	}
	chunkSig, err := syncMutationSignature(chunk.Mutations[0])
	if err != nil {
		t.Fatalf("syncMutationSignature: %v", err)
	}
	if entrySig != chunkSig {
		t.Fatalf("expected entry signature to match canonical chunk mutation\nentry=%q\nchunk=%q", entrySig, chunkSig)
	}
}

func TestNormalizeJSONCanonicalizesEquivalentPayloads(t *testing.T) {
	a := []byte(`{"a":1,"b":[2,3]}`)
	b := []byte("{\n  \"b\": [2,3], \"a\":1\n}")
	if string(normalizeJSON(a)) != string(normalizeJSON(b)) {
		t.Fatalf("expected normalized payloads to match")
	}
}

func TestErrorSentinels(t *testing.T) {
	if !errors.Is(ErrChunkNotFound, ErrChunkNotFound) {
		t.Fatalf("expected ErrChunkNotFound to be comparable")
	}
	if !errors.Is(ErrChunkConflict, ErrChunkConflict) {
		t.Fatalf("expected ErrChunkConflict to be comparable")
	}
	if !errors.Is(ErrDashboardProjectInvalid, ErrDashboardProjectInvalid) {
		t.Fatalf("expected ErrDashboardProjectInvalid to be comparable")
	}
	if !errors.Is(ErrDashboardProjectForbidden, ErrDashboardProjectForbidden) {
		t.Fatalf("expected ErrDashboardProjectForbidden to be comparable")
	}
	if !errors.Is(ErrDashboardProjectNotFound, ErrDashboardProjectNotFound) {
		t.Fatalf("expected ErrDashboardProjectNotFound to be comparable")
	}
}

func TestDashboardScopedQueriesRejectInvalidOrOutOfScopeProjectBeforeDB(t *testing.T) {
	cs := &CloudStore{dashboardAllowedScopes: map[string]struct{}{"proj-a": {}}}

	tests := []struct {
		name string
		call func() error
		want error
	}{
		{
			name: "project detail rejects blank project",
			call: func() error {
				_, err := cs.ProjectDetail("   ")
				return err
			},
			want: ErrDashboardProjectInvalid,
		},
		{
			name: "project detail rejects out of scope project",
			call: func() error {
				_, err := cs.ProjectDetail("proj-b")
				return err
			},
			want: ErrDashboardProjectForbidden,
		},
		{
			name: "recent observations rejects out of scope project",
			call: func() error {
				_, err := cs.ListRecentObservations("proj-b", "", 10)
				return err
			},
			want: ErrDashboardProjectForbidden,
		},
		{
			name: "recent sessions rejects out of scope project",
			call: func() error {
				_, err := cs.ListRecentSessions("proj-b", "", 10)
				return err
			},
			want: ErrDashboardProjectForbidden,
		},
		{
			name: "recent prompts rejects out of scope project",
			call: func() error {
				_, err := cs.ListRecentPrompts("proj-b", "", 10)
				return err
			},
			want: ErrDashboardProjectForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected error %v, got %v", tt.want, err)
			}
		})
	}
}

func TestIsUniqueViolation(t *testing.T) {
	if !isUniqueViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("expected Postgres unique violation to be detected")
	}
	if isUniqueViolation(errors.New("boom")) {
		t.Fatal("expected non-pg error to return false")
	}
}

func TestCollectSessionIDsIncludesChunkSessionsAndMutationSessions(t *testing.T) {
	sessionIDs := collectSessionIDsFromPayload([]byte(`{
		"sessions":[{"id":"s-1"}],
		"mutations":[
			{"entity":"session","op":"upsert","payload":"{\"id\":\"s-2\",\"directory\":\"/tmp/s-2\"}"},
			{"entity":"session","op":"upsert","payload":"\"{\\\"id\\\":\\\"s-3\\\",\\\"directory\\\":\\\"/tmp/s-3\\\"}\""},
			{"entity":"observation","op":"upsert","payload":"{\"session_id\":\"s-1\"}"}
		]
	}`))

	if _, ok := sessionIDs["s-1"]; !ok {
		t.Fatalf("expected session id from chunk sessions")
	}
	if _, ok := sessionIDs["s-2"]; !ok {
		t.Fatalf("expected session id from mutation payload")
	}
	if _, ok := sessionIDs["s-3"]; !ok {
		t.Fatalf("expected session id from double-encoded mutation payload")
	}
}

func TestParseClientCreatedAt(t *testing.T) {
	t.Run("empty is allowed", func(t *testing.T) {
		got, err := parseClientCreatedAt("")
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil timestamp for empty input, got %v", got)
		}
	})

	t.Run("valid RFC3339", func(t *testing.T) {
		got, err := parseClientCreatedAt("2026-04-01T12:30:00Z")
		if err != nil {
			t.Fatalf("expected valid parse, got %v", err)
		}
		if got == nil || got.Format(time.RFC3339) != "2026-04-01T12:30:00Z" {
			t.Fatalf("unexpected timestamp parse result: %v", got)
		}
	})

	t.Run("invalid format returns error", func(t *testing.T) {
		if _, err := parseClientCreatedAt("not-a-time"); err == nil {
			t.Fatal("expected parse error for invalid timestamp")
		}
	})
}

func TestSortManifestRowsByServerCreatedAtForReplay(t *testing.T) {
	rows := []manifestRow{
		{
			chunkID:       "chunk-newer-client-time",
			createdBy:     "dev-a",
			manifestTime:  time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC),
			serverCreated: time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC),
		},
		{
			chunkID:       "chunk-older-client-time",
			createdBy:     "dev-b",
			manifestTime:  time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC),
			serverCreated: time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
		},
	}

	entries := toManifestEntries(rows)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	gotOrder := []string{entries[0].ID, entries[1].ID}
	wantOrder := []string{"chunk-older-client-time", "chunk-newer-client-time"}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Fatalf("expected server-created ordering %v, got %v", wantOrder, gotOrder)
	}

	if entries[0].CreatedAt != "2026-04-09T10:00:00Z" || entries[1].CreatedAt != "2026-04-08T10:00:00Z" {
		t.Fatalf("expected manifest created_at to preserve metadata timestamps, got %+v", entries)
	}
}

func TestSortManifestRowsBreaksServerTimeTiesByChunkID(t *testing.T) {
	serverTimestamp := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	rows := []manifestRow{
		{chunkID: "b", createdBy: "dev", manifestTime: serverTimestamp, serverCreated: serverTimestamp},
		{chunkID: "a", createdBy: "dev", manifestTime: serverTimestamp, serverCreated: serverTimestamp},
	}
	entries := toManifestEntries(rows)
	gotOrder := []string{entries[0].ID, entries[1].ID}
	wantOrder := []string{"a", "b"}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Fatalf("expected deterministic chunk-id tie-break ordering %v, got %v", wantOrder, gotOrder)
	}
}

func TestMaterializedChunkMutationsBuildsOrderedUpserts(t *testing.T) {
	project := "proj-materialize"
	chunk := parseMustChunk(t, []byte(`{
		"sessions":[{"id":"s-1","project":"proj-materialize","directory":"/tmp/s-1","started_at":"2026-04-29T10:00:00Z"}],
		"observations":[{"sync_id":"obs-1","session_id":"s-1","project":"proj-materialize","type":"decision","title":"Decision","content":"Content","scope":"project","created_at":"2026-04-29T10:01:00Z","updated_at":"2026-04-29T10:01:00Z"}],
		"prompts":[{"sync_id":"prompt-1","session_id":"s-1","project":"proj-materialize","content":"Prompt","created_at":"2026-04-29T10:02:00Z"}]
	}`))

	entries, err := materializedChunkMutations(project, chunk)
	if err != nil {
		t.Fatalf("materializedChunkMutations: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	want := []struct {
		entity string
		key    string
	}{
		{store.SyncEntitySession, "s-1"},
		{store.SyncEntityObservation, "obs-1"},
		{store.SyncEntityPrompt, "prompt-1"},
	}
	for i, entry := range entries {
		if entry.Project != project || entry.Entity != want[i].entity || entry.EntityKey != want[i].key || entry.Op != store.SyncOpUpsert {
			t.Fatalf("entry %d mismatch: %+v", i, entry)
		}
		var payload map[string]any
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatalf("entry %d payload is not object JSON: %v", i, err)
		}
		if payload["project"] != project {
			t.Fatalf("entry %d payload project mismatch: %v", i, payload["project"])
		}
	}
}

func TestMaterializedChunkMutationsRejectsMissingSyncIDs(t *testing.T) {
	tests := []struct {
		name  string
		chunk engramsync.ChunkData
		want  string
	}{
		{
			name:  "observation missing sync id",
			chunk: engramsync.ChunkData{Observations: []store.Observation{{SessionID: "s-1"}}},
			want:  "observations[0].sync_id is required",
		},
		{
			name:  "prompt missing sync id",
			chunk: engramsync.ChunkData{Prompts: []store.Prompt{{SessionID: "s-1"}}},
			want:  "prompts[0].sync_id is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := materializedChunkMutations("proj-a", tt.chunk)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q error, got %v", tt.want, err)
			}
		})
	}
}

func TestWriteChunkMaterializesMutationsAndIsReplayIdempotent(t *testing.T) {
	cs := openTestCloudStore(t)
	project := "test-chunk-materialize-" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "-")
	payload, err := chunkcodec.CanonicalizeForProject([]byte(`{
		"sessions":[{"id":"s-1","directory":"/tmp/s-1","started_at":"2026-04-29T10:00:00Z"}],
		"observations":[{"sync_id":"obs-1","session_id":"s-1","type":"decision","title":"Decision","content":"Content","scope":"project","created_at":"2026-04-29T10:01:00Z","updated_at":"2026-04-29T10:01:00Z"}],
		"prompts":[{"sync_id":"prompt-1","session_id":"s-1","content":"Prompt","created_at":"2026-04-29T10:02:00Z"}]
	}`), project)
	if err != nil {
		t.Fatalf("canonicalize chunk: %v", err)
	}
	chunkID := chunkIDFromPayload(payload)

	if err := cs.WriteChunk(context.Background(), project, chunkID, "tester", "2026-04-29T10:03:00Z", payload); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	stored, err := cs.ReadChunk(context.Background(), project, chunkID)
	if err != nil {
		t.Fatalf("ReadChunk: %v", err)
	}
	if string(normalizeJSON(stored)) != string(normalizeJSON(payload)) {
		t.Fatalf("stored chunk payload mismatch")
	}

	mutations, _, _, err := cs.ListMutationsSince(context.Background(), 0, 100, []string{project})
	if err != nil {
		t.Fatalf("ListMutationsSince: %v", err)
	}
	if len(mutations) != 3 {
		t.Fatalf("expected 3 materialized mutations, got %d: %+v", len(mutations), mutations)
	}
	if mutations[0].Entity != store.SyncEntitySession || mutations[1].Entity != store.SyncEntityObservation || mutations[2].Entity != store.SyncEntityPrompt {
		t.Fatalf("expected session/observation/prompt order, got %+v", mutations)
	}
	if mutations[1].EntityKey != "obs-1" || mutations[1].Op != store.SyncOpUpsert || mutations[1].Project != project {
		t.Fatalf("unexpected observation mutation: %+v", mutations[1])
	}

	if err := cs.WriteChunk(context.Background(), project, chunkID, "tester", "2026-04-29T10:03:00Z", payload); err != nil {
		t.Fatalf("replay WriteChunk: %v", err)
	}
	mutationsAfterReplay, _, _, err := cs.ListMutationsSince(context.Background(), 0, 100, []string{project})
	if err != nil {
		t.Fatalf("ListMutationsSince after replay: %v", err)
	}
	if len(mutationsAfterReplay) != 3 {
		t.Fatalf("expected replay not to duplicate mutations, got %d: %+v", len(mutationsAfterReplay), mutationsAfterReplay)
	}
}

func TestBackfillMutationChunksMaterializesExistingMutationRows(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()
	project := uniqueCloudstoreTestProject("mutation-backfill")
	cleanupCloudstoreProject(t, cs, project)

	insertLegacyCloudMutation(t, cs, project, store.SyncEntitySession, "sess-backfill", store.SyncOpUpsert, `{"id":"sess-backfill","directory":"/work/backfill","started_at":"2026-05-04T01:45:00Z"}`)
	insertLegacyCloudMutation(t, cs, project, store.SyncEntityObservation, "obs-backfill", store.SyncOpUpsert, `{"sync_id":"obs-backfill","session_id":"sess-backfill","type":"decision","title":"Backfilled observation","content":"must be reconstructed from chunks","scope":"project","created_at":"2026-05-04T01:49:52Z"}`)

	dryRun, err := cs.BackfillMutationChunks(ctx, project, false)
	if err != nil {
		t.Fatalf("BackfillMutationChunks dry-run: %v", err)
	}
	if dryRun.Applied || dryRun.CandidateMutations != 2 || dryRun.ChunksPlanned != 1 || dryRun.ChunksInserted != 0 {
		t.Fatalf("unexpected dry-run report: %+v", dryRun)
	}
	if got := countCloudChunksForProject(t, cs, project); got != 0 {
		t.Fatalf("dry-run must not insert chunks, got %d", got)
	}

	report, err := cs.BackfillMutationChunks(ctx, project, true)
	if err != nil {
		t.Fatalf("BackfillMutationChunks apply: %v", err)
	}
	if !report.Applied || report.CandidateMutations != 2 || report.ChunksPlanned != 1 || report.ChunksInserted != 1 {
		t.Fatalf("unexpected apply report: %+v", report)
	}

	chunks := readCloudChunksForProject(t, cs, project)
	if len(chunks) != 1 {
		t.Fatalf("expected one repair chunk, got %d", len(chunks))
	}
	var chunk engramsync.ChunkData
	if err := json.Unmarshal(chunks[0], &chunk); err != nil {
		t.Fatalf("decode repair chunk: %v", err)
	}
	if len(chunk.Sessions) != 1 || chunk.Sessions[0].ID != "sess-backfill" {
		t.Fatalf("expected session upsert in repair chunk, got %+v", chunk.Sessions)
	}
	if len(chunk.Observations) != 1 || chunk.Observations[0].SyncID != "obs-backfill" {
		t.Fatalf("expected observation upsert in payload.observations after repair, got %+v", chunk.Observations)
	}
	if len(chunk.Mutations) != 2 {
		t.Fatalf("expected original mutation replay entries retained, got %d", len(chunk.Mutations))
	}
}

func TestBackfillMutationChunksIsIdempotent(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()
	project := uniqueCloudstoreTestProject("mutation-backfill-idempotent")
	cleanupCloudstoreProject(t, cs, project)

	insertLegacyCloudMutation(t, cs, project, store.SyncEntityObservation, "obs-idempotent", store.SyncOpUpsert, `{"sync_id":"obs-idempotent","session_id":"sess-idempotent","type":"decision","title":"Idempotent observation","content":"materialize once","scope":"project","created_at":"2026-05-04T01:49:52Z"}`)

	first, err := cs.BackfillMutationChunks(ctx, project, true)
	if err != nil {
		t.Fatalf("first BackfillMutationChunks: %v", err)
	}
	if first.ChunksInserted != 1 {
		t.Fatalf("expected first repair to insert one chunk, got %+v", first)
	}
	second, err := cs.BackfillMutationChunks(ctx, project, true)
	if err != nil {
		t.Fatalf("second BackfillMutationChunks: %v", err)
	}
	if second.ChunksPlanned != 0 || second.ChunksInserted != 0 || second.AlreadyMaterialized != 1 {
		t.Fatalf("expected second repair to be noop, got %+v", second)
	}
	if got := countCloudChunksForProject(t, cs, project); got != 1 {
		t.Fatalf("expected no duplicate chunks after rerun, got %d", got)
	}
}

func TestBackfillMutationChunksSkipsInvalidLegacyMutationPayloads(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()
	project := uniqueCloudstoreTestProject("mutation-backfill-invalid")
	cleanupCloudstoreProject(t, cs, project)

	insertLegacyCloudMutation(t, cs, project, store.SyncEntitySession, "manual-save-engram", store.SyncOpUpsert, `{"id":"manual-save-engram"}`)
	insertLegacyCloudMutation(t, cs, project, store.SyncEntityObservation, "obs-valid", store.SyncOpUpsert, `{"sync_id":"obs-valid","session_id":"sess-valid","type":"decision","title":"Valid observation","content":"materialize this one","scope":"project","created_at":"2026-05-04T01:49:52Z"}`)

	report, err := cs.BackfillMutationChunks(ctx, project, true)
	if err != nil {
		t.Fatalf("BackfillMutationChunks must skip invalid legacy payloads instead of failing: %v", err)
	}
	if !report.Applied || report.CandidateMutations != 2 || report.InvalidMutations != 1 || report.ChunksPlanned != 1 || report.ChunksInserted != 1 {
		t.Fatalf("unexpected report for invalid legacy payload skip: %+v", report)
	}
	chunks := readCloudChunksForProject(t, cs, project)
	if len(chunks) != 1 {
		t.Fatalf("expected valid mutation to still materialize into one chunk, got %d", len(chunks))
	}
	var chunk engramsync.ChunkData
	if err := json.Unmarshal(chunks[0], &chunk); err != nil {
		t.Fatalf("decode repair chunk: %v", err)
	}
	if len(chunk.Sessions) != 0 {
		t.Fatalf("invalid legacy session without directory must not be materialized, got %+v", chunk.Sessions)
	}
	if len(chunk.Observations) != 1 || chunk.Observations[0].SyncID != "obs-valid" {
		t.Fatalf("expected valid observation to materialize, got %+v", chunk.Observations)
	}
}

func uniqueCloudstoreTestProject(prefix string) string {
	return prefix + "-" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "-")
}

func cleanupCloudstoreProject(t *testing.T, cs *CloudStore, project string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = cs.db.ExecContext(context.Background(), `DELETE FROM cloud_chunks WHERE project_name = $1`, project)
		_, _ = cs.db.ExecContext(context.Background(), `DELETE FROM cloud_mutations WHERE project = $1`, project)
		_, _ = cs.db.ExecContext(context.Background(), `DELETE FROM cloud_project_sessions WHERE project_name = $1`, project)
	})
}

func insertLegacyCloudMutation(t *testing.T, cs *CloudStore, project, entity, entityKey, op, payload string) {
	t.Helper()
	_, err := cs.db.ExecContext(context.Background(), `
		INSERT INTO cloud_mutations (project, entity, entity_key, op, payload)
		VALUES ($1, $2, $3, $4, $5)`, project, entity, entityKey, op, []byte(payload))
	if err != nil {
		t.Fatalf("insert legacy cloud mutation %s/%s: %v", entity, entityKey, err)
	}
}

func countCloudChunksForProject(t *testing.T, cs *CloudStore, project string) int {
	t.Helper()
	var count int
	if err := cs.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM cloud_chunks WHERE project_name = $1`, project).Scan(&count); err != nil {
		t.Fatalf("count cloud chunks: %v", err)
	}
	return count
}

func readCloudChunksForProject(t *testing.T, cs *CloudStore, project string) [][]byte {
	t.Helper()
	rows, err := cs.db.QueryContext(context.Background(), `SELECT payload FROM cloud_chunks WHERE project_name = $1 ORDER BY created_at ASC, chunk_id ASC`, project)
	if err != nil {
		t.Fatalf("query cloud chunks: %v", err)
	}
	defer rows.Close()
	var chunks [][]byte
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scan cloud chunk: %v", err)
		}
		chunks = append(chunks, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate cloud chunks: %v", err)
	}
	return chunks
}

func parseMustChunk(t *testing.T, payload []byte) engramsync.ChunkData {
	t.Helper()
	chunk, err := parseChunkData(payload)
	if err != nil {
		t.Fatalf("parse chunk data: %v", err)
	}
	return chunk
}

func TestBuildDashboardReadModelSupportsParityQueries(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			project:   "proj-a",
			createdBy: "alan@example.com",
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"s-1","project":"proj-a","started_at":"2026-04-21T08:00:00Z"}],
				"observations":[{"sync_id":"obs-1","session_id":"s-1","project":"proj-a","type":"decision","title":"Decision A","created_at":"2026-04-21T08:10:00Z"}],
				"prompts":[{"sync_id":"prompt-1","session_id":"s-1","project":"proj-a","content":"Prompt A","created_at":"2026-04-21T08:20:00Z"}]
			}`)),
		},
		{
			project:   "proj-a",
			createdBy: "sofia@example.com",
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"s-2","project":"proj-a","started_at":"2026-04-22T08:00:00Z"}],
				"observations":[{"sync_id":"obs-2","session_id":"s-2","project":"proj-a","type":"note","title":"Note B","created_at":"2026-04-22T08:10:00Z"}]
			}`)),
		},
		{
			project:   "proj-b",
			createdBy: "alan@example.com",
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"s-3","project":"proj-b","started_at":"2026-04-23T08:00:00Z"}],
				"observations":[{"sync_id":"obs-3","session_id":"s-3","project":"proj-b","type":"decision","title":"Decision C","created_at":"2026-04-23T08:10:00Z"}],
				"prompts":[{"sync_id":"prompt-3","session_id":"s-3","project":"proj-b","content":"Prompt C","created_at":"2026-04-23T08:20:00Z"}]
			}`)),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("build dashboard read model: %v", err)
	}

	if len(model.projects) != 2 {
		t.Fatalf("expected project metrics for two projects, got %d", len(model.projects))
	}
	if model.admin.Projects != 2 {
		t.Fatalf("expected admin project count=2, got %+v", model.admin)
	}

	projDetail, ok := model.projectDetails["proj-a"]
	if !ok {
		t.Fatalf("expected project detail for proj-a")
	}
	if projDetail.Stats.Sessions != 2 || projDetail.Stats.Observations != 2 {
		t.Fatalf("expected project detail metrics to be queryable, got %+v", projDetail.Stats)
	}

	filteredObservations := model.filterObservations("proj-a", "Decision")
	if len(filteredObservations) != 1 || filteredObservations[0].Title != "Decision A" {
		t.Fatalf("expected queryable browser observations filter, got %+v", filteredObservations)
	}

	contributors := model.listContributors("")
	if len(contributors) != 2 {
		t.Fatalf("expected contributor rows from chunk history backfill, got %d", len(contributors))
	}
}

func TestBuildDashboardReadModelReplaysMutationsInOrder(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			project:   "proj-replay",
			createdBy: "alan@example.com",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"mutations":[
					{"entity":"observation","entity_key":"obs-1","op":"delete","payload":"{\"sync_id\":\"obs-1\",\"session_id\":\"s-1\",\"deleted\":true,\"hard_delete\":true}"},
					{"entity":"prompt","entity_key":"prompt-2","op":"upsert","payload":"{\"sync_id\":\"prompt-2\",\"session_id\":\"s-1\",\"project\":\"proj-replay\",\"content\":\"Prompt persisted\",\"created_at\":\"2026-04-23T09:40:00Z\"}"}
				]
			}`)),
		},
		{
			project:   "proj-replay",
			createdBy: "alan@example.com",
			createdAt: time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"mutations":[
					{"entity":"session","entity_key":"s-1","op":"upsert","payload":"{\"id\":\"s-1\",\"project\":\"proj-replay\",\"started_at\":\"2026-04-23T09:30:00Z\"}"},
					{"entity":"observation","entity_key":"obs-1","op":"upsert","payload":"{\"sync_id\":\"obs-1\",\"session_id\":\"s-1\",\"project\":\"proj-replay\",\"type\":\"decision\",\"title\":\"Decision final\",\"created_at\":\"2026-04-23T09:31:00Z\"}"},
					{"entity":"prompt","entity_key":"prompt-1","op":"delete","payload":"{\"sync_id\":\"prompt-1\",\"session_id\":\"s-1\",\"deleted\":true,\"hard_delete\":true}"}
				]
			}`)),
		},
		{
			project:   "proj-replay",
			createdBy: "alan@example.com",
			createdAt: time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"s-1","project":"proj-replay","started_at":"2026-04-23T09:00:00Z"}],
				"observations":[{"sync_id":"obs-1","session_id":"s-1","project":"proj-replay","type":"decision","title":"Decision draft","created_at":"2026-04-23T09:10:00Z"}],
				"prompts":[{"sync_id":"prompt-1","session_id":"s-1","project":"proj-replay","content":"Prompt removed","created_at":"2026-04-23T09:20:00Z"}]
			}`)),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("build dashboard read model: %v", err)
	}

	detail := model.projectDetails["proj-replay"]
	if detail.Stats.Chunks != 3 {
		t.Fatalf("expected 3 chunks counted for project history, got %d", detail.Stats.Chunks)
	}
	if detail.Stats.Sessions != 1 {
		t.Fatalf("expected final replayed session count=1, got %d", detail.Stats.Sessions)
	}
	if detail.Stats.Observations != 0 {
		t.Fatalf("expected replayed observation delete to remove obs-1, got %d", detail.Stats.Observations)
	}
	if detail.Stats.Prompts != 1 {
		t.Fatalf("expected delete-only + upsert replay to keep one prompt, got %d", detail.Stats.Prompts)
	}
	if len(detail.Sessions) != 1 || detail.Sessions[0].StartedAt != "2026-04-23T09:30:00Z" {
		t.Fatalf("expected repeated session upsert to keep newest value, got %+v", detail.Sessions)
	}
	if len(detail.Observations) != 0 {
		t.Fatalf("expected no observations after delete-only chunk, got %+v", detail.Observations)
	}
	if len(detail.Prompts) != 1 || detail.Prompts[0].Content != "Prompt persisted" {
		t.Fatalf("expected prompt-2 to remain after replay, got %+v", detail.Prompts)
	}

	if got := model.filterObservations("proj-replay", ""); len(got) != 0 {
		t.Fatalf("expected browser observations to reflect replay deletes, got %+v", got)
	}
	if got := model.filterPrompts("proj-replay", "persisted"); len(got) != 1 || got[0].Content != "Prompt persisted" {
		t.Fatalf("expected browser prompts query to use replayed current state, got %+v", got)
	}
}

func TestBuildDashboardReadModelProjectDetailContributorChunksAreProjectScoped(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			project:   "proj-a",
			createdBy: "alan@example.com",
			createdAt: time.Date(2026, 4, 21, 8, 0, 0, 0, time.UTC),
			parsed:    engramsync.ChunkData{Mutations: []store.SyncMutation{{Entity: store.SyncEntitySession, EntityKey: "s-a1", Op: store.SyncOpUpsert, Payload: `{"id":"s-a1","project":"proj-a","started_at":"2026-04-21T08:00:00Z"}`}}},
		},
		{
			project:   "proj-a",
			createdBy: "alan@example.com",
			createdAt: time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
			parsed:    engramsync.ChunkData{Mutations: []store.SyncMutation{{Entity: store.SyncEntityObservation, EntityKey: "obs-a", Op: store.SyncOpUpsert, Payload: `{"sync_id":"obs-a","session_id":"s-a1","project":"proj-a","type":"decision","title":"A","created_at":"2026-04-21T09:00:00Z"}`}}},
		},
		{
			project:   "proj-b",
			createdBy: "alan@example.com",
			createdAt: time.Date(2026, 4, 22, 8, 0, 0, 0, time.UTC),
			parsed:    engramsync.ChunkData{Mutations: []store.SyncMutation{{Entity: store.SyncEntitySession, EntityKey: "s-b1", Op: store.SyncOpUpsert, Payload: `{"id":"s-b1","project":"proj-b","started_at":"2026-04-22T08:00:00Z"}`}}},
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("build dashboard read model: %v", err)
	}
	detail, ok := model.projectDetails["proj-a"]
	if !ok {
		t.Fatalf("expected detail for proj-a")
	}
	if len(detail.Contributors) != 1 {
		t.Fatalf("expected one contributor for proj-a, got %+v", detail.Contributors)
	}
	if detail.Contributors[0].Chunks != 2 {
		t.Fatalf("expected contributor chunk count scoped to proj-a=2, got %+v", detail.Contributors[0])
	}

	contributors := model.listContributors("alan")
	if len(contributors) != 1 || contributors[0].Chunks != 3 {
		t.Fatalf("expected global contributor list to keep global chunk count=3, got %+v", contributors)
	}
}

func TestBuildDashboardReadModelUsesStableChunkIDOrderForEqualTimestamps(t *testing.T) {
	timestamp := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	base := []dashboardChunkRow{
		{
			chunkID:   "b-chunk",
			project:   "proj-stable",
			createdBy: "alan@example.com",
			createdAt: timestamp,
			parsed:    engramsync.ChunkData{Mutations: []store.SyncMutation{{Entity: store.SyncEntityObservation, EntityKey: "obs-1", Op: store.SyncOpUpsert, Payload: `{"sync_id":"obs-1","session_id":"s-1","project":"proj-stable","type":"decision","title":"newer","created_at":"2026-04-23T10:00:00Z"}`}}},
		},
		{
			chunkID:   "a-chunk",
			project:   "proj-stable",
			createdBy: "alan@example.com",
			createdAt: timestamp,
			parsed:    engramsync.ChunkData{Mutations: []store.SyncMutation{{Entity: store.SyncEntityObservation, EntityKey: "obs-1", Op: store.SyncOpUpsert, Payload: `{"sync_id":"obs-1","session_id":"s-1","project":"proj-stable","type":"decision","title":"older","created_at":"2026-04-23T09:59:00Z"}`}}},
		},
	}

	model, err := buildDashboardReadModel(base)
	if err != nil {
		t.Fatalf("build dashboard read model: %v", err)
	}
	rows := model.filterObservations("proj-stable", "")
	if len(rows) != 1 || rows[0].Title != "newer" {
		t.Fatalf("expected deterministic replay winner from chunk-id ordering, got %+v", rows)
	}

	reversed := []dashboardChunkRow{base[1], base[0]}
	modelReversed, err := buildDashboardReadModel(reversed)
	if err != nil {
		t.Fatalf("build dashboard read model reversed: %v", err)
	}
	rowsReversed := modelReversed.filterObservations("proj-stable", "")
	if len(rowsReversed) != 1 || rowsReversed[0].Title != "newer" {
		t.Fatalf("expected stable replay regardless input order, got %+v", rowsReversed)
	}
}

func TestBuildDashboardReadModelRemovesSessionOnUpsertTombstone(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			project:   "proj-tombstone",
			createdBy: "alan@example.com",
			createdAt: time.Date(2026, 4, 25, 9, 0, 0, 0, time.UTC),
			parsed: engramsync.ChunkData{Mutations: []store.SyncMutation{{
				Entity:    store.SyncEntitySession,
				EntityKey: "s-tomb",
				Op:        store.SyncOpUpsert,
				Payload:   `{"id":"s-tomb","project":"proj-tombstone","started_at":"2026-04-25T09:00:00Z"}`,
			}}},
		},
		{
			project:   "proj-tombstone",
			createdBy: "alan@example.com",
			createdAt: time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC),
			parsed: engramsync.ChunkData{Mutations: []store.SyncMutation{{
				Entity:    store.SyncEntitySession,
				EntityKey: "s-tomb",
				Op:        store.SyncOpUpsert,
				Payload:   `{"id":"s-tomb","project":"proj-tombstone","deleted_at":"2026-04-25T10:00:00Z"}`,
			}}},
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("build dashboard read model: %v", err)
	}

	detail := model.projectDetails["proj-tombstone"]
	if len(detail.Sessions) != 0 {
		t.Fatalf("expected upsert tombstone to remove session from detail list, got %+v", detail.Sessions)
	}
	if detail.Stats.Sessions != 0 {
		t.Fatalf("expected upsert tombstone to remove session from stats, got %d", detail.Stats.Sessions)
	}
	if got := model.filterSessions("proj-tombstone", ""); len(got) != 0 {
		t.Fatalf("expected browser sessions to omit tombstoned session, got %+v", got)
	}
}

func TestBuildDashboardReadModelFailsOnMalformedMutationPayload(t *testing.T) {
	_, err := buildDashboardReadModel([]dashboardChunkRow{{
		chunkID:   "bad-chunk",
		project:   "proj-bad",
		createdBy: "alan@example.com",
		createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
		parsed:    engramsync.ChunkData{Mutations: []store.SyncMutation{{Entity: store.SyncEntityObservation, EntityKey: "obs-1", Op: store.SyncOpUpsert, Payload: `{"sync_id":`}}},
	}})
	if err == nil {
		t.Fatal("expected malformed mutation payload to fail read-model replay")
	}
	if !strings.Contains(err.Error(), "invalid dashboard mutation payload") {
		t.Fatalf("expected malformed payload error context, got %v", err)
	}
}

func TestDashboardReadModelScopedFiltersAllowlistAcrossSurfaces(t *testing.T) {
	model, err := buildDashboardReadModel([]dashboardChunkRow{
		{
			chunkID:   "a1",
			project:   "proj-a",
			createdBy: "alan@example.com",
			createdAt: time.Date(2026, 4, 21, 8, 0, 0, 0, time.UTC),
			parsed:    parseMustChunk(t, []byte(`{"sessions":[{"id":"s-a","project":"proj-a","started_at":"2026-04-21T08:00:00Z"}]}`)),
		},
		{
			chunkID:   "b1",
			project:   "proj-b",
			createdBy: "sofia@example.com",
			createdAt: time.Date(2026, 4, 22, 8, 0, 0, 0, time.UTC),
			parsed:    parseMustChunk(t, []byte(`{"sessions":[{"id":"s-b","project":"proj-b","started_at":"2026-04-22T08:00:00Z"}],"observations":[{"sync_id":"obs-b","session_id":"s-b","project":"proj-b","type":"decision","title":"B","created_at":"2026-04-22T08:10:00Z"}]}`)),
		},
	})
	if err != nil {
		t.Fatalf("build dashboard read model: %v", err)
	}

	scoped := model.scoped(map[string]struct{}{"proj-a": {}})
	if len(scoped.projects) != 1 || scoped.projects[0].Project != "proj-a" {
		t.Fatalf("expected projects surface scoped to allowlist, got %+v", scoped.projects)
	}
	if _, ok := scoped.projectDetails["proj-b"]; ok {
		t.Fatalf("expected project detail scope to remove proj-b, got %+v", scoped.projectDetails["proj-b"])
	}
	if rows := scoped.filterSessions("", ""); len(rows) != 1 || rows[0].Project != "proj-a" {
		t.Fatalf("expected browser sessions scoped to allowlist, got %+v", rows)
	}
	contributors := scoped.listContributors("")
	if len(contributors) != 1 || contributors[0].CreatedBy != "alan@example.com" {
		t.Fatalf("expected contributors scoped to allowlist projects, got %+v", contributors)
	}
	if scoped.admin.Projects != 1 || scoped.admin.Contributors != 1 || scoped.admin.Chunks != 1 {
		t.Fatalf("expected admin overview scoped to allowlist, got %+v", scoped.admin)
	}
}

func TestDashboardQuerySurfacesReuseCachedReadModelUntilInvalidated(t *testing.T) {
	loadCalls := 0
	cs := &CloudStore{}
	cs.dashboardReadModelLoad = func() (dashboardReadModel, error) {
		loadCalls++
		return dashboardReadModel{
			projects:     []DashboardProjectRow{{Project: "proj-a", Chunks: 1, Sessions: 1, Observations: 1, Prompts: 1}},
			contributors: []DashboardContributorRow{{CreatedBy: "alan@example.com", Chunks: 1, Projects: 1}},
			projectDetails: map[string]DashboardProjectDetail{
				"proj-a": {
					Project:      "proj-a",
					Stats:        DashboardProjectRow{Project: "proj-a", Chunks: 1, Sessions: 1, Observations: 1, Prompts: 1},
					Contributors: []DashboardContributorRow{{CreatedBy: "alan@example.com", Chunks: 1, Projects: 1}},
					Sessions:     []DashboardSessionRow{{Project: "proj-a", SessionID: "s-1", StartedAt: "2026-04-22T10:00:00Z"}},
					Observations: []DashboardObservationRow{{Project: "proj-a", SessionID: "s-1", Type: "decision", Title: "Decision A", CreatedAt: "2026-04-22T10:10:00Z"}},
					Prompts:      []DashboardPromptRow{{Project: "proj-a", SessionID: "s-1", Content: "Prompt A", CreatedAt: "2026-04-22T10:20:00Z"}},
				},
			},
			admin: DashboardAdminOverview{Projects: 1, Contributors: 1, Chunks: 1},
		}, nil
	}

	if rows, err := cs.ListProjects(""); err != nil || len(rows) != 1 {
		t.Fatalf("expected projects from cached model, rows=%+v err=%v", rows, err)
	}
	if rows, err := cs.ListContributors("alan"); err != nil || len(rows) != 1 {
		t.Fatalf("expected contributors from cached model, rows=%+v err=%v", rows, err)
	}
	if detail, err := cs.ProjectDetail("proj-a"); err != nil || detail.Project != "proj-a" {
		t.Fatalf("expected project detail from cached model, detail=%+v err=%v", detail, err)
	}
	if rows, err := cs.ListRecentSessions("proj-a", "", 10); err != nil || len(rows) != 1 {
		t.Fatalf("expected sessions from cached model, rows=%+v err=%v", rows, err)
	}
	if rows, err := cs.ListRecentObservations("proj-a", "", 10); err != nil || len(rows) != 1 {
		t.Fatalf("expected observations from cached model, rows=%+v err=%v", rows, err)
	}
	if rows, err := cs.ListRecentPrompts("proj-a", "", 10); err != nil || len(rows) != 1 {
		t.Fatalf("expected prompts from cached model, rows=%+v err=%v", rows, err)
	}
	if overview, err := cs.AdminOverview(); err != nil || overview.Projects != 1 {
		t.Fatalf("expected admin overview from cached model, overview=%+v err=%v", overview, err)
	}

	if loadCalls != 1 {
		t.Fatalf("expected one read-model load across all dashboard queries, got %d", loadCalls)
	}

	cs.invalidateDashboardReadModel()
	if _, err := cs.ListProjects(""); err != nil {
		t.Fatalf("expected projects query after explicit invalidation, got %v", err)
	}
	if loadCalls != 2 {
		t.Fatalf("expected cache invalidation to force one reload, got %d", loadCalls)
	}
}

func TestSetDashboardAllowedProjectsInvalidatesCachedReadModel(t *testing.T) {
	loadCalls := 0
	cs := &CloudStore{}
	cs.dashboardReadModelLoad = func() (dashboardReadModel, error) {
		loadCalls++
		return dashboardReadModel{}, nil
	}

	if _, err := cs.ListProjects(""); err != nil {
		t.Fatalf("expected initial list projects call to succeed, got %v", err)
	}
	if loadCalls != 1 {
		t.Fatalf("expected initial load count=1, got %d", loadCalls)
	}

	cs.SetDashboardAllowedProjects([]string{"proj-a"})
	if _, err := cs.ListProjects(""); err != nil {
		t.Fatalf("expected list projects after allowlist update to succeed, got %v", err)
	}
	if loadCalls != 2 {
		t.Fatalf("expected allowlist update to invalidate read-model cache, got load count %d", loadCalls)
	}
}
