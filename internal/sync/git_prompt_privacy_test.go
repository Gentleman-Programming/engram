package sync

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func privacyChunk(t *testing.T, sy *Syncer, result *SyncResult) ChunkData {
	t.Helper()
	raw, err := sy.transport.ReadChunk(result.ChunkID)
	if err != nil {
		t.Fatal(err)
	}
	if chunkcodec.ChunkID(raw) != result.ChunkID {
		t.Fatal("hash does not describe exported payload")
	}
	var chunk ChunkData
	if err := json.Unmarshal(raw, &chunk); err != nil {
		t.Fatal(err)
	}
	if result.PromptsExported != len(chunk.Prompts) {
		t.Fatal("prompt result count mismatch")
	}
	manifest, err := sy.readManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Chunks {
		if entry.ID == result.ChunkID && (entry.Prompts != len(chunk.Prompts) || entry.Sessions != len(chunk.Sessions) || entry.Memories != len(chunk.Observations)) {
			t.Fatal("manifest counts mismatch")
		}
	}
	return chunk
}

func TestGitPromptPrivacyDefault(t *testing.T) {
	for _, project := range []string{"", "proj-a"} {
		t.Run("project="+project, func(t *testing.T) {
			s := newTestStore(t)
			seedStoreForSync(t, s)
			before, err := s.Export()
			if err != nil {
				t.Fatal(err)
			}
			sy := New(s, t.TempDir())
			result, err := sy.Export("privacy-test", project)
			if err != nil {
				t.Fatal(err)
			}
			chunk := privacyChunk(t, sy, result)
			raw, _ := json.Marshal(chunk)
			if result.PromptsExported != 0 || bytes.Contains(raw, []byte("prompt-a")) || bytes.Contains(raw, []byte("prompt-b")) {
				t.Fatalf("default Git export contains prompts: count=%d payload=%s", result.PromptsExported, raw)
			}
			for _, mutation := range chunk.Mutations {
				if mutation.Entity == store.SyncEntityPrompt && mutation.Op != store.SyncOpDelete {
					t.Fatal("content-bearing prompt mutation")
				}
			}
			after, err := s.Export()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Prompts, after.Prompts) || len(after.Prompts) != 2 {
				t.Fatal("local/standalone prompts changed")
			}
		})
	}
}

