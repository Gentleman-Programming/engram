package chunkcodec_test

import (
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCanonicalizeOpaqueSessionIdentities(t *testing.T) {
	for _, id := range []string{"session", " session ", "ordinary-control"} {
		t.Run(id, func(t *testing.T) {
			for _, op := range []string{store.SyncOpUpsert, store.SyncOpDelete} {
				payloads := []struct {
					entity, key string
					body        map[string]any
				}{
					{store.SyncEntitySession, id, map[string]any{"id": id, "directory": "/work"}},
					{store.SyncEntityObservation, "obs", map[string]any{"sync_id": "obs", "session_id": id, "type": "note", "title": "title", "content": "content", "scope": "project"}},
					{store.SyncEntityPrompt, "prompt", map[string]any{"sync_id": "prompt", "session_id": id, "content": "prompt"}},
				}
				var mutations []store.SyncMutation
				for _, p := range payloads {
					b, err := json.Marshal(p.body)
					if err != nil {
						t.Fatal(err)
					}
					mutations = append(mutations, store.SyncMutation{Entity: p.entity, EntityKey: p.key, Op: op, Payload: string(b)})
				}
				raw, err := json.Marshal(map[string]any{"mutations": mutations})
				if err != nil {
					t.Fatal(err)
				}
				canonical, err := chunkcodec.CanonicalizeForProject(raw, "project")
				if err != nil {
					t.Fatal(err)
				}
				var got struct {
					Mutations []store.SyncMutation `json:"mutations"`
				}
				if err := json.Unmarshal(canonical, &got); err != nil {
					t.Fatal(err)
				}
				if len(got.Mutations) != 3 {
					t.Fatalf("mutations = %d", len(got.Mutations))
				}
				for i, m := range got.Mutations {
					var body map[string]any
					if err := json.Unmarshal([]byte(m.Payload), &body); err != nil {
						t.Fatal(err)
					}
					field := "session_id"
					if i == 0 {
						field = "id"
						if m.EntityKey != id {
							t.Errorf("%s key = %q, want %q", op, m.EntityKey, id)
						}
					}
					if body[field] != id || body["project"] != "project" {
						t.Errorf("%s %s = %#v", op, m.Entity, body)
					}
				}
			}
		})
	}
}

func TestCanonicalizeRejectsAmbiguousOrBlankSessionIdentity(t *testing.T) {
	for _, tc := range []struct{ name, id, key string }{
		{"historical trimmed key", " session ", "session"},
		{"opposite mismatch", "session", " session "},
		{"whitespace only", " \t\n ", " \t\n "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"id": tc.id, "directory": "/work"})
			raw, _ := json.Marshal(map[string]any{"mutations": []store.SyncMutation{{Entity: store.SyncEntitySession, EntityKey: tc.key, Op: store.SyncOpUpsert, Payload: string(body)}}})
			if b, err := chunkcodec.CanonicalizeForProject(raw, "project"); err == nil {
				t.Fatalf("accepted invalid identity: %s", b)
			}
		})
	}
}
