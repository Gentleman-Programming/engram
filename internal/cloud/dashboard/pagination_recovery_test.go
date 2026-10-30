package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

// recoveryStore sequences initial, retry and fallback calls without hiding their order.
type recoveryStore struct {
	parityStoreStub
	t               *testing.T
	events          []string
	log             strings.Builder
	label           string
	controlsErr     error
	initialErr      error
	fallbackSuccess bool
	ctx             context.Context
}

func (s *recoveryStore) fetch(limit, offset int, filters ...string) (int, error) {
	s.t.Helper()
	s.events = append(s.events, fmt.Sprintf("fetch:%d", offset))
	if limit != 10 {
		s.t.Errorf("limit = %d", limit)
	}
	for _, filter := range filters {
		if filter != "needle" {
			s.t.Errorf("filter = %q", filter)
		}
	}
	n := 0
	for _, event := range s.events {
		if strings.HasPrefix(event, "fetch:") {
			n++
		}
	}
	if n == 1 {
		return 25, s.initialErr
	}
	if n == 2 {
		return 999, errors.New("retry")
	}
	if !strings.Contains(s.log.String(), "re-fetch "+s.label+" page 3: retry") {
		s.t.Error("retry was not logged before fallback")
	}
	if s.fallbackSuccess {
		return 999, nil
	}
	return 999, errors.New("fallback")
}
func (s *recoveryStore) ListRecentObservationsPaginated(p, q, k string, l, o int) ([]cloudstore.DashboardObservationRow, int, error) {
	n, e := s.fetch(l, o, p, q, k)
	if n == 999 && e == nil {
		return []cloudstore.DashboardObservationRow{{Title: "Recovered"}}, n, e
	}
	return nil, n, e
}
func (s *recoveryStore) ListRecentSessionsPaginated(p, q string, l, o int) ([]cloudstore.DashboardSessionRow, int, error) {
	n, e := s.fetch(l, o, p, q)
	return nil, n, e
}
func (s *recoveryStore) ListRecentPromptsPaginated(p, q string, l, o int) ([]cloudstore.DashboardPromptRow, int, error) {
	n, e := s.fetch(l, o, p, q)
	return nil, n, e
}
func (s *recoveryStore) ListContributorsPaginated(q string, l, o int) ([]cloudstore.DashboardContributorRow, int, error) {
	n, e := s.fetch(l, o, q)
	return nil, n, e
}
func (s *recoveryStore) ListProjectsPaginated(q string, l, o int) ([]cloudstore.DashboardProjectRow, int, error) {
	n, e := s.fetch(l, o, q)
	return nil, n, e
}
func (s *recoveryStore) ListProjectSyncControls() ([]cloudstore.ProjectSyncControl, error) {
	s.events = append(s.events, "controls")
	return nil, s.controlsErr
}
func (s *recoveryStore) ListAuditEntriesPaginated(ctx context.Context, f cloudstore.AuditFilter, l, o int) ([]cloudstore.DashboardAuditRow, int, error) {
	if ctx != s.ctx {
		s.t.Error("audit request context changed")
	}
	n, e := s.fetch(l, o, f.Project, f.Contributor, f.Outcome)
	return nil, n, e
}

func TestRecoverPaginationRows(t *testing.T) {
	failure := errors.New("fetch")
	for _, tc := range []struct {
		name                      string
		original, retry, fallback []int
		retryErr, fallbackErr     error
		offset                    int
		want                      []int
		events                    []string
	}{
		{name: "retry success", original: []int{1}, retry: []int{2}, offset: 20, want: []int{2}, events: []string{"fetch:20"}},
		{name: "retain rows", original: []int{1}, retry: []int{9}, retryErr: failure, offset: 20, want: []int{1}, events: []string{"fetch:20", "retry error"}},
		{name: "fallback success", retryErr: failure, fallback: []int{3}, offset: 20, want: []int{3}, events: []string{"fetch:20", "retry error", "fetch:0"}},
		{name: "fallback failure retains nil", retryErr: failure, fallbackErr: failure, offset: 20, events: []string{"fetch:20", "retry error", "fetch:0", "fallback error"}},
		{name: "fallback failure retains empty", original: []int{}, retryErr: failure, fallbackErr: failure, offset: 20, want: []int{}, events: []string{"fetch:20", "retry error", "fetch:0", "fallback error"}},
		{name: "retry nil success", original: []int{1}, offset: 20, events: []string{"fetch:20"}},
		{name: "retry empty success", original: []int{1}, retry: []int{}, offset: 20, want: []int{}, events: []string{"fetch:20"}},
		{name: "fallback nil success", original: []int{}, retryErr: failure, offset: 20, events: []string{"fetch:20", "retry error", "fetch:0"}},
		{name: "fallback empty success repeated zero", retryErr: failure, fallback: []int{}, want: []int{}, events: []string{"fetch:0", "retry error", "fetch:0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			calls := 0
			got := recoverPaginationRows(tc.original, tc.offset, func(offset int) ([]int, int, error) {
				events = append(events, fmt.Sprintf("fetch:%d", offset))
				calls++
				if calls == 1 {
					return tc.retry, 999, tc.retryErr
				}
				return tc.fallback, 999, tc.fallbackErr
			}, func(err error) {
				if err != failure {
					t.Errorf("retry error %v", err)
				}
				events = append(events, "retry error")
			}, func(err error) {
				if err != failure {
					t.Errorf("fallback error %v", err)
				}
				events = append(events, "fallback error")
			})
			if !reflect.DeepEqual(got, tc.want) || !reflect.DeepEqual(events, tc.events) {
				t.Errorf("rows %#v events %v; want %#v %v", got, events, tc.want, tc.events)
			}
		})
	}
}

