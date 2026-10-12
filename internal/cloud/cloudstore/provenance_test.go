package cloudstore

import (
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/chunkcodec"
	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
)

func TestMaterializedObservationProvenance(t *testing.T) {
	for _, op := range []string{"upsert", "delete"} {
		t.Run(op, func(t *testing.T) {
			body := json.RawMessage(`{"sync_id":"source","session_id":"session","type":"decision","title":"Title","content":"Body","scope":"project","superseded_by_sync_id":"survivor"}`)
			payload, counts, err := materializedMutationBatchChunk([]MutationEntry{{Project: "project", Entity: "observation", EntityKey: "source", Op: op, Payload: body}})
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Observations []map[string]any `json:"observations"`
				Mutations    []struct {
					Payload string `json:"payload"`
				} `json:"mutations"`
			}
			if err := json.Unmarshal(payload, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Mutations) != 1 {
				t.Fatalf("mutations lost: %s", payload)
			}
			var mutation map[string]any
			if err := chunkcodec.DecodeSyncMutationPayload(result.Mutations[0].Payload, &mutation); err != nil {
				t.Fatal(err)
			}
			if mutation["superseded_by_sync_id"] != "survivor" {
				t.Fatalf("mutation provenance lost: %s", payload)
			}
			if op == "delete" {
				if len(result.Observations) != 0 || counts.observations != 0 {
					t.Fatalf("delete created placeholder: %s", payload)
				}
			} else {
				if len(result.Observations) != 1 || counts.observations != 1 {
					t.Fatalf("missing full row: %s", payload)
				}
				if result.Observations[0]["superseded_by_sync_id"] != "survivor" {
					t.Fatalf("full row provenance lost: %s", payload)
				}
				var chunk engramsync.ChunkData
				if err := json.Unmarshal(payload, &chunk); err != nil {
					t.Fatal(err)
				}
				entries, err := materializedChunkMutations("project", chunk)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 {
					t.Fatalf("unexpected replay entries: %v", entries)
				}
				var row map[string]any
				if err := json.Unmarshal(entries[0].Payload, &row); err != nil {
					t.Fatal(err)
				}
				if row["superseded_by_sync_id"] != "survivor" {
					t.Fatalf("replay lost provenance: %v", row)
				}
			}
		})
	}
}
