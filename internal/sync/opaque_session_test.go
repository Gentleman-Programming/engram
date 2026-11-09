package sync_test

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
)

func TestLocalChunkRoundTripPreservesOpaqueSessions(t *testing.T) {
	newStore := func() *store.Store {
		s, err := store.New(store.FallbackConfig(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := s.Close(); err != nil {
				t.Errorf("close local store: %v", err)
			}
		})
		return s
	}
	source, destination := newStore(), newStore()
	for i, id := range []string{"session", " session ", "ordinary-control"} {
		if err := source.CreateSession(id, "opaque", "/work"); err != nil {
			t.Fatal(err)
		}
		if _, err := source.AddObservation(store.AddObservationParams{SessionID: id, Project: "opaque", Type: "note", Title: fmt.Sprintf("obs-%d", i), Content: fmt.Sprintf("unique content %d", i)}); err != nil {
			t.Fatal(err)
		}
		if _, err := source.AddPrompt(store.AddPromptParams{SessionID: id, Project: "opaque", Content: fmt.Sprintf("prompt-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	transport := engramsync.NewFileTransport(t.TempDir())
	exporter := engramsync.NewWithTransport(source, transport)
	if _, err := exporter.ExportWithOptions("fixture", "opaque", engramsync.GitExportOptions{IncludePrompts: true}); err != nil {
		t.Fatal(err)
	}
	importer := engramsync.NewWithTransport(destination, transport)
	for iteration := 0; iteration < 2; iteration++ {
		if _, err := importer.Import(); err != nil {
			t.Fatal(err)
		}
		data, err := destination.ExportProject("opaque")
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, s := range data.Sessions {
			ids = append(ids, s.ID)
		}
		sort.Strings(ids)
		if !reflect.DeepEqual(ids, []string{" session ", "ordinary-control", "session"}) {
			t.Errorf("iteration %d IDs = %q", iteration, ids)
		}
		obs, prompts := map[string]string{}, map[string]string{}
		for _, o := range data.Observations {
			obs[o.Title] = o.SessionID
		}
		for _, p := range data.Prompts {
			prompts[p.Content] = p.SessionID
		}
		if !reflect.DeepEqual(obs, map[string]string{"obs-0": "session", "obs-1": " session ", "obs-2": "ordinary-control"}) {
			t.Errorf("observation ownership = %#v", obs)
		}
		if !reflect.DeepEqual(prompts, map[string]string{"prompt-0": "session", "prompt-1": " session ", "prompt-2": "ordinary-control"}) {
			t.Errorf("prompt ownership = %#v", prompts)
		}
		deferred, dead, err := destination.CountDeferredAndDead()
		if err != nil {
			t.Fatal(err)
		}
		if deferred != 0 || dead != 0 {
			t.Errorf("deferred=%d dead=%d", deferred, dead)
		}
	}
}
