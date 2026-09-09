//go:build e2e

package server

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func TestProjectMergeE2E(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", projectMergeTestToken)
	st, ts := newE2EServer(t)
	seedHTTPProjectMerge(t, st, "Engram")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/projects/merge", bytes.NewBufferString(`{"from":["Engram"],"to":"engram","confirmed":true}`))
	if err != nil {
		t.Fatalf("new merge request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+projectMergeTestToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("merge projects: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, decodeJSON[map[string]any](t, resp))
	}
	result := decodeJSON[store.MergeResult](t, resp)
	if result.Canonical != "engram" || result.ObservationsUpdated != 1 || result.SessionsUpdated != 1 || result.PromptsUpdated != 1 {
		t.Fatalf("unexpected merge result: %+v", result)
	}

	sessionResp, err := ts.Client().Get(ts.URL + "/sessions/merge-session")
	if err != nil {
		t.Fatalf("get merged session: %v", err)
	}
	if sessionResp.StatusCode != http.StatusOK {
		t.Fatalf("get merged session status = %d", sessionResp.StatusCode)
	}
	session := decodeJSON[store.Session](t, sessionResp)
	if session.Project != "engram" {
		t.Fatalf("merged session project = %q, want engram", session.Project)
	}
}
