package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

const projectMergeTestToken = "merge-token"

func serveProjectMerge(t *testing.T, h http.Handler, body, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/projects/merge", strings.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func seedHTTPProjectMerge(t *testing.T, st *store.Store, project string) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, []any{"merge-session", project, "/work/engram"}},
		{`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, []any{"merge-observation", "merge-session", "decision", "legacy", "content", project, "project", "merge-hash"}},
		{`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`, []any{"merge-prompt", "merge-session", "prompt", project}},
		{`INSERT INTO sync_enrolled_projects (project) VALUES (?)`, []any{project}},
		{`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`, []any{store.DefaultSyncTargetKey, store.SyncEntityObservation, "merge-observation", store.SyncOpUpsert, fmt.Sprintf(`{"sync_id":"merge-observation","project":%q}`, project), store.SyncSourceLocal, project}},
	}
	for _, statement := range statements {
		if _, err := st.DB().Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed project merge: %v", err)
		}
	}
}

func TestHandleMergeProjectsRequiresConfiguredAuth(t *testing.T) {
	for _, tt := range []struct {
		name          string
		serverToken   string
		authorization string
		wantStatus    int
	}{
		{name: "token unset", authorization: "Bearer " + projectMergeTestToken, wantStatus: http.StatusServiceUnavailable},
		{name: "credential missing", serverToken: projectMergeTestToken, wantStatus: http.StatusUnauthorized},
		{name: "credential wrong", serverToken: projectMergeTestToken, authorization: "Bearer wrong", wantStatus: http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENGRAM_HTTP_TOKEN", tt.serverToken)
			srv := New(newServerTestStore(t), 0)
			var writes int32
			srv.SetOnWrite(func() { atomic.AddInt32(&writes, 1) })

			rec := serveProjectMerge(t, srv.Handler(), `{"from":["Engram"],"to":"engram","confirmed":true}`, tt.authorization)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if atomic.LoadInt32(&writes) != 0 {
				t.Fatalf("rejected request notified autosync")
			}
		})
	}
}

func TestHandleMergeProjectsValidatesRequest(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", projectMergeTestToken)
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "invalid JSON", body: `{"from":`},
		{name: "confirmation required", body: `{"from":["Engram"],"to":"engram"}`},
		{name: "sources required", body: `{"from":[],"to":"engram","confirmed":true}`},
		{name: "target required", body: `{"from":["Engram"],"to":" ","confirmed":true}`},
		{name: "blank source", body: `{"from":[" "],"to":"engram","confirmed":true}`},
		{name: "non-equivalent source", body: `{"from":["other"],"to":"engram","confirmed":true}`},
		{name: "oversized", body: `{"from":["Engram"],"to":"engram","confirmed":true,"padding":"` + strings.Repeat("x", 8<<10) + `"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(newServerTestStore(t), 0)
			var writes int32
			srv.SetOnWrite(func() { atomic.AddInt32(&writes, 1) })

			rec := serveProjectMerge(t, srv.Handler(), tt.body, "Bearer "+projectMergeTestToken)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if atomic.LoadInt32(&writes) != 0 {
				t.Fatalf("invalid request notified autosync")
			}
		})
	}
}

func TestHandleMergeProjectsMergesSyncIdentityAndIsIdempotent(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", projectMergeTestToken)
	st := newServerTestStore(t)
	seedHTTPProjectMerge(t, st, "Engram")
	srv := New(st, 0)
	var writes int32
	srv.SetOnWrite(func() { atomic.AddInt32(&writes, 1) })
	body := `{"from":["Engram"],"to":"engram","confirmed":true}`

	rec := serveProjectMerge(t, srv.Handler(), body, "Bearer "+projectMergeTestToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var result store.MergeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Canonical != "engram" || len(result.SourcesMerged) != 1 || result.SourcesMerged[0] != "Engram" {
		t.Fatalf("unexpected merge identity: %+v", result)
	}
	if result.ObservationsUpdated != 1 || result.SessionsUpdated != 1 || result.PromptsUpdated != 1 {
		t.Fatalf("unexpected merge counts: %+v", result)
	}
	for _, table := range []string{"sessions", "observations", "user_prompts"} {
		var legacy, canonical int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE project = 'Engram'`).Scan(&legacy); err != nil {
			t.Fatalf("count legacy %s: %v", table, err)
		}
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE project = 'engram'`).Scan(&canonical); err != nil {
			t.Fatalf("count canonical %s: %v", table, err)
		}
		if legacy != 0 || canonical != 1 {
			t.Fatalf("%s project rows: legacy=%d canonical=%d", table, legacy, canonical)
		}
	}
	enrolled, err := st.IsProjectEnrolled("engram")
	if err != nil || !enrolled {
		t.Fatalf("canonical enrollment = %v, err=%v", enrolled, err)
	}
	pending, err := st.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending mutations: %v", err)
	}
	if len(pending) == 0 {
		t.Fatal("merge left no deliverable sync mutations")
	}
	for _, mutation := range pending {
		if mutation.Project != "engram" {
			t.Fatalf("pending mutation kept project %q: %+v", mutation.Project, mutation)
		}
		var payload struct {
			Project string `json:"project"`
		}
		if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
			t.Fatalf("decode pending mutation payload: %v", err)
		}
		if payload.Project != "engram" {
			t.Fatalf("pending mutation payload kept project %q: %+v", payload.Project, mutation)
		}
	}
	if atomic.LoadInt32(&writes) != 1 {
		t.Fatalf("autosync notifications = %d, want 1", writes)
	}

	rec = serveProjectMerge(t, srv.Handler(), body, "Bearer "+projectMergeTestToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("idempotent status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode idempotent result: %v", err)
	}
	if len(result.SourcesMerged) != 0 || result.ObservationsUpdated != 0 || result.SessionsUpdated != 0 || result.PromptsUpdated != 0 {
		t.Fatalf("idempotent merge moved records: %+v", result)
	}
	if atomic.LoadInt32(&writes) != 2 {
		t.Fatalf("successful idempotent merge notifications = %d, want 2", writes)
	}
}

func TestHandleMergeProjectsFailsClosedForMixedSources(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", projectMergeTestToken)
	st := newServerTestStore(t)
	seedHTTPProjectMerge(t, st, "Engram")
	srv := New(st, 0)
	var writes int32
	srv.SetOnWrite(func() { atomic.AddInt32(&writes, 1) })

	rec := serveProjectMerge(t, srv.Handler(), `{"from":["Engram","other"],"to":"engram","confirmed":true}`, "Bearer "+projectMergeTestToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var legacy int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM observations WHERE project = 'Engram'`).Scan(&legacy); err != nil {
		t.Fatalf("count legacy observations: %v", err)
	}
	if legacy != 1 {
		t.Fatalf("mixed-source request partially merged: legacy observations = %d", legacy)
	}
	if atomic.LoadInt32(&writes) != 0 {
		t.Fatalf("failed merge notified autosync")
	}
}

func TestHandleMergeProjectsHidesInfrastructureErrors(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", projectMergeTestToken)
	st := newServerTestStore(t)
	srv := New(st, 0)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	rec := serveProjectMerge(t, srv.Handler(), `{"from":["Engram"],"to":"engram","confirmed":true}`, "Bearer "+projectMergeTestToken)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "database is closed") {
		t.Fatalf("response leaked infrastructure error: %s", rec.Body.String())
	}
}
