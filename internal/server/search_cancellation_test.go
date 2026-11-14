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

func TestSearchCancellationWhileWaitingForConnection(t *testing.T) {
	for _, format := range []string{"", "&response_format=compact"} {
		t.Run("format="+format, func(t *testing.T) {
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
					t.Error("search handler did not finish after connection release")
				}
			})
			req := httptest.NewRequest(http.MethodGet, "/search?q=synthetic&all_projects=true"+format, nil).WithContext(ctx)
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
					t.Fatal("search exited before waiting for the retained connection")
				case <-watchdog.C:
					t.Fatal("search never entered the connection pool wait")
				case <-poll.C:
				}
			}
			cancel()
			watchdog.Reset(5 * time.Second)
			select {
			case <-done:
				if rec.Code != http.StatusInternalServerError || rec.Body.String() != "{\"error\":\"context canceled\"}\n" {
					t.Fatalf("canceled search = %d %s", rec.Code, rec.Body.String())
				}
			case <-watchdog.C:
				t.Fatal("request cancellation failed to end the connection pool wait")
			}
			// conn stays retained until cleanup, so releasing the connection
			// cannot be what allowed the canceled handler to finish.
		})
	}
}
