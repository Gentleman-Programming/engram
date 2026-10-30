package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestIsolatedSessionHealthCapability(t *testing.T) {
	rec := httptest.NewRecorder()
	New(newServerTestStore(t), 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Capabilities["isolated_session_registration"] != true {
		t.Fatalf("missing supported isolation capability: %s", rec.Body.String())
	}
}

func TestIsolatedSessionRegistration(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, directory := range []string{"", " /repos/runtime "} {
			for _, continuation := range []bool{false, true} {
				if continuation && !resume {
					continue
				}
				t.Run(fmt.Sprintf("resume=%t/directory=%q/continuation=%t", resume, directory, continuation), func(t *testing.T) {
					st := newServerTestStore(t)
					root, selected := "satellite", "satellite"
					if continuation {
						if err := st.StartSessionWithOwnershipMode(root, "target", "", store.SessionOwnershipProjectOwned); err != nil {
							t.Fatal(err)
						}
						if err := st.EndSession(root, "terminal"); err != nil {
							t.Fatal(err)
						}
						selected += ":resume:2"
					}
					if err := st.StartSessionWithOwnershipMode(selected, "target", directory, store.SessionOwnershipProjectOwned); err != nil {
						t.Fatal(err)
					}
					if _, err := st.DB().Exec(`UPDATE sessions SET runtime_lease_expires_at = '2000-01-01 00:00:00' WHERE id = ?`, selected); err != nil {
						t.Fatal(err)
					}
					before, err := st.GetSession(selected)
					if err != nil {
						t.Fatal(err)
					}
					var mutationsBefore int
					if err := st.DB().QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsBefore); err != nil {
						t.Fatal(err)
					}
					rec := httptest.NewRecorder()
					body := fmt.Sprintf(`{"id":%q,"project":"target","ownership_mode":"project_owned","isolated":true,"resume":%t}`, root, resume)
					New(st, 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(body)))
					after, err := st.GetSession(selected)
					if err != nil {
						t.Fatal(err)
					}
					if strings.TrimSpace(directory) != "" {
						if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "session_isolation_conflict") {
							t.Fatalf("expected isolation conflict, got %d: %s", rec.Code, rec.Body.String())
						}
						if !reflect.DeepEqual(before, after) {
							t.Fatalf("rejected registration mutated session or lease: %#v -> %#v", before, after)
						}
						var mutationsAfter int
						if err := st.DB().QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsAfter); err != nil {
							t.Fatal(err)
						}
						if mutationsBefore != mutationsAfter {
							t.Fatal("rejected registration changed sync mutations")
						}
					} else {
						if rec.Code != http.StatusCreated || after.Directory != "" {
							t.Fatalf("expected isolated registration, got %d: %s; directory=%q", rec.Code, rec.Body.String(), after.Directory)
						}
						candidates, err := st.ActiveRuntimeSessions("target", "/repos/runtime")
						if err != nil {
							t.Fatal(err)
						}
						for _, candidate := range candidates {
							if candidate == selected {
								t.Fatal("isolated satellite became a runtime candidate")
							}
						}
					}
				})
			}
		}
	}
}

func TestIsolatedSessionRejectsEndedNonblankRootBeforeContinuation(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.StartSessionWithOwnershipMode("satellite", "target", "/repos/runtime", store.SessionOwnershipProjectOwned); err != nil {
		t.Fatal(err)
	}
	if err := st.EndSession("satellite", "terminal"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New(st, 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(`{"id":"satellite","project":"target","ownership_mode":"project_owned","isolated":true,"resume":true}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected isolation conflict, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := st.GetSession("satellite:resume:2"); err == nil {
		t.Fatal("rejected root created a continuation")
	}
}

func TestIsolatedSessionNewAndInvalidRequests(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, extra := range []string{"", `,"directory":""`, `,"directory":" \t\n "`, `,"directory":" /repos/runtime "`, `,"ownership_mode":"shared"`} {
			t.Run(fmt.Sprintf("resume=%t/extra=%s", resume, extra), func(t *testing.T) {
				st := newServerTestStore(t)
				rec := httptest.NewRecorder()
				mode := `,"ownership_mode":"project_owned"`
				if strings.Contains(extra, "ownership_mode") {
					mode = ""
				}
				body := fmt.Sprintf(`{"id":"satellite","project":"target","isolated":true,"resume":%t%s%s}`, resume, mode, extra)
				New(st, 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(body)))
				if strings.Contains(extra, "/repos/runtime") || strings.Contains(extra, "shared") {
					if rec.Code != http.StatusBadRequest {
						t.Fatalf("expected bad request, got %d: %s", rec.Code, rec.Body.String())
					}
					if _, err := st.GetSession("satellite"); err == nil {
						t.Fatal("invalid isolation request created session")
					}
				} else {
					session, err := st.GetSession("satellite")
					if rec.Code != http.StatusCreated || err != nil || session.Directory != "" || session.OwnershipMode != store.SessionOwnershipProjectOwned {
						t.Fatalf("new isolated session: %d %s %#v %v", rec.Code, rec.Body.String(), session, err)
					}
				}
			})
		}
	}
}
