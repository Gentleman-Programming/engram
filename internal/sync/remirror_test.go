package sync

import (
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func TestCloudRemirrorExportsAckedStateWithStableEntityIdentity(t *testing.T) {
	source := newTestStore(t)
	transport := newFakeCloudTransport()
	syncer := NewCloudWithTransport(source, transport, "remirror-project")
	if err := source.CreateSession("remirror-session", "remirror-project", "/tmp/remirror"); err != nil {
		t.Fatal(err)
	}
	if err := source.EnrollProject("remirror-project"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.AddObservation(store.AddObservationParams{SessionID: "remirror-session", Type: "decision", Title: "replay", Content: "state", Project: "remirror-project", Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := syncer.Export("test", "remirror-project"); err != nil {
		t.Fatalf("initial export: %v", err)
	}

	if err := source.RemirrorProject("remirror-project"); err != nil {
		t.Fatal(err)
	}
	first, err := syncer.Export("test", "remirror-project")
	if err != nil || first.IsEmpty {
		t.Fatalf("first remirror export: result=%+v err=%v", first, err)
	}
	if err := source.RemirrorProject("remirror-project"); err != nil {
		t.Fatal(err)
	}
	second, err := syncer.Export("test", "remirror-project")
	if err != nil || second.IsEmpty {
		t.Fatalf("second remirror export: result=%+v err=%v", second, err)
	}

	var firstChunk, secondChunk ChunkData
	if err := json.Unmarshal(transport.chunks[first.ChunkID], &firstChunk); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(transport.chunks[second.ChunkID], &secondChunk); err != nil {
		t.Fatal(err)
	}
	if len(firstChunk.Mutations) != len(secondChunk.Mutations) || len(firstChunk.Mutations) == 0 {
		t.Fatalf("expected matching non-empty replay mutations: first=%d second=%d", len(firstChunk.Mutations), len(secondChunk.Mutations))
	}
	for i := range firstChunk.Mutations {
		if firstChunk.Mutations[i].Entity != secondChunk.Mutations[i].Entity || firstChunk.Mutations[i].EntityKey != secondChunk.Mutations[i].EntityKey || firstChunk.Mutations[i].Op != secondChunk.Mutations[i].Op {
			t.Fatalf("remirror changed remote identity: first=%+v second=%+v", firstChunk.Mutations[i], secondChunk.Mutations[i])
		}
	}
}