func TestPaginationRecoveryHandlers(t *testing.T) {
	// These tests are deliberately nonparallel: they temporarily capture the standard logger.
	for _, tc := range []struct {
		path, label string
		suffix      bool
	}{
		{"browser/observations", "observations", true}, {"browser/sessions", "sessions", true},
		{"browser/prompts", "prompts", true}, {"contributors/list", "contributors list", false},
		{"projects/list", "projects list", true}, {"admin/audit-log/list", "audit log list", true},
	} {
		t.Run(tc.label, func(t *testing.T) {
			s := &recoveryStore{t: t, label: tc.label}
			old := log.Writer()
			log.SetOutput(&s.log)
			defer log.SetOutput(old)
			mux := http.NewServeMux()
			Mount(mux, MountConfig{Store: s, RequireSession: func(*http.Request) error { return nil }, IsAdmin: func(*http.Request) bool { return true }})
			req := httptest.NewRequest("GET", "/dashboard/"+tc.path+"?page=5&pageSize=10&q=needle&project=needle&type=needle&contributor=needle&outcome=needle", nil)
			req.Header.Set("HX-Request", "true")
			type contextKey struct{}
			req = req.WithContext(context.WithValue(req.Context(), contextKey{}, "request"))
			s.ctx = req.Context()
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			want := []string{"fetch:40", "fetch:20", "fetch:0"}
			if tc.label == "projects list" {
				want = []string{"fetch:40", "controls", "fetch:20", "fetch:0"}
			}
			if !reflect.DeepEqual(s.events, want) {
				t.Errorf("events %v, want %v", s.events, want)
			}
			retry := "dashboard: re-fetch " + tc.label + " page 3: retry"
			if tc.suffix {
				retry += " (using first-page rows)"
			}
			logs := s.log.String()
			if !strings.Contains(logs, retry+"\n") || !strings.Contains(logs, "dashboard: fallback "+tc.label+" page 1: fallback\n") {
				t.Errorf("logs %q", logs)
			}
			if strings.Contains(rec.Body.String(), "pagination-info") {
				t.Error("empty results unexpectedly rendered pagination")
			}
			if tc.label == "observations" {
				s.events = nil
				s.fallbackSuccess = true
				rec = httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if !strings.Contains(rec.Body.String(), "21–25 of 25") || !strings.Contains(rec.Body.String(), "Recovered") {
					t.Errorf("fallback rows or original-total pagination missing: %s", rec.Body.String())
				}
			}
			// Initial failures stop before recovery (and before project controls).
			s.events = nil
			s.initialErr = errors.New("initial")
			rec = httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			wantStatus := 502
			if strings.HasPrefix(tc.path, "browser/") {
				wantStatus = 503
			}
			if rec.Code != wantStatus || !reflect.DeepEqual(s.events, []string{"fetch:40"}) {
				t.Errorf("initial failure: status %d events %v", rec.Code, s.events)
			}
			// A nil store still renders successfully, with no recovery diagnostics.
			nilMux := http.NewServeMux()
			Mount(nilMux, MountConfig{RequireSession: func(*http.Request) error { return nil }, IsAdmin: func(*http.Request) bool { return true }})
			before := s.log.String()
			rec = httptest.NewRecorder()
			nilMux.ServeHTTP(rec, req)
			if rec.Code != 200 || s.log.String() != before {
				t.Errorf("nil store: status %d logs %q", rec.Code, s.log.String())
			}
		})
	}
}

func TestPaginationRecoveryProjectControlsFailure(t *testing.T) {
	s := &recoveryStore{t: t, controlsErr: errors.New("controls")}
	h := &handlers{cfg: MountConfig{Store: s}}
	rec := httptest.NewRecorder()
	h.handleProjectsList(rec, httptest.NewRequest("GET", "/dashboard/projects/list?page=5&q=needle", nil))
	if rec.Code != 502 || !reflect.DeepEqual(s.events, []string{"fetch:40", "controls"}) {
		t.Fatalf("status %d events %v", rec.Code, s.events)
	}
}
