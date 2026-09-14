package cloudstore

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

const observationVersionID = "7b49d3c4-9b55-4d85-9e27-7b5762e0a638"

func observationVersionChunk(t *testing.T, project, versionID, title string, parent bool) []byte {
	t.Helper()
	parents := ""
	if parent {
		parents = `"sessions":[{"id":"session-1","directory":"/tmp/session-1"}],"observations":[{"sync_id":"obs-1","session_id":"session-1","type":"decision","title":"Current","content":"Current","scope":"project"}],`
	}
	payload := []byte(fmt.Sprintf(`{%s"observation_versions":[{"version_id":%q,"observation_sync_id":"obs-1","session_id":"session-1","type":"decision","title":%q,"content":"Body","project":%q,"scope":"project","revision_count":1,"history_complete":true,"captured_at":"2026-05-01T00:00:00Z"}]}`, parents, versionID, title, project))
	canonical, err := chunkcodec.CanonicalizeForProject(payload, project)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	return canonical
}

func observationParentChunk(t *testing.T, project string) []byte {
	t.Helper()
	payload, err := chunkcodec.CanonicalizeForProject([]byte(`{"sessions":[{"id":"session-1","directory":"/tmp/session-1"}],"observations":[{"sync_id":"obs-1","session_id":"session-1","type":"decision","title":"Current","content":"Current","scope":"project"}]}`), project)
	if err != nil { t.Fatalf("canonicalize parent: %v", err) }
	return payload
}

func TestWriteChunkMaterializesImmutableObservationVersions(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx, project := context.Background(), uniqueCloudstoreTestProject("observation-version")
	cleanupCloudstoreProject(t, cs, project)
	t.Cleanup(func() {
		_, _ = cs.db.ExecContext(ctx, `DELETE FROM cloud_observation_versions WHERE project = $1`, project)
		_, _ = cs.db.ExecContext(ctx, `DELETE FROM cloud_deleted_observations WHERE project = $1`, project)
	})

	sameChunk := observationVersionChunk(t, project, observationVersionID, "Version one", true)
	if err := cs.WriteChunk(ctx, project, chunkIDFromPayload(sameChunk), "tester", "", sameChunk); err == nil {
		t.Fatal("same-chunk parent evidence must be rejected")
	}
	parent := observationParentChunk(t, project)
	if err := cs.WriteChunk(ctx, project, chunkIDFromPayload(parent), "tester", "", parent); err != nil { t.Fatalf("write parent chunk: %v", err) }
	payload := observationVersionChunk(t, project, observationVersionID, "Version one", false)
	if err := cs.WriteChunk(ctx, project, chunkIDFromPayload(payload), "tester", "", payload); err != nil { t.Fatalf("write version chunk: %v", err) }
	var count int
	if err := cs.db.QueryRowContext(ctx, `SELECT count(*) FROM cloud_observation_versions WHERE version_id = $1 AND hidden_at IS NULL`, observationVersionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected visible version projection, count=%d err=%v", count, err)
	}
	if err := cs.WriteChunk(ctx, project, chunkIDFromPayload(payload), "tester", "", payload); err != nil {
		t.Fatalf("identical version replay: %v", err)
	}
	conflict := observationVersionChunk(t, project, observationVersionID, "Changed", false)
	if err := cs.WriteChunk(ctx, project, chunkIDFromPayload(conflict), "tester", "", conflict); err == nil {
		t.Fatal("expected immutable version conflict")
	}
	if err := cs.db.QueryRowContext(ctx, `SELECT count(*) FROM cloud_chunks WHERE project_name = $1`, project).Scan(&count); err != nil || count != 2 {
		t.Fatalf("conflict must roll back raw chunk, count=%d err=%v", count, err)
	}

	deletePayload, err := chunkcodec.CanonicalizeForProject([]byte(`{"mutations":[{"entity":"observation","entity_key":"obs-1","op":"delete","payload":"{\"sync_id\":\"obs-1\",\"session_id\":\"session-1\",\"deleted\":true}"}]}`), project)
	if err != nil { t.Fatalf("canonicalize delete: %v", err) }
	if err := cs.WriteChunk(ctx, project, chunkIDFromPayload(deletePayload), "tester", "", deletePayload); err != nil { t.Fatalf("write delete: %v", err) }
	if err := cs.db.QueryRowContext(ctx, `SELECT count(*) FROM cloud_observation_versions WHERE version_id = $1 AND hidden_at IS NOT NULL`, observationVersionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("delete must hide version, count=%d err=%v", count, err)
	}
	if err := cs.WriteChunk(ctx, project, chunkIDFromPayload(payload), "tester", "", payload); err != nil { t.Fatalf("replay remains accepted: %v", err) }
	if err := cs.db.QueryRowContext(ctx, `SELECT count(*) FROM cloud_observation_versions WHERE version_id = $1 AND hidden_at IS NOT NULL`, observationVersionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replayed version must remain hidden, count=%d err=%v", count, err)
	}
}

