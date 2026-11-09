package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCaptureProjectProvenanceOmissionAndSessionEnd(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	for _, payload := range []string{
		`{"id":"known","project":"example","project_provenance":{"source":"git_root"}}`,
		`{"id":"known","project":"example"}`,
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/sessions", strings.NewReader(payload)))
		if rec.Code != 201 {
			t.Fatalf("register=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/sessions/known/end", strings.NewReader(`{"summary":"finished","project_provenance":{"source":"explicit_override"}}`)))
	if rec.Code != 200 {
		t.Fatalf("end=%d body=%s", rec.Code, rec.Body.String())
	}
	p, err := st.CaptureProjectProvenance("session", "known")
	if err != nil || p == nil || p.Source != "git_root" {
		t.Fatalf("known evidence downgraded: %+v, %v", p, err)
	}
	session, err := st.GetSession("known")
	if err != nil || session.EndedAt == nil {
		t.Fatalf("ended session=%+v err=%v", session, err)
	}
}

func TestCaptureProjectProvenanceHTTPWrites(t *testing.T) {
	for _, endpoint := range []string{"observations", "prompts"} {
		t.Run(endpoint, func(t *testing.T) {
			st := newServerTestStore(t)
			if err := st.CreateSession("capture", "example", ""); err != nil {
				t.Fatal(err)
			}
			srv := New(st, 0)
			for _, source := range []string{"explicit_override", "git_remote", "dir_basename", "unknown"} {
				rec := httptest.NewRecorder()
				payload := `{"session_id":"capture","project":"example","type":"manual","title":"` + source + `","content":"` + source + `","project_provenance":{"source":"` + source + `","raw_project":"Raw Project","directory":"/original"}}`
				srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/"+endpoint, strings.NewReader(payload)))
				if rec.Code != 201 {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				var response struct {
					ID int64 `json:"id"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				kind := "observation"
				if endpoint == "prompts" {
					kind = "prompt"
				}
				evidence, err := st.CaptureProjectProvenance(kind, strconv.FormatInt(response.ID, 10))
				if err != nil || evidence == nil || evidence.Source != source || evidence.RawProject != "Raw Project" || evidence.Directory != "/original" {
					t.Fatalf("evidence=%+v err=%v", evidence, err)
				}
			}
		})
	}
}

func TestCaptureProjectProvenanceHTTPRejectionNoMutation(t *testing.T) {
	for _, endpoint := range []string{"sessions", "observations", "prompts"} {
		for _, metadata := range []string{`{"source":"invalid"}`, `{"source":42}`, `{"source":null}`, `{"directory":false}`, `[]`, `"explicit_override"`} {
			t.Run(endpoint+metadata, func(t *testing.T) {
				st := newServerTestStore(t)
				if err := st.CreateSession("capture", "example", ""); err != nil {
					t.Fatal(err)
				}
				if err := st.EnrollProject("example"); err != nil {
					t.Fatal(err)
				}
				srv := New(st, 0)
				request := func(method, path, payload string) *httptest.ResponseRecorder {
					rec := httptest.NewRecorder()
					srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(payload)))
					return rec
				}
				if _, err := st.AddObservation(store.AddObservationParams{
					SessionID: "capture", Project: "example", Type: "manual", Title: "title", Content: "content",
				}); err != nil {
					t.Fatal(err)
				}
				beforeObservations, err := st.RecentObservations("example", "project", 10)
				if err != nil {
					t.Fatal(err)
				}
				beforeStats := request("GET", "/stats?all_projects=true", "").Body.String()
				beforeMutations, err := st.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
				if err != nil {
					t.Fatal(err)
				}
				rec := request("POST", "/"+endpoint, `{"id":"rejected","session_id":"capture","project":"example","type":"manual","title":"title","content":"content","project_provenance":`+metadata+`}`)
				if rec.Code != 400 {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				afterStats := request("GET", "/stats?all_projects=true", "").Body.String()
				afterMutations, err := st.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
				if err != nil {
					t.Fatal(err)
				}
				afterObservations, err := st.RecentObservations("example", "project", 10)
				if err != nil {
					t.Fatal(err)
				}
				if beforeStats != afterStats || !reflect.DeepEqual(beforeMutations, afterMutations) || !reflect.DeepEqual(beforeObservations, afterObservations) {
					t.Fatal("rejected metadata changed counts, records or queued mutations")
				}
			})
		}
	}
}

func TestCaptureProjectProvenanceSessionCompatibility(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSessionWithOwnershipMode("legacy", "example", "", store.SessionOwnershipProjectOwned); err != nil {
		t.Fatal(err)
	}
	if err := st.EnrollProject("example"); err != nil {
		t.Fatal(err)
	}
	srv := New(st, 0)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/sessions", strings.NewReader(`{"id":"legacy","project":"example","ownership_mode":"project_owned","project_provenance":{"source":"explicit_override"}}`)))
	if rec.Code != 201 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	p, err := st.CaptureProjectProvenance("session", "legacy")
	if err != nil || p != nil {
		t.Fatalf("old session relabeled: %+v, %v", p, err)
	}
	before, err := st.GetSession("legacy")
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/sessions", strings.NewReader(`{"id":"legacy","project":"foreign","ownership_mode":"project_owned","project_provenance":{"source":"explicit_override"}}`)))
	if rec.Code != 409 {
		t.Fatalf("conflict=%d body=%s", rec.Code, rec.Body.String())
	}
	after, err := st.GetSession("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("conflict changed session")
	}
}

func TestCaptureProjectProvenanceSessionReadback(t *testing.T) {
	for _, source := range []string{"explicit_override", "git_root", "dir_basename", "unknown", ""} {
		t.Run(source, func(t *testing.T) {
			st := newServerTestStore(t)
			srv := New(st, 0)
			payload := `{"id":"evidence","project":"example","directory":"/workspace","project_provenance":{"source":"` + source + `","directory":"/original","raw_project":"Original Name","future_field":true}}`
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/sessions", strings.NewReader(payload)))
			if rec.Code != 201 {
				t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
			}
			rec = httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/sessions/evidence", nil))
			if rec.Code != 200 {
				t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
			}
			var body struct {
				ProjectProvenance struct {
					Source     string `json:"source"`
					Directory  string `json:"directory"`
					RawProject string `json:"raw_project"`
				} `json:"project_provenance"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			want := source
			if want == "" {
				want = "unknown"
			}
			if body.ProjectProvenance.Source != want || body.ProjectProvenance.Directory != "/original" || body.ProjectProvenance.RawProject != "Original Name" {
				t.Fatalf("evidence = %+v", body.ProjectProvenance)
			}
		})
	}
}

func TestCaptureProjectProvenanceInvalidSessionDoesNotRegister(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/sessions", strings.NewReader(`{"id":"invalid-evidence","project":"example","directory":"/workspace","project_provenance":{"source":"invented"}}`)))
	if rec.Code != 400 {
		t.Fatalf("status = %d, body = %s; want 400", rec.Code, rec.Body.String())
	}
	session, err := st.GetSession("invalid-evidence")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if session != nil {
		t.Fatal("invalid metadata registered a session")
	}
}
