package cloudserver_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
)

type opaqueTransport struct {
	t       *testing.T
	handler http.Handler
}

func (tr opaqueTransport) ReadManifest() (*engramsync.Manifest, error) {
	w := opaqueRequest(tr.t, tr.handler, "GET", "/sync/pull?project=opaque", nil)
	if w.Code != 200 {
		return nil, fmt.Errorf("manifest: HTTP %d %s", w.Code, w.Body.String())
	}
	var m engramsync.Manifest
	err := json.Unmarshal(w.Body.Bytes(), &m)
	return &m, err
}
func (tr opaqueTransport) ReadChunk(id string) ([]byte, error) {
	w := opaqueRequest(tr.t, tr.handler, "GET", "/sync/pull/"+url.PathEscape(id)+"?project=opaque", nil)
	if w.Code != 200 {
		return nil, fmt.Errorf("chunk: HTTP %d %s", w.Code, w.Body.String())
	}
	return w.Body.Bytes(), nil
}
func (opaqueTransport) WriteManifest(*engramsync.Manifest) error {
	return fmt.Errorf("read-only replay fixture")
}
func (opaqueTransport) WriteChunk(string, []byte, engramsync.ChunkEntry) error {
	return fmt.Errorf("read-only replay fixture")
}

func checkOpaqueReplay(t *testing.T, h http.Handler, cs *cloudstore.CloudStore, expected *store.ExportData) {
	t.Helper()
	w := opaqueRequest(t, h, "GET", "/sync/mutations/pull?since_seq=0&limit=100", nil)
	if w.Code != 200 {
		t.Fatalf("pull: %d %s", w.Code, w.Body.String())
	}
	var pull struct {
		Mutations []cloudstore.StoredMutation `json:"mutations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &pull); err != nil {
		t.Fatal(err)
	}
	mutations := opaqueLocal(t)
	chunks := opaqueLocal(t)
	if err := chunks.EnrollProject("opaque"); err != nil {
		t.Fatal(err)
	}
	sy := engramsync.NewCloudWithTransport(chunks, opaqueTransport{t, h}, "opaque")
	for iteration := 0; iteration < 2; iteration++ {
		for _, m := range pull.Mutations {
			if err := mutations.ApplyPulledMutation("opaque-replay", store.SyncMutation{Seq: m.Seq, Project: m.Project, Entity: m.Entity, EntityKey: m.EntityKey, Op: m.Op, Payload: string(m.Payload)}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := sy.Import(); err != nil {
			t.Fatal(err)
		}
		for name, s := range map[string]*store.Store{"mutation": mutations, "chunk": chunks} {
			actual, err := s.ExportProject("opaque")
			if err != nil {
				t.Fatal(err)
			}
			ids := func(d *store.ExportData) []string {
				var out []string
				for _, r := range d.Sessions {
					out = append(out, r.ID)
				}
				sort.Strings(out)
				return out
			}
			if !reflect.DeepEqual(ids(actual), ids(expected)) {
				t.Errorf("%s replay %d IDs = %q", name, iteration, ids(actual))
			}
			owners := func(d *store.ExportData) map[string]string {
				out := map[string]string{}
				for _, r := range d.Observations {
					out["observation/"+r.SyncID] = r.SessionID
				}
				for _, r := range d.Prompts {
					out["prompt/"+r.SyncID] = r.SessionID
				}
				return out
			}
			if !reflect.DeepEqual(owners(actual), owners(expected)) {
				t.Errorf("%s replay %d owners = %#v, want %#v", name, iteration, owners(actual), owners(expected))
			}
			deferred, dead, err := s.CountDeferredAndDead()
			if err != nil {
				t.Fatal(err)
			}
			if deferred != 0 || dead != 0 {
				t.Errorf("%s replay: deferred=%d dead=%d", name, deferred, dead)
			}
		}
	}
	scoped, err := cs.DashboardStoreForProjects([]string{"opaque"})
	if err != nil {
		t.Fatal(err)
	}
	for name, view := range map[string]dashboard.DashboardStore{"direct": cs, "scoped": scoped} {
		rows, err := view.ListRecentSessions("opaque", "", 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 {
			t.Errorf("%s dashboard sessions = %+v", name, rows)
		}
		mux := http.NewServeMux()
		dashboard.Mount(mux, dashboard.MountConfig{Store: view, RequireSession: func(*http.Request) error { return nil }})
		for i, id := range []string{"session", " session ", "ordinary-control"} {
			sess, obs, prompts, err := view.GetSessionDetail("opaque", id)
			if err != nil {
				t.Fatal(err)
			}
			if sess.SessionID != id || len(obs) != 1 || len(prompts) != 1 {
				t.Errorf("%s lookup %q = %+v obs=%+v prompts=%+v", name, id, sess, obs, prompts)
				continue
			}
			if obs[0].SessionID != id || obs[0].Title != fmt.Sprintf("obs-%d", i) || prompts[0].SessionID != id || prompts[0].Content != fmt.Sprintf("prompt-%d", i) {
				t.Errorf("%s %q wrong children", name, id)
			}
			if _, parent, _, err := view.GetObservationDetail("opaque", id, obs[0].SyncID); err != nil || parent.SessionID != id {
				t.Errorf("observation parent = %+v, %v", parent, err)
			}
			if _, parent, _, err := view.GetPromptDetail("opaque", id, prompts[0].SyncID); err != nil || parent.SessionID != id {
				t.Errorf("prompt parent = %+v, %v", parent, err)
			}
			for _, path := range []string{"/dashboard/sessions/opaque/" + url.PathEscape(id), "/dashboard/observations/opaque/" + url.PathEscape(id) + "/" + url.PathEscape(obs[0].SyncID), "/dashboard/prompts/opaque/" + url.PathEscape(id) + "/" + url.PathEscape(prompts[0].SyncID)} {
				page := opaqueRequest(t, mux, "GET", path, nil)
				if page.Code != 200 {
					t.Errorf("%s: HTTP %d %s", path, page.Code, page.Body.String())
				}
				if strings.Contains(path, "/sessions/") {
					// Match rendered title text, not random sync IDs inside link attributes.
					if !strings.Contains(page.Body.String(), ">"+fmt.Sprintf("obs-%d", i)+"<") || strings.Contains(page.Body.String(), ">"+fmt.Sprintf("obs-%d", (i+1)%3)+"<") {
						t.Errorf("session page %q mixes child content", id)
					}
				}
			}
		}
	}
}
