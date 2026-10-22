package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestReviewProjectContractUnresolvedHTTP(t *testing.T) {
	_, cwd := httpHomeSelectionFixture(t)
	f := newWorkflowReviewFixture(t)
	before := f.snapshot(t)
	pending, err := f.store.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1000)
	if err != nil {
		t.Fatal(err)
	}
	got := homeSelectionHTTP(t, f.server, http.MethodPost, "/review/mark_reviewed?cwd="+url.QueryEscape(cwd), fmt.Sprintf(`{"id":%d}`, f.ids[0]), http.StatusConflict)
	want := map[string]any{"code": "explicit_project_required", "error": "project resolution failed: explicit project required: use --project, ENGRAM_PROJECT, or .engram/config.json project_name"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unresolved refusal: %v, want %v", got, want)
	}
	f.unchanged(t, before)
	after, err := f.store.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1000)
	if err != nil || !reflect.DeepEqual(pending, after) {
		t.Errorf("refusal changed pending sync mutations: before=%v after=%v err=%v", pending, after, err)
	}
}

func TestReviewProjectContractHTTP(t *testing.T) {
	for _, tc := range []struct{ name, current, owner, query string }{
		{"explicit positive", "beta", "alpha", "?project=ALPHA"},
		{"explicit wrong owner", "beta", "beta", "?project=ALPHA"},
		{"omitted wrong owner", "alpha", "beta", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_PROJECT", tc.current)
			s := newServerTestStore(t)
			for _, p := range []string{"alpha", "beta"} {
				if err := s.CreateSession(p, p, ""); err != nil {
					t.Fatal(err)
				}
				if err := s.EnrollProject(p); err != nil {
					t.Fatal(err)
				}
			}
			id, err := s.AddObservation(store.AddObservationParams{SessionID: tc.owner, Project: tc.owner, Type: "decision", Title: "HTTP ownership", Content: "review target"})
			if err != nil {
				t.Fatal(err)
			}
			past := "2000-01-01 00:00:00"
			if _, err := s.DB().Exec(`UPDATE observations SET review_after = ? WHERE id = ?`, past, id); err != nil {
				t.Fatal(err)
			}
			snapshot := func() []any {
				t.Helper()
				data, err := s.Export()
				if err != nil {
					t.Fatal(err)
				}
				data.ExportedAt = ""
				pending, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1000)
				if err != nil {
					t.Fatal(err)
				}
				stats, err := s.Stats()
				if err != nil {
					t.Fatal(err)
				}
				seq, err := s.MaxPendingSyncMutationSeq("")
				if err != nil {
					t.Fatal(err)
				}
				return []any{data, pending, stats, seq}
			}
			before := snapshot()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/review/mark_reviewed"+tc.query, strings.NewReader(fmt.Sprintf(`{"id":%d}`, id)))
			New(s, 0).Handler().ServeHTTP(rec, req)
			if tc.owner == "beta" {
				if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != `{"error":"observation not found in resolved project"}` {
					t.Fatalf("refusal: %d %s", rec.Code, rec.Body.String())
				}
				if after := snapshot(); !reflect.DeepEqual(before, after) {
					t.Fatalf("refusal mutated observations/counters/sync: before=%v after=%v", before, after)
				}
			} else {
				if rec.Code != http.StatusOK {
					t.Fatalf("mark: %d %s", rec.Code, rec.Body.String())
				}
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["id"] != float64(id) || body["state"] != store.ObservationStateActive {
					t.Fatalf("mark result: %v", body)
				}
				obs, err := s.GetObservation(id)
				if err != nil || obs.ReviewAfter == nil || *obs.ReviewAfter == past {
					t.Fatalf("mark had no effect: %+v %v", obs, err)
				}
			}
		})
	}
}
