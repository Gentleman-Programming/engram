package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestStatsRequestCancellationAfterProjectLookup(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("fixture", "registered", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddObservation(store.AddObservationParams{
		SessionID: "fixture", Project: "registered", Type: "manual", Title: "Fixture", Content: "Synthetic test",
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(st, 0)
	for _, tc := range []struct {
		project, healthy, canceled string
		healthyStatus              int
	}{
		{"registered", "{\"total_sessions\":1,\"total_observations\":1,\"total_prompts\":0,\"projects\":[\"registered\"]}\n",
			"{\"error\":\"stats unavailable\"}\n", http.StatusOK},
		{"missing", "{\"available_projects\":[\"registered\"],\"code\":\"unknown_project\",\"error\":\"project \\\"missing\\\" not found\"}\n",
			"{\"code\":\"project_resolution_failed\",\"error\":\"project resolution failed\"}\n", http.StatusNotFound},
	} {
		t.Run(tc.project, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/stats?project="+tc.project, nil)
			healthy := httptest.NewRecorder()
			srv.Handler().ServeHTTP(healthy, req)
			if healthy.Code != tc.healthyStatus || healthy.Body.String() != tc.healthy {
				t.Fatalf("healthy stats = %d %s", healthy.Code, healthy.Body.String())
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			disconnected := httptest.NewRecorder()
			srv.Handler().ServeHTTP(disconnected, req.WithContext(cancelAfterFirstQuery{Context: ctx, cancel: cancel}))
			if ctx.Err() != context.Canceled {
				t.Fatal("project existence query did not trigger cancellation")
			}
			if disconnected.Code != http.StatusInternalServerError || disconnected.Body.String() != tc.canceled {
				t.Fatalf("stats after cancellation = %d %s", disconnected.Code, disconnected.Body.String())
			}
		})
	}
}
