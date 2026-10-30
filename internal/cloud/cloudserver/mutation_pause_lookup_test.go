package cloudserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

// pauseLookupStore measures fake getter calls, not SQL or database concurrency.
type pauseLookupStore struct {
	*fakeMutationStore
	projects []string
	events   []string
	reads    func(int, string) (bool, error)
	inserts  int
}

func (s *pauseLookupStore) IsProjectSyncEnabled(project string) (bool, error) {
	s.projects = append(s.projects, project)
	s.events = append(s.events, "read:"+project)
	if s.reads != nil {
		return s.reads(len(s.projects), project)
	}
	return s.fakeMutationStore.IsProjectSyncEnabled(project)
}

func (s *pauseLookupStore) InsertMutationBatch(ctx context.Context, entries []MutationEntry) ([]int64, error) {
	s.inserts++
	s.events = append(s.events, "insert")
	return s.fakeMutationStore.InsertMutationBatch(ctx, entries)
}

func (s *pauseLookupStore) InsertAuditEntry(ctx context.Context, entry cloudstore.AuditEntry) error {
	s.events = append(s.events, "audit:"+entry.Project)
	return s.fakeMutationStore.InsertAuditEntry(ctx, entry)
}

func pauseLookupPush(t *testing.T, s *pauseLookupStore, entries []MutationEntry, projects []string, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", marshalPushRequestWithCreatedBy(t, entries, " alice "))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	New(s, multiProjectAuth{token: "secret", projects: projects}, 0).Handler().ServeHTTP(rec, req)
	return rec
}

func TestPauseLookupBatchCardinality(t *testing.T) {
	for _, distinct := range []int{1, 10, 100} {
		t.Run(fmt.Sprintf("B100_D%d", distinct), func(t *testing.T) {
			s := &pauseLookupStore{fakeMutationStore: newFakeMutationStore()}
			projects := make([]string, distinct)
			for i := range projects {
				projects[i] = fmt.Sprintf("proj-%d", i)
			}
			entries := makeMutationEntries(100, projects[0])
			want := make([]string, 100)
			for i := range entries {
				entries[i].Project = projects[i%distinct]
				want[i] = entries[i].Project
			}
			rec := pauseLookupPush(t, s, entries, projects, "secret")
			if rec.Code != http.StatusOK || !reflect.DeepEqual(s.projects, want) || s.inserts != 1 || len(s.mutations) != 100 || len(s.auditCalls) != 0 {
				t.Fatalf("status=%d body=%s reads=%v inserts=%d mutations=%d audits=%v", rec.Code, rec.Body.String(), s.projects, s.inserts, len(s.mutations), s.auditCalls)
			}
		})
	}
}

func TestPauseLookupRejection(t *testing.T) {
	for _, mode := range []string{"paused", "error", "scripted-repeat-pause", "alias", "auth"} {
		t.Run(mode, func(t *testing.T) {
			s := &pauseLookupStore{fakeMutationStore: newFakeMutationStore()}
			s.mutations = makeMutationEntries(1, "prior")
			s.chunks["prior"] = []byte("synthetic")
			prior := append([]MutationEntry(nil), s.mutations...)
			entries := makeMutationEntries(3, "proj-a")
			entries[1].Project, entries[2].Project = "proj-b", "proj-c"
			wantReads := []string{"proj-a", "proj-b"}
			wantEvents := []string{"read:proj-a", "read:proj-b", "audit:proj-b"}
			status, token := http.StatusConflict, "secret"
			body := "{\"error\":\"sync is paused for project \\\"proj-b\\\"\",\"error_class\":\"policy\",\"error_code\":\"sync-paused\",\"project\":\"proj-a\",\"project_path\":\"\",\"project_source\":\"request_body\"}\n"
			s.syncEnabledMap["proj-b"] = false
			switch mode {
			case "error":
				s.reads = func(n int, _ string) (bool, error) {
					if n == 2 {
						return false, fmt.Errorf("synthetic lookup failure")
					}
					return true, nil
				}
				status, body = http.StatusInternalServerError, "check project sync: synthetic lookup failure\n"
				wantEvents = wantEvents[:2]
			case "scripted-repeat-pause":
				// Fake-script proof of a second read, not a real DB concurrency claim.
				entries[1].Project = "proj-a"
				s.reads = func(n int, p string) (bool, error) {
					if n == 2 {
						s.syncEnabledMap[p] = false
					}
					return s.fakeMutationStore.IsProjectSyncEnabled(p)
				}
				wantReads = []string{"proj-a", "proj-a"}
				wantEvents = []string{"read:proj-a", "read:proj-a", "audit:proj-a"}
				body = "{\"error\":\"sync is paused for project \\\"proj-a\\\"\",\"error_class\":\"policy\",\"error_code\":\"sync-paused\",\"project\":\"proj-a\",\"project_path\":\"\",\"project_source\":\"request_body\"}\n"
			case "alias":
				entries[1].Project = " PROJ--B "
			case "auth":
				token, status, body = "wrong", http.StatusUnauthorized, "unauthorized: unauthorized\n"
				wantReads, wantEvents = nil, nil
			}
			rec := pauseLookupPush(t, s, entries, []string{"proj-a", "proj-b", "proj-c"}, token)
			if rec.Code != status || rec.Body.String() != body {
				t.Fatalf("status=%d body=%q; want %d %q", rec.Code, rec.Body.String(), status, body)
			}
			if !reflect.DeepEqual(s.projects, wantReads) || !reflect.DeepEqual(s.events, wantEvents) {
				t.Fatalf("reads=%v events=%v", s.projects, s.events)
			}
			if s.inserts != 0 || !reflect.DeepEqual(s.mutations, prior) || string(s.chunks["prior"]) != "synthetic" || len(s.chunks) != 1 {
				t.Fatalf("rejection changed storage: %+v", s)
			}
			if status == http.StatusConflict {
				wantAudit := []cloudstore.AuditEntry{{Contributor: "alice", Project: wantReads[1], Action: cloudstore.AuditActionMutationPush, Outcome: cloudstore.AuditOutcomeRejectedProjectPaused, EntryCount: 3, ReasonCode: "sync-paused"}}
				if !reflect.DeepEqual(s.auditCalls, wantAudit) {
					t.Fatalf("audits=%+v want=%+v", s.auditCalls, wantAudit)
				}
			} else if len(s.auditCalls) != 0 {
				t.Fatalf("unexpected audits: %+v", s.auditCalls)
			}
		})
	}
}
