package cloudserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudserver"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
)

func TestOpaqueSessionRoundTrip(t *testing.T) {
	for _, mode := range []string{"typed", "mutations", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			cs, _ := opaqueCloud(t)
			h := cloudserver.New(cs, opaqueAuth{}, 0).Handler()
			source := opaqueLocal(t)
			if err := source.EnrollProject("opaque"); err != nil {
				t.Fatal(err)
			}
			ids := []string{"session", " session ", "ordinary-control"}
			for i, id := range ids {
				if err := source.CreateSession(id, "opaque", "/work"); err != nil {
					t.Fatal(err)
				}
				if _, err := source.AddObservation(store.AddObservationParams{SessionID: id, Project: "opaque", Type: "note", Title: fmt.Sprintf("obs-%d", i), Content: fmt.Sprintf("distinct evidence %d", i), Scope: "project"}); err != nil {
					t.Fatal(err)
				}
				if _, err := source.AddPrompt(store.AddPromptParams{SessionID: id, Project: "opaque", Content: fmt.Sprintf("prompt-%d", i)}); err != nil {
					t.Fatal(err)
				}
			}
			data, err := source.ExportProject("opaque")
			if err != nil {
				t.Fatal(err)
			}
			mutations, err := source.ListPendingProjectMutations("opaque")
			if err != nil {
				t.Fatal(err)
			}
			entries := make([]cloudstore.MutationEntry, 0, len(mutations))
			for _, m := range mutations {
				entries = append(entries, cloudstore.MutationEntry{Project: "opaque", Entity: m.Entity, EntityKey: m.EntityKey, Op: m.Op, Payload: json.RawMessage(m.Payload)})
			}
			var w *httptest.ResponseRecorder
			if mode == "mutations" {
				w = opaqueRequest(t, h, "POST", "/sync/mutations/push", map[string]any{"entries": entries})
			} else {
				c := engramsync.ChunkData{Sessions: data.Sessions, Observations: data.Observations, Prompts: data.Prompts}
				if mode == "mixed" {
					c.Mutations = mutations
				}
				w = opaqueRequest(t, h, "POST", "/sync/push", map[string]any{"project": "opaque", "data": c})
			}
			if w.Code != 200 {
				t.Fatalf("push: %d %s", w.Code, w.Body.String())
			}
			known, err := cs.KnownSessionIDs(context.Background(), "opaque")
			if err != nil {
				t.Fatal(err)
			}
			var gotIDs []string
			for id := range known {
				gotIDs = append(gotIDs, id)
			}
			sort.Strings(gotIDs)
			if !reflect.DeepEqual(gotIDs, []string{" session ", "ordinary-control", "session"}) {
				t.Errorf("session index = %q", gotIDs)
			}
			rows, _, seq, err := cs.ListMutationsSince(context.Background(), 0, 100, []string{"opaque"})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 9 {
				t.Fatalf("mutations = %d", len(rows))
			}
			report, err := cs.BackfillMutationChunks(context.Background(), "opaque", false)
			if err != nil {
				t.Fatal(err)
			}
			wantMaterialized, wantPlanned := 9, 0
			if mode == "typed" {
				// Existing backfill compares explicit mutations, not typed arrays.
				wantMaterialized, wantPlanned = 0, 1
			}
			if report.InvalidMutations != 0 || report.AlreadyMaterialized != wantMaterialized || report.ChunksPlanned != wantPlanned {
				t.Fatalf("identity-aware backfill dry run = %+v", report)
			}
			for _, row := range rows {
				var fields map[string]string
				// Decode identity fields without depending on JSON formatting or field order.
				var body map[string]any
				if err := json.Unmarshal(row.Payload, &body); err != nil {
					t.Fatal(err)
				}
				fields = map[string]string{}
				for k, v := range body {
					if text, ok := v.(string); ok {
						fields[k] = text
					}
				}
				if row.Entity == store.SyncEntitySession {
					if row.EntityKey != fields["id"] {
						t.Errorf("session key %q != payload %q", row.EntityKey, fields["id"])
					}
					continue
				}
				name := fields["title"]
				prefix := "obs"
				if row.Entity == store.SyncEntityPrompt {
					name = fields["content"]
					prefix = "prompt"
				}
				for i, id := range ids {
					if name == fmt.Sprintf("%s-%d", prefix, i) && fields["session_id"] != id {
						t.Errorf("%s owner = %q, want %q", name, fields["session_id"], id)
					}
				}
			}
			checkOpaqueReplay(t, h, cs, data)
			// Rejection must not accept a trimmed alias or change data/cursors.
			for _, bad := range []store.Session{{ID: " \t\n ", Project: "opaque", Directory: "/work"}} {
				w = opaqueRequest(t, h, "POST", "/sync/push", map[string]any{"project": "opaque", "data": engramsync.ChunkData{Sessions: []store.Session{bad}}})
				if w.Code != 400 {
					t.Errorf("blank chunk: %d %s", w.Code, w.Body.String())
				}
				w = opaqueRequest(t, h, "POST", "/sync/mutations/push", map[string]any{"entries": []cloudstore.MutationEntry{{Project: "opaque", Entity: "session", EntityKey: bad.ID, Op: "upsert", Payload: opaqueJSON(t, bad)}}})
				if w.Code != 400 {
					t.Errorf("blank mutation: %d %s", w.Code, w.Body.String())
				}
			}
			mismatch := cloudstore.MutationEntry{Project: "opaque", Entity: "session", EntityKey: "session", Op: "upsert", Payload: json.RawMessage(`{"id":" session ","directory":"/work"}`)}
			w = opaqueRequest(t, h, "POST", "/sync/mutations/push", map[string]any{"entries": []cloudstore.MutationEntry{mismatch}})
			if w.Code != 400 {
				t.Errorf("historical mismatch: %d %s", w.Code, w.Body.String())
			}
			if _, err := cs.InsertMutationBatch(context.Background(), []cloudstore.MutationEntry{mismatch}); err == nil {
				t.Error("direct ingestion silently accepted a mismatched historical identity")
			}
			alias := engramsync.ChunkData{Prompts: []store.Prompt{{SyncID: "alias", SessionID: "\tsession\t", Content: "must not attach to a trimmed alias"}}}
			w = opaqueRequest(t, h, "POST", "/sync/push", map[string]any{"project": "opaque", "data": alias})
			if w.Code != 400 {
				t.Errorf("missing exact parent: %d %s", w.Code, w.Body.String())
			}
			after, _, afterSeq, err := cs.ListMutationsSince(context.Background(), 0, 100, []string{"opaque"})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, rows) || afterSeq != seq {
				t.Error("rejected requests changed mutation journal or cursor")
			}
		})
	}
}