func TestVersionRequiresLatestValidObservationMutation(t *testing.T) {
	cs, ctx := openTestCloudStore(t), context.Background()
	for i, tt := range []struct{ name, op, payload string; accept bool }{
		{"valid", "", "", true}, {"delete", "delete", `{}`, false}, {"malformed", "upsert", `[]`, false},
		{"identity", "upsert", `{"sync_id":"other","session_id":"session-1","project":"p","scope":"project"}`, false},
		{"session", "upsert", `{"sync_id":"obs-1","session_id":"other","project":"p","scope":"project"}`, false},
		{"project", "upsert", `{"sync_id":"obs-1","session_id":"session-1","project":"other","scope":"project"}`, false},
		{"scope", "upsert", `{"sync_id":"obs-1","session_id":"session-1","project":"p","scope":"private"}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			project := fmt.Sprintf("%s-%d", uniqueCloudstoreTestProject("version-parent"), i)
			cleanupCloudstoreProject(t, cs, project)
			t.Cleanup(func() { _, _ = cs.db.ExecContext(ctx, `DELETE FROM cloud_observation_versions WHERE project = $1`, project) })
			parent := observationParentChunk(t, project)
			if err := cs.WriteChunk(ctx, project, chunkIDFromPayload(parent), "tester", "", parent); err != nil { t.Fatal(err) }
			if tt.op != "" { insertLegacyCloudMutation(t, cs, project, store.SyncEntityObservation, "obs-1", tt.op, strings.ReplaceAll(tt.payload, `"project":"p"`, fmt.Sprintf(`"project":%q`, project))) }
			payload := observationVersionChunk(t, project, fmt.Sprintf("%08x-9b55-4d85-9e27-7b5762e0a638", i+1), tt.name, false)
			if err := cs.WriteChunk(ctx, project, chunkIDFromPayload(payload), "tester", "", payload); (err == nil) != tt.accept { t.Fatalf("accept=%v err=%v", tt.accept, err) }
		})
	}
}

func TestBackfillObservationVersionsIsIdempotent(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx, project := context.Background(), uniqueCloudstoreTestProject("observation-version-migration")
	cleanupCloudstoreProject(t, cs, project)
	t.Cleanup(func() { _, _ = cs.db.ExecContext(ctx, `DELETE FROM cloud_observation_versions WHERE project = $1`, project) })
	payload := observationVersionChunk(t, project, "9b49d3c4-9b55-4d85-9e27-7b5762e0a638", "Migrated", false)
	second := observationVersionChunk(t, project, "ab49d3c4-9b55-4d85-9e27-7b5762e0a638", "Migrated again", false)
	for i, chunk := range [][]byte{observationParentChunk(t, project), payload, second} {
		if _, err := cs.db.ExecContext(ctx, `INSERT INTO cloud_chunks (project_name, chunk_id, created_by, payload, created_at) VALUES ($1, $2, 'legacy', $3, $4)`, project, chunkIDFromPayload(chunk), chunk, fmt.Sprintf("2026-05-01T00:00:0%dZ", i)); err != nil { t.Fatalf("insert legacy chunk: %v", err) }
	}
	if err := cs.backfillObservationVersions(ctx); err != nil { t.Fatalf("first backfill: %v", err) }
	if err := cs.backfillObservationVersions(ctx); err != nil { t.Fatalf("second backfill: %v", err) }
	var count int
	if err := cs.db.QueryRowContext(ctx, `SELECT count(*) FROM cloud_observation_versions WHERE project = $1`, project).Scan(&count); err != nil || count != 2 {
		t.Fatalf("expected two idempotent migrated versions, count=%d err=%v", count, err)
	}
}
