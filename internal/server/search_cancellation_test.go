package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSearchCanceledRequest(t *testing.T) {
	for _, format := range []string{"", "&response_format=compact"} {
		t.Run("format="+format, func(t *testing.T) {
			srv := New(newServerTestStore(t), 0)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			req := httptest.NewRequest(http.MethodGet, "/search?q=synthetic&all_projects=true"+format, nil).WithContext(ctx)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusInternalServerError || rec.Body.String() != "{\"error\":\"context canceled\"}\n" {
				t.Fatalf("canceled search = %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestReadCancellationWhileWaitingForConnection(t *testing.T) {
	const canceledRead = "{\"error\":\"context canceled\"}\n"
	const canceledProjectLookup = "{\"code\":\"project_resolution_failed\",\"error\":\"project resolution failed: context canceled\"}\n"
	tests := map[string]struct{ path, errorBody string }{
		"health":                {"/health", "{\"error\":\"health check failed\"}\n"},
		"search":                {"/search?q=synthetic&all_projects=true", canceledRead},
		"compact-search":        {"/search?q=synthetic&all_projects=true&response_format=compact", canceledRead},
		"context-sessions":      {"/context?all_projects=true&pinned=-1&observations=-1&prompts=-1", canceledRead},
		"context-pinned":        {"/context?all_projects=true&sessions=-1&observations=-1&prompts=-1", canceledRead},
		"context-observations":  {"/context?all_projects=true&sessions=-1&pinned=-1&prompts=-1", canceledRead},
		"context-prompts":       {"/context?all_projects=true&sessions=-1&pinned=-1&observations=-1", canceledRead},
		"context-older-history": {"/context?all_projects=true&sessions=-1&pinned=-1&prompts=-1&older_history=true", canceledRead},
		"context-project":       {"/context?project=fixture", canceledProjectLookup},
		"search-project":        {"/search?q=synthetic&project=fixture", canceledProjectLookup},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			st := newServerTestStore(t)
			if got := st.DB().Stats().MaxOpenConnections; got != 1 {
				t.Fatalf("fixture requires one connection, got %d", got)
			}
			srv := New(st, 0)
			conn, err := st.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			t.Cleanup(func() {
				cancel()
				_ = conn.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("handler did not finish after connection release")
				}
			})
			req := httptest.NewRequest(http.MethodGet, tc.path, nil).WithContext(ctx)
			rec := httptest.NewRecorder()
			before := st.DB().Stats().WaitCount
			go func() {
				srv.Handler().ServeHTTP(rec, req)
				close(done)
			}()

			// Observe actual pool admission before canceling. The ticker only
			// polls state; elapsed time is not evidence that the query started.
			watchdog := time.NewTimer(5 * time.Second)
			defer watchdog.Stop()
			poll := time.NewTicker(time.Millisecond)
			defer poll.Stop()
			for st.DB().Stats().WaitCount == before {
				select {
				case <-done:
					t.Fatal("handler exited before waiting for the retained connection")
				case <-watchdog.C:
					t.Fatal("handler never entered the connection pool wait")
				case <-poll.C:
				}
			}
			cancel()
			watchdog.Reset(5 * time.Second)
			select {
			case <-done:
				if rec.Code != http.StatusInternalServerError || rec.Body.String() != tc.errorBody {
					t.Fatalf("canceled request = %d %s", rec.Code, rec.Body.String())
				}
			case <-watchdog.C:
				t.Fatal("request cancellation failed to end the connection pool wait")
			}
			// conn stays retained until cleanup, so releasing the connection
			// cannot be what allowed the canceled handler to finish.
		})
	}
}
