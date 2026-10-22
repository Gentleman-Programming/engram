package cloudserver_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
)

func TestOpaqueSessionDeleteMaterialization(t *testing.T) {
	for _, tc := range []struct {
		name, key, payload string
		reject             bool
	}{
		{"ordinary mismatch", "session", `{"id":" session "}`, true},
		{"padded mismatch", " session ", `{"id":"session"}`, true},
		{"ordinary", "session", `{"id":"session"}`, false},
		{"padded", " session ", `{"id":" session "}`, false},
		{"key only", " session ", `{}`, false},
		{"null legacy", " session ", `null`, false},
		{"blank legacy", " session ", `{"id":" \t "}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs, _ := opaqueCloud(t)
			ctx := context.Background()
			local := opaqueLocal(t)
			for i, id := range []string{"session", " session "} {
				if err := local.CreateSession(id, "opaque", "/work"); err != nil {
					t.Fatal(err)
				}
				if _, err := local.AddObservation(store.AddObservationParams{SessionID: id, Project: "opaque", Type: "note", Title: fmt.Sprintf("obs-%d", i), Content: fmt.Sprintf("evidence-%d", i), Scope: "project"}); err != nil {
					t.Fatal(err)
				}
				if _, err := local.AddPrompt(store.AddPromptParams{SessionID: id, Project: "opaque", Content: fmt.Sprintf("prompt-%d", i)}); err != nil {
					t.Fatal(err)
				}
			}
			data, err := local.ExportProject("opaque")
			if err != nil {
				t.Fatal(err)
			}
			write := func(c engramsync.ChunkData) error {
				payload := opaqueJSON(t, c)
				return cs.WriteChunk(ctx, "opaque", chunkcodec.ChunkID(payload), "fixture", "", payload)
			}
			if err := write(engramsync.ChunkData{Sessions: data.Sessions, Observations: data.Observations, Prompts: data.Prompts}); err != nil {
				t.Fatal(err)
			}
			snapshot := func() []byte {
				rows, _, seq, err := cs.ListMutationsSince(ctx, 0, 100, []string{"opaque"})
				if err != nil {
					t.Fatal(err)
				}
				manifest, err := cs.ReadManifest(ctx, "opaque")
				if err != nil {
					t.Fatal(err)
				}
				known, err := cs.KnownSessionIDs(ctx, "opaque")
				if err != nil {
					t.Fatal(err)
				}
				details := []any{}
				for _, id := range []string{"session", " session "} {
					s, o, p, err := cs.GetSessionDetail("opaque", id)
					if err != nil {
						t.Fatal(err)
					}
					details = append(details, s, o, p)
				}
				return opaqueJSON(t, []any{rows, seq, manifest, known, details})
			}
			before := snapshot()
			mutation := store.SyncMutation{Entity: store.SyncEntitySession, EntityKey: tc.key, Op: store.SyncOpDelete, Payload: tc.payload}
			chunk := engramsync.ChunkData{Sessions: []store.Session{{ID: "must-be-atomic", Project: "opaque", Directory: "/work"}}, Mutations: []store.SyncMutation{mutation}}
			err = write(chunk)
			if tc.reject {
				if err == nil {
					t.Fatal("WriteChunk accepted mismatched session DELETE")
				}
				if after := snapshot(); !reflect.DeepEqual(before, after) {
					t.Error("rejected chunk changed journal, sequence, manifest, index or dashboard details")
				}
				_, err = cs.InsertMutationBatch(ctx, []cloudstore.MutationEntry{
					{Project: "opaque", Entity: store.SyncEntitySession, EntityKey: "must-be-atomic", Op: store.SyncOpUpsert, Payload: opaqueJSON(t, store.Session{ID: "must-be-atomic", Project: "opaque", Directory: "/work"})},
					{Project: "opaque", Entity: mutation.Entity, EntityKey: mutation.EntityKey, Op: mutation.Op, Payload: []byte(mutation.Payload)},
				})
				if err == nil {
					t.Fatal("InsertMutationBatch accepted mismatched session DELETE")
				}
				if after := snapshot(); !reflect.DeepEqual(before, after) {
					t.Error("rejected batch changed public state")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				rows, _, _, err := cs.ListMutationsSince(ctx, 0, 100, []string{"opaque"})
				if err != nil {
					t.Fatal(err)
				}
				last := rows[len(rows)-1]
				if last.EntityKey != tc.key || last.Op != store.SyncOpDelete {
					t.Fatalf("delete identity changed: %+v", last)
				}
				other := "session"
				if tc.key == other {
					other = " session "
				}
				s, o, p, err := cs.GetSessionDetail("opaque", other)
				if err != nil || s.SessionID != other || len(o) != 1 || len(p) != 1 {
					t.Fatalf("sibling changed: %+v %v %v %v", s, o, p, err)
				}
			}
		})
	}
}
