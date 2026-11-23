package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestRuntimeSessionResolveReusesExactStoredDirectory(t *testing.T) {
	_, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git is required to observe worktree lookups")
	}
	directory, subdirectory := httpHomeSelectionFixture(t)
	directory = projectpkg.DirectoryIdentity(directory)

	db := newServerTestStore(t)
	const root, project, mode = "registered-root", "fixture-project", store.SessionOwnershipProjectOwned
	if err := db.CreateSessionWithOwnershipMode(root, project, directory, mode); err != nil {
		t.Fatal(err)
	}
	if err := db.EndSession(root, "terminal fixture"); err != nil {
		t.Fatal(err)
	}
	effective, err := db.ResumeSessionWithOwnershipMode(root, project, directory, mode)
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "git-trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	handler := New(db, 0).Handler()
	call := func(owner, requested, expected string, status int) {
		t.Helper()
		body, err := json.Marshal(map[string]string{"id": root, "project": owner, "directory": requested, "ownership_mode": mode})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/runtime-sessions/resolve", bytes.NewReader(body)))
		if w.Code != status {
			t.Fatalf("resolve %q = %d %s, want %d", requested, w.Code, w.Body, status)
		}
		if expected != "" {
			var ack map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil || ack["id"] != expected ||
				ack["resumed_from"] != root || ack["status"] != "resolved" {
				t.Fatalf("resolve ack = %s, %v", w.Body, err)
			}
		}
	}
	call(project, directory, effective, http.StatusOK)
	if records, err := os.ReadFile(trace); err != nil || strings.Count(string(records), `"event":"start"`) != 1 {
		t.Fatalf("an exact persisted scope must use one Git lookup: %v\n%s", err, records)
	}
	call("other-project", directory, "", http.StatusConflict)
	if err := os.Remove(trace); err != nil {
		t.Fatal(err)
	}

	call(project, subdirectory, effective, http.StatusOK)
	if records, err := os.ReadFile(trace); err != nil || !strings.Contains(string(records), "--show-toplevel") {
		t.Fatalf("the worktree alias did not consult Git: %v\n%s", err, records)
	}
	if runtime.GOOS == "windows" {
		call(project, strings.ToUpper(directory), effective, http.StatusOK)
	}
	// A new binding for the live continuation cannot reuse a prior result.
	if _, err := db.DB().Exec("UPDATE sessions SET directory = ? WHERE id = ?", t.TempDir(), effective); err != nil {
		t.Fatal(err)
	}
	call(project, directory, "", http.StatusConflict)

	if _, err := db.DB().Exec("UPDATE sessions SET directory = ? WHERE id = ?", directory, effective); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	nested := filepath.Join(other, "other-nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	httpHomeGit(t, other, "init", "--quiet")
	httpHomeGit(t, directory, "config", "--local", "core.worktree", nested)
	// Git maps directory to nested, then maps nested to the other checkout's
	// root. Equal strings in SQLite must not bypass this scope conflict.
	call(project, directory, "", http.StatusConflict)
	httpHomeGit(t, directory, "config", "--local", "--unset", "core.worktree")

	shift := "shift"
	if err := os.MkdirAll(filepath.Join(directory, shift, shift), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(directory, ".git"))
	t.Setenv("GIT_WORK_TREE", shift)
	call(project, directory, "", http.StatusConflict)
}

func TestRuntimeSessionResolveReadsWhileWriterIsReserved(t *testing.T) {
	directory := t.TempDir()
	cfg := store.FallbackConfig(filepath.Join(directory, "store"))
	db, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const root, project = "codex-host", "project"
	const mode = store.SessionOwnershipProjectOwned
	if err := db.CreateSessionWithOwnershipMode(root, project, directory, mode); err != nil {
		t.Fatal(err)
	}
	if err := db.EndSession(root, "root summary"); err != nil {
		t.Fatal(err)
	}
	effective, err := db.ResumeSessionWithOwnershipMode(root, project, directory, mode)
	if err != nil {
		t.Fatal(err)
	}

	other, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	writer, err := other.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	if _, err := writer.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = writer.ExecContext(context.Background(), "ROLLBACK") }()

	body, err := json.Marshal(map[string]string{"id": root, "project": project, "directory": directory, "ownership_mode": mode})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(db, 0).Handler()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/runtime-sessions/resolve", bytes.NewReader(body)))
		done <- w
	}()
	select {
	case w := <-done:
		var ack map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil || w.Code != http.StatusOK ||
			ack["id"] != effective || ack["resumed_from"] != root || ack["status"] != "resolved" {
			t.Fatalf("resolve = %d %s, %v", w.Code, w.Body, err)
		}
	case <-time.After(3 * time.Second):
		if _, err := writer.ExecContext(t.Context(), "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("resolve did not finish after writer release")
		}
		t.Fatal("read-only resolution waited for the other connection's writer reservation")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	canceled := httptest.NewRecorder()
	handler.ServeHTTP(canceled, httptest.NewRequestWithContext(ctx, http.MethodPost, "/runtime-sessions/resolve", bytes.NewReader(body)))
	if canceled.Body.Len() != 0 {
		t.Fatalf("canceled resolve returned a response: %s", canceled.Body)
	}
}