func TestGitPromptPrivacyLateOptIn(t *testing.T) {
	s := newTestStore(t)
	seedStoreForSync(t, s)
	sy := New(s, t.TempDir())
	if _, err := sy.Export("test", "proj-a"); err != nil {
		t.Fatal(err)
	}
	seedRelationForProject(t, s, "proj-a", "later-session", "privacy-relation")
	regression, err := sy.Export("test", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	ordinary := privacyChunk(t, sy, regression)
	if len(ordinary.Observations) != 2 || len(ordinary.Sessions) != 1 || len(ordinary.Mutations) != 1 || ordinary.Mutations[0].Entity != store.SyncEntityRelation {
		t.Fatalf("memory/session/relation regression: %+v", ordinary)
	}
	result, err := sy.ExportWithOptions("test", "proj-a", GitExportOptions{IncludePrompts: true})
	if err != nil {
		t.Fatal(err)
	}
	chunk := privacyChunk(t, sy, result)
	if len(chunk.Prompts) != 1 || len(chunk.Sessions) != 1 || chunk.Sessions[0].ID != chunk.Prompts[0].SessionID {
		t.Fatalf("omitted prompt closure missing: %+v", chunk)
	}
	again, err := sy.ExportWithOptions("test", "proj-a", GitExportOptions{IncludePrompts: true})
	if err != nil || !again.IsEmpty {
		t.Fatalf("repeated opt-in: %+v %v", again, err)
	}
	dst := newTestStore(t)
	if _, err := New(dst, sy.syncDir).Import(); err != nil {
		t.Fatal(err)
	}
	imported, err := dst.Export()
	if err != nil || len(imported.Prompts) != 1 {
		t.Fatalf("imported prompts: %+v %v", imported, err)
	}
	fresh := New(dst, t.TempDir())
	exported, err := fresh.Export("test", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(privacyChunk(t, fresh, exported).Prompts) != 0 {
		t.Fatal("imported prompt leaked")
	}
	if err := s.DeletePrompt(chunk.Prompts[0].ID); err != nil {
		t.Fatal(err)
	}
	deleted, err := sy.Export("test", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	tombstone := privacyChunk(t, sy, deleted)
	if len(tombstone.Prompts) != 0 || len(tombstone.Mutations) != 1 || tombstone.Mutations[0].Entity != store.SyncEntityPrompt || tombstone.Mutations[0].Op != store.SyncOpDelete {
		t.Fatalf("missing tombstone: %+v", tombstone)
	}
	if _, err := New(dst, sy.syncDir).Import(); err != nil {
		t.Fatal(err)
	}
	imported, err = dst.Export()
	if err != nil || len(imported.Prompts) != 0 {
		t.Fatal("deletion did not propagate")
	}
}

func TestGitPromptPrivacyHistorical(t *testing.T) {
	for _, shape := range []string{"legacy", "mutation", "delete"} {
		t.Run(shape, func(t *testing.T) {
			s := newTestStore(t)
			seedStoreForSync(t, s)
			data, err := s.ExportProject("proj-a")
			if err != nil {
				t.Fatal(err)
			}
			sy := New(s, t.TempDir())
			chunk := ChunkData{Sessions: data.Sessions, Prompts: data.Prompts}
			if shape != "legacy" {
				chunk.Mutations = synthesizeMutationsFromChunk(chunk)
				chunk.Prompts = nil
			}
			if shape == "delete" {
				chunk.Mutations = []store.SyncMutation{{Entity: store.SyncEntityPrompt, EntityKey: data.Prompts[0].SyncID, Op: store.SyncOpDelete, Payload: `{}`}}
			}
			raw, err := json.Marshal(chunk)
			if err != nil {
				t.Fatal(err)
			}
			id := chunkcodec.ChunkID(raw)
			entry := ChunkEntry{ID: id, CreatedAt: "2099-01-01T00:00:00Z"}
			if err := sy.transport.WriteChunk(id, raw, entry); err != nil {
				t.Fatal(err)
			}
			if err := sy.writeManifest(&Manifest{Version: 1, Chunks: []ChunkEntry{entry}}); err != nil {
				t.Fatal(err)
			}
			result, err := sy.ExportWithOptions("test", "proj-a", GitExportOptions{IncludePrompts: true})
			if err != nil {
				t.Fatal(err)
			}
			if result.PromptsExported != 0 {
				t.Fatal("historical identity duplicated or resurrected")
			}
			if shape != "delete" {
				dst := newTestStore(t)
				if _, err := New(dst, sy.syncDir).Import(); err != nil {
					t.Fatal(err)
				}
				restored, err := dst.Export()
				if err != nil || len(restored.Prompts) != 1 {
					t.Fatal("historical prompt import changed")
				}
			}
		})
	}
}

func TestGitPromptPrivacyCloud(t *testing.T) {
	s := newTestStore(t)
	seedStoreForSync(t, s)
	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatal(err)
	}
	transport := NewFileTransport(t.TempDir())
	sy := NewCloudWithTransport(s, transport, "proj-a")
	before, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sy.ExportWithOptions("test", "proj-a", GitExportOptions{IncludePrompts: true}); err == nil {
		t.Fatal("cloud accepted Git option")
	}
	manifest, err := transport.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Chunks) != 0 {
		t.Fatal("rejection wrote artifacts")
	}
	after, err := s.Export()
	if err != nil || !reflect.DeepEqual(before.Prompts, after.Prompts) {
		t.Fatal("rejection changed prompts")
	}
	result, err := sy.Export("test", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	if result.PromptsExported != 1 {
		t.Fatalf("cloud prompts changed: %+v", result)
	}
}
