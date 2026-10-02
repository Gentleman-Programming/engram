package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func TestRuntimeSessionResolveAndEndNeverCreate(t *testing.T) {
	root := t.TempDir()
	db, err := store.New(store.FallbackConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler := New(db, 0).Handler()
	call := func(route, id, project, directory string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"id": id, "project": project, "directory": directory, "ownership_mode": "project_owned"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, route, bytes.NewReader(body)))
		return w
	}
	if err := db.CreateSessionWithOwnershipMode("host", "project", root, store.SessionOwnershipProjectOwned); err != nil {
		t.Fatal(err)
	}
	if err := db.EndSession("host", "root summary"); err != nil {
		t.Fatal(err)
	}
	effective, err := db.ResumeSessionWithOwnershipMode("host", "project", root, store.SessionOwnershipProjectOwned)
	if err != nil {
		t.Fatal(err)
	}
	w := call("/runtime-sessions/resolve", "host", "project", root)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve status = %d: %s", w.Code, w.Body.String())
	}
	var ack map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil || ack["id"] != effective || ack["resumed_from"] != "host" {
		t.Fatalf("resolve ack = %s, %v", w.Body.String(), err)
	}
	for _, route := range []string{"/runtime-sessions/resolve", "/runtime-sessions/end"} {
		for _, context := range []struct{ project, directory string }{{"foreign", root}, {"project", filepath.Join(root, "other")}} {
			if w := call(route, "host", context.project, context.directory); w.Code != http.StatusConflict {
				t.Fatalf("scope conflict %s = %d: %s", route, w.Code, w.Body.String())
			}
		}
	}
	if w := call("/runtime-sessions/end", "host", "project", root); w.Code != http.StatusOK {
		t.Fatalf("end = %d: %s", w.Code, w.Body.String())
	}
	for _, route := range []string{"/runtime-sessions/resolve", "/runtime-sessions/end"} {
		for _, id := range []string{"host", "missing"} {
			if w := call(route, id, "project", root); w.Code != http.StatusNotFound {
				t.Fatalf("no live identity %s %s = %d", route, id, w.Code)
			}
		}
	}
	for _, id := range []string{"missing", "host:resume:3"} {
		if _, err := db.GetSession(id); err == nil {
			t.Fatalf("created %s", id)
		}
	}
	for _, id := range []string{"host", effective} {
		session, err := db.GetSession(id)
		if err != nil || session.EndedAt == nil {
			t.Fatalf("terminal %s = %+v, %v", id, session, err)
		}
	}
	for _, body := range []string{`{}`, `{"id":"host","project":"project","directory":"x","ownership_mode":"bogus"}`, `{"id":"host","project":"project","directory":"x","extra":true}`, `{} {}`} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/runtime-sessions/resolve", bytes.NewBufferString(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid body %s = %d", body, w.Code)
		}
	}
}