func TestRuntimeSessionEndRequiresExpectedEffectiveIDWhenProvided(t *testing.T) {
	db := newServerTestStore(t)
	srv := New(db, 0)
	writes := 0
	srv.SetOnWrite(func() { writes++ })
	handler := srv.Handler()
	directory := t.TempDir()
	const root = "claude-host"
	const project = "project"
	const mode = store.SessionOwnershipProjectOwned
	if err := db.EnrollProject(project); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSessionWithOwnershipMode(root, project, directory, mode); err != nil {
		t.Fatal(err)
	}

	call := func(route, owner, expected string, status int) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]string{
			"id": root, "project": owner, "directory": directory,
			"ownership_mode": mode, "summary": "confirmed end",
		})
		if err != nil {
			t.Fatal(err)
		}
		body = append(body[:len(body)-1], expected...)
		body = append(body, '}')
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, route, bytes.NewReader(body)))
		if w.Code != status {
			t.Fatalf("%s with %s = %d: %s, want %d", route, expected, w.Code, w.Body, status)
		}
		return w
	}
	session := func(id string) *store.Session {
		t.Helper()
		got, err := db.GetSession(id)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	pending := func() []store.SyncMutation {
		t.Helper()
		rows, err := db.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	end := "/runtime-sessions/end"
	expectedRoot := `,"expected_effective_id":"claude-host"`
	beforeRoot, beforePending := session(root), pending()
	for _, invalid := range []string{
		`,"expected_effective_id":null`, `,"expected_effective_id":false`,
		`,"expected_effective_id":""`, `,"expected_effective_id":" \t"`,
		expectedRoot + expectedRoot, expectedRoot + `,"EXPECTED_EFFECTIVE_ID":"other"`,
	} {
		call(end, project, invalid, http.StatusBadRequest)
	}
	call("/runtime-sessions/resolve", project, expectedRoot, http.StatusBadRequest)
	call(end, "foreign", expectedRoot, http.StatusConflict)
	call(end, project, `,"expected_effective_id":"other"`, http.StatusConflict)
	if !reflect.DeepEqual(beforeRoot, session(root)) || !reflect.DeepEqual(beforePending, pending()) || writes != 0 {
		t.Fatal("rejected end changed the root, sync queue, or write notifications")
	}

	w := call(end, project, expectedRoot, http.StatusOK)
	var ack map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil || ack["id"] != root || ack["status"] != "ended" {
		t.Fatalf("end ack = %s, %v", w.Body, err)
	}
	afterRoot, afterPending := session(root), pending()
	if afterRoot.EndedAt == nil || afterRoot.Summary == nil || *afterRoot.Summary != "confirmed end" ||
		reflect.DeepEqual(beforePending, afterPending) || writes != 1 {
		t.Fatalf("valid end = %+v, notifications = %d, sync changed = %t", afterRoot, writes, !reflect.DeepEqual(beforePending, afterPending))
	}
	call(end, project, expectedRoot, http.StatusNotFound)
	if !reflect.DeepEqual(afterRoot, session(root)) || !reflect.DeepEqual(afterPending, pending()) || writes != 1 {
		t.Fatal("repeated end changed the root, sync queue, or write notifications")
	}

	effective, err := db.ResumeSessionWithOwnershipMode(root, project, directory, mode)
	if err != nil || effective == root {
		t.Fatalf("resume = %q, %v", effective, err)
	}
	beforeContinuation, beforePending := session(effective), pending()
	call(end, project, expectedRoot, http.StatusConflict)
	if !reflect.DeepEqual(afterRoot, session(root)) || !reflect.DeepEqual(beforeContinuation, session(effective)) ||
		!reflect.DeepEqual(beforePending, pending()) || writes != 1 {
		t.Fatal("delayed end changed the continuation, root, sync queue, or write notifications")
	}
	expectedContinuation, err := json.Marshal(effective)
	if err != nil {
		t.Fatal(err)
	}
	w = call(end, project, `,"expected_effective_id":`+string(expectedContinuation), http.StatusOK)
	ack = nil
	if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil || ack["id"] != effective || ack["resumed_from"] != root || ack["status"] != "ended" {
		t.Fatalf("continuation end ack = %s, %v", w.Body, err)
	}
	if session(effective).EndedAt == nil || writes != 2 {
		t.Fatal("valid continuation end did not finalize the continuation")
	}
}

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
