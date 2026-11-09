package cloudserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
)

func TestHandlerPushAcceptsIsolatedSessions(t *testing.T) {
	for _, directory := range []string{"", `,"directory":""`} {
		for _, mutation := range []bool{false, true} {
			t.Run(directory+map[bool]string{false: "/direct", true: "/mutation"}[mutation], func(t *testing.T) {
				st := &fakeStore{}
				session := `{"id":"runtime@target","ownership_mode":"project_owned"` + directory + `}`
				data := `{"sessions":[` + session + `]}`
				if mutation {
					raw, err := json.Marshal(map[string]any{"mutations": []store.SyncMutation{{
						Entity: store.SyncEntitySession, EntityKey: "runtime@target", Op: store.SyncOpUpsert, Payload: session,
					}}})
					if err != nil {
						t.Fatal(err)
					}
					data = string(raw)
				}
				srv := New(st, fakeAuth{}, 0)
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sync/push",
					bytes.NewBufferString(`{"project":"target","created_by":"tester","data":`+data+`}`)))
				if rec.Code != http.StatusOK || len(st.chunks) != 1 {
					t.Fatalf("push = %d %s; chunks=%d", rec.Code, rec.Body.String(), len(st.chunks))
				}
				for _, raw := range st.chunks {
					var chunk engramsync.ChunkData
					if err := json.Unmarshal(raw, &chunk); err != nil {
						t.Fatal(err)
					}
					var got store.Session
					if mutation {
						if err := json.Unmarshal([]byte(chunk.Mutations[0].Payload), &got); err != nil {
							t.Fatal(err)
						}
					} else {
						got = chunk.Sessions[0]
					}
					if got.Directory != "" || got.OwnershipMode != store.SessionOwnershipProjectOwned || got.Project != "target" {
						t.Fatalf("stored isolated session = %+v", got)
					}
				}
			})
		}
	}
}

func TestHandlerPushRejectsInvalidIsolatedDirectory(t *testing.T) {
	for _, directory := range []string{`null`, `123`} {
		for _, mutation := range []bool{false, true} {
			st := &fakeStore{}
			session := `{"id":"runtime@target","ownership_mode":"project_owned","directory":` + directory + `}`
			data := `{"sessions":[` + session + `]}`
			if mutation {
				raw, err := json.Marshal(map[string]any{"mutations": []store.SyncMutation{{Entity: store.SyncEntitySession, EntityKey: "runtime@target", Op: store.SyncOpUpsert, Payload: session}}})
				if err != nil {
					t.Fatal(err)
				}
				data = string(raw)
			}
			rec := httptest.NewRecorder()
			New(st, fakeAuth{}, 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sync/push", bytes.NewBufferString(`{"project":"target","data":`+data+`}`)))
			if rec.Code != http.StatusBadRequest || len(st.chunks) != 0 {
				t.Fatalf("invalid directory %s mutation=%t: %d %s; writes=%d", directory, mutation, rec.Code, rec.Body.String(), len(st.chunks))
			}
		}
	}
}
