package chunkcodec

import (
	"encoding/json"
	"testing"
)

func TestCanonicalizeObservationProvenanceRejectsInvalidInput(t *testing.T) {
	for _, body := range []string{
		`{"sync_id":"source","superseded_by_sync_id":42}`,
		`{"superseded_by_sync_id":"survivor"}`,
	} {
		doc, _ := json.Marshal(map[string]any{"mutations": []map[string]any{{"entity": "observation", "op": "delete", "payload": body}}})
		before := string(doc)
		result, err := CanonicalizeForProject(doc, "project")
		if err == nil || result != nil {
			t.Fatalf("invalid mutation accepted: %s, %v", result, err)
		}
		if string(doc) != before {
			t.Fatal("rejection mutated input")
		}
	}
}
