package chunkcodec

import (
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCanonicalizeForProjectPreservesIsolatedSession(t *testing.T) {
	for _, directory := range []string{"", `,"directory":""`} {
		t.Run(directory, func(t *testing.T) {
			payload := `{"id":"runtime@target","project":"wrong","ownership_mode":"project_owned"` + directory + `}`
			raw, err := json.Marshal(map[string]any{"mutations": []store.SyncMutation{{
				Entity: store.SyncEntitySession, EntityKey: "runtime@target", Op: store.SyncOpUpsert, Payload: payload,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := CanonicalizeForProject(raw, "target")
			if err != nil {
				t.Fatal(err)
			}
			var chunk struct {
				Mutations []store.SyncMutation `json:"mutations"`
			}
			if err := json.Unmarshal(canonical, &chunk); err != nil {
				t.Fatal(err)
			}
			var session store.Session
			if err := DecodeSyncMutationPayload(chunk.Mutations[0].Payload, &session); err != nil {
				t.Fatal(err)
			}
			if session.Directory != "" || session.Project != "target" || session.OwnershipMode != store.SessionOwnershipProjectOwned {
				t.Fatalf("isolated session lost during canonicalization: %+v", session)
			}
		})
	}
}
