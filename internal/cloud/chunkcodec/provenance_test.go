package chunkcodec

import (
	"encoding/json"
	"testing"
)

func TestCanonicalizeObservationProvenance(t *testing.T) {
	for _, op := range []string{"upsert", "delete"} {
		t.Run(op, func(t *testing.T) {
			body := `{"sync_id":"source","session_id":"session","type":"decision","title":"Title","content":"Body","scope":"project","superseded_by_sync_id":"survivor","deleted_at":"2026-01-01"}`
			doc, _ := json.Marshal(map[string]any{"observations": []json.RawMessage{json.RawMessage(body)}, "mutations": []map[string]any{{"entity": "observation", "entity_key": "source", "op": op, "payload": body}}})
			canonical, err := CanonicalizeForProject(doc, "project")
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Observations []map[string]any `json:"observations"`
				Mutations    []struct {
					Payload string `json:"payload"`
				} `json:"mutations"`
			}
			if err := json.Unmarshal(canonical, &result); err != nil {
				t.Fatal(err)
			}
			var mutation map[string]any
			if err := DecodeSyncMutationPayload(result.Mutations[0].Payload, &mutation); err != nil {
				t.Fatal(err)
			}
			for _, row := range []map[string]any{result.Observations[0], mutation} {
				if row["superseded_by_sync_id"] != "survivor" {
					t.Fatalf("provenance lost: %s", canonical)
				}
				if row["project"] != "project" {
					t.Fatalf("project not canonicalized: %v", row)
				}
			}
			again, err := CanonicalizeForProject(canonical, "project")
			if err != nil || string(again) != string(canonical) {
				t.Fatalf("non-idempotent canonicalization: %s, %v", again, err)
			}
		})
	}
}

func TestCanonicalizeLegacyObservationProvenance(t *testing.T) {
	for _, metadata := range []string{"", `,"superseded_by_sync_id":null`} {
		payload := `{"sync_id":"source"` + metadata + `}`
		doc, _ := json.Marshal(map[string]any{"mutations": []map[string]any{{"entity": "observation", "op": "delete", "payload": payload}}})
		canonical, err := CanonicalizeForProject(doc, "project")
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Mutations []struct {
				Payload string `json:"payload"`
			} `json:"mutations"`
		}
		if err := json.Unmarshal(canonical, &result); err != nil {
			t.Fatal(err)
		}
		var row map[string]any
		if err := DecodeSyncMutationPayload(result.Mutations[0].Payload, &row); err != nil {
			t.Fatal(err)
		}
		if value := row["superseded_by_sync_id"]; value != nil {
			t.Fatalf("legacy metadata invented: %v", row)
		}
	}
}
