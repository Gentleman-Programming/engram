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

func TestSearchCancelsUnknownProjectHint(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/search?q=synthetic&project=missing", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req.WithContext(cancelAfterFirstQuery{Context: ctx, cancel: cancel}))
	if ctx.Err() != context.Canceled {
		t.Fatal("the project existence query did not trigger cancellation")
	}
	const canceled = "{\"code\":\"project_resolution_failed\",\"error\":\"project resolution failed: context canceled\"}\n"
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != canceled {
		t.Fatalf("unknown project hint after cancellation = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSessionReadCancelsBeforeProvenance(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.StartSession("fixture", "project", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	srv := New(st, 0)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/sessions/fixture", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req.WithContext(cancelAfterFirstQuery{Context: ctx, cancel: cancel}))
	if ctx.Err() != context.Canceled {
		t.Fatal("the session query did not trigger cancellation")
	}
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != "{\"error\":\"context canceled\"}\n" {
		t.Fatalf("session after cancellation = %d %s", rec.Code, rec.Body.String())
	}
}

type cancelAfterFirstQuery struct {
	context.Context
	cancel context.CancelFunc
}

// Hide the embedded cancellation state so database/sql uses AfterFunc when
// it attaches the cancellation watcher to the first query's rows.
func (ctx cancelAfterFirstQuery) Value(any) any { return nil }

func (ctx cancelAfterFirstQuery) AfterFunc(fn func()) func() bool {
	stop := context.AfterFunc(ctx.Context, fn)
	return func() bool {
		stopped := stop()
		if stopped {
			ctx.cancel()
		}
		return stopped
	}
}

func TestHealthDoesNotWaitForStoreConnection(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	held, err := st.DB().Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/health", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health with a busy store connection = %d %s", rec.Code, rec.Body.String())
	}
}

func TestReadCancellationWhileWaitingForConnection(t *testing.T) {
	const canceledRead = "{\"error\":\"context canceled\"}\n"
	const canceledProjectLookup = "{\"code\":\"project_resolution_failed\",\"error\":\"project resolution failed: context canceled\"}\n"
	tests := map[string]struct{ path, errorBody string }{
		"session-owner":         {"/sessions/synthetic", canceledRead},
		"search":                {"/search?q=synthetic&all_projects=true", canceledRead},
		"compact-search":        {"/search?q=synthetic&all_projects=true&response_format=compact", canceledRead},
		"context-sessions":      {"/context?all_projects=true&pinned=-1&observations=-1&prompts=-1", canceledRead},
		"context-pinned":        {"/context?all_projects=true&sessions=-1&observations=-1&prompts=-1", canceledRead},
		"context-observations":  {"/context?all_projects=true&sessions=-1&pinned=-1&prompts=-1", canceledRead},
		"context-prompts":       {"/context?all_projects=true&sessions=-1&pinned=-1&observations=-1", canceledRead},
		"context-older-history": {"/context?all_projects=true&sessions=-1&pinned=-1&prompts=-1&older_history=true", canceledRead},
		"compaction-context":    {"/context/compaction?session_id=fixture", canceledRead},
		"stats-unscoped":        {"/stats?all_projects=true", "{\"error\":\"stats unavailable\"}\n"},
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
