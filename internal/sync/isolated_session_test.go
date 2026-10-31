package sync

import (
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCloudImportPreservesIsolatedSession(t *testing.T) {
	for _, directory := range []string{"", `,"directory":""`} {
		for _, mutation := range []bool{false, true} {
			t.Run(directory+map[bool]string{false: "/direct", true: "/mutation"}[mutation], func(t *testing.T) {
				dst := newTestStore(t)
				if err := dst.EnrollProject("target"); err != nil {
					t.Fatal(err)
				}
				transport := newFakeCloudTransport()
				const id = "isolated-chunk"
				transport.manifest = &Manifest{Version: ownershipModeManifestVersion, Chunks: []ChunkEntry{{ID: id, CreatedAt: "2026-01-01T00:00:00Z"}}}
				session := `{"id":"runtime@target","project":"target","ownership_mode":"project_owned"` + directory + `}`
				raw := []byte(`{"sessions":[` + session + `]}`)
				if mutation {
					var err error
					raw, err = json.Marshal(map[string]any{"mutations": []store.SyncMutation{{Entity: store.SyncEntitySession, EntityKey: "runtime@target", Op: store.SyncOpUpsert, Payload: session}}})
					if err != nil {
						t.Fatal(err)
					}
				}
				transport.chunks[id] = raw
				importer := NewCloudWithTransport(dst, transport, "target")
				for i := 0; i < 2; i++ {
					if _, err := importer.Import(); err != nil {
						t.Fatal(err)
					}
				}
				got, err := dst.GetSession("runtime@target")
				if err != nil || got.Directory != "" || got.Project != "target" || got.OwnershipMode != store.SessionOwnershipProjectOwned {
					t.Fatalf("pulled session = %+v, %v", got, err)
				}
				synced, err := dst.GetSyncedChunksForTarget("cloud:target")
				if err != nil || !synced[id] {
					t.Fatalf("chunk not recorded: %v, %v", synced, err)
				}
				if resumed, err := dst.RegisterIsolatedSession("runtime@target", "target", true); err != nil || resumed != "runtime@target" {
					t.Fatalf("pulled session cannot resume isolated: %q, %v", resumed, err)
				}
			})
		}
	}
}

func TestCloudExportImportIsolatedSessionWithObservation(t *testing.T) {
	src := newTestStore(t)
	dst := newTestStore(t)
	for _, s := range []*store.Store{src, dst} {
		if err := s.EnrollProject("target"); err != nil {
			t.Fatal(err)
		}
	}
	id, err := src.RegisterIsolatedSession("runtime@target", "target", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.AddObservation(store.AddObservationParams{
		SessionID: id, Project: "target", Scope: "project", Type: "discovery",
		Title: "satellite learning", Content: "learning shared with the enrolled project",
	}); err != nil {
		t.Fatal(err)
	}
	transport := newFakeCloudTransport()
	result, err := NewCloudWithTransport(src, transport, "target").Export("alice", "target")
	if err != nil || result.IsEmpty {
		t.Fatalf("export = %+v, %v", result, err)
	}
	var chunk ChunkData
	if err := json.Unmarshal(transport.chunks[result.ChunkID], &chunk); err != nil {
		t.Fatal(err)
	}
	parentIncluded := false
	for _, mutation := range chunk.Mutations {
		if mutation.Entity == store.SyncEntitySession && mutation.EntityKey == id {
			parentIncluded = true
		}
	}
	if !parentIncluded {
		t.Fatal("exported observation without its isolated parent session mutation")
	}
	importer := NewCloudWithTransport(dst, transport, "target")
	for i := 0; i < 2; i++ {
		if _, err := importer.Import(); err != nil {
			t.Fatal(err)
		}
	}
	session, err := dst.GetSession(id)
	if err != nil || session.Directory != "" || session.Project != "target" || session.OwnershipMode != store.SessionOwnershipProjectOwned {
		t.Fatalf("imported parent = %+v, %v", session, err)
	}
	observations, err := dst.AllObservations("target", "project", 10)
	if err != nil || len(observations) != 1 || observations[0].SessionID != id || observations[0].Content != "learning shared with the enrolled project" {
		t.Fatalf("imported observations = %+v, %v", observations, err)
	}
	deferred, err := dst.ListDeferred(store.ListDeferredOptions{})
	if err != nil || len(deferred) != 0 {
		t.Fatalf("deferred pulls = %+v, %v", deferred, err)
	}
}

func TestCloudImportRejectsInvalidIsolatedDirectoryAtomically(t *testing.T) {
	for _, directory := range []string{`null`, `123`} {
		dst := newTestStore(t)
		if err := dst.EnrollProject("target"); err != nil {
			t.Fatal(err)
		}
		transport := newFakeCloudTransport()
		const id = "invalid-isolated-chunk"
		transport.manifest = &Manifest{Version: ownershipModeManifestVersion, Chunks: []ChunkEntry{{ID: id, CreatedAt: "2026-01-01T00:00:00Z"}}}
		transport.chunks[id] = []byte(`{"sessions":[{"id":"runtime@target","project":"target","ownership_mode":"project_owned","directory":` + directory + `}]}`)
		if _, err := NewCloudWithTransport(dst, transport, "target").Import(); err == nil {
			t.Fatalf("invalid directory %s accepted", directory)
		}
		if _, err := dst.GetSession("runtime@target"); err == nil {
			t.Fatal("invalid session persisted")
		}
		synced, err := dst.GetSyncedChunksForTarget("cloud:target")
		if err != nil || synced[id] {
			t.Fatalf("invalid chunk recorded: %v, %v", synced, err)
		}
	}
}
