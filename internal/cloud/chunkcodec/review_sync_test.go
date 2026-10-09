package chunkcodec

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCanonicalizePreservesReviewAfterPresence(t *testing.T) {
	for _, field := range []string{``, `,"review_after":null`, `,"review_after":"2020-07-01 00:00:00"`} {
		t.Run(field, func(t *testing.T) {
			payload := `{"sync_id":"review","session_id":"session","type":"decision","title":"decision","content":"content","scope":"project","revision_count":3,"duplicate_count":2` + field + `}`
			raw, err := json.Marshal(map[string]any{"mutations": []store.SyncMutation{{Entity: store.SyncEntityObservation, EntityKey: "review", Op: store.SyncOpUpsert, Payload: payload}}})
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := CanonicalizeForProject(raw, "demo")
			if err != nil {
				t.Fatal(err)
			}
			var chunk struct {
				Mutations []store.SyncMutation `json:"mutations"`
			}
			if err := json.Unmarshal(canonical, &chunk); err != nil {
				t.Fatal(err)
			}
			var before, after map[string]any
			if err := json.Unmarshal([]byte(payload), &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(chunk.Mutations[0].Payload), &after); err != nil {
				t.Fatal(err)
			}
			a, ap := before["review_after"]
			b, bp := after["review_after"]
			if ap != bp || !reflect.DeepEqual(a, b) {
				t.Fatalf("review date presence/value changed: before=%s after=%s", payload, chunk.Mutations[0].Payload)
			}
			if after["revision_count"] != float64(3) || after["duplicate_count"] != float64(2) || after["project"] != "demo" {
				t.Fatalf("metadata changed: %#v", after)
			}
		})
	}
}
