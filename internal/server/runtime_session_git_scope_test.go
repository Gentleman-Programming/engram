package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestRuntimeResolveKeepsLinkedWorktreeScopesDistinct(t *testing.T) {
	primary, nested := httpHomeSelectionFixture(t)
	httpHomeGit(t, primary, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	linked := filepath.Join(t.TempDir(), "linked checkout")
	httpHomeGit(t, primary, "worktree", "add", "--detach", linked, "HEAD")

	const root, project = "codex-linked-host", "fixture-project"
	mode := store.SessionOwnershipProjectOwned
	db := newServerTestStore(t)
	if err := db.StartSessionWithOwnershipMode(root, project, primary, mode); err != nil {
		t.Fatal(err)
	}
	if err := db.EndSession(root, "fixture ended"); err != nil {
		t.Fatal(err)
	}
	effective, err := db.ResumeSessionWithOwnershipMode(root, project, primary, mode)
	if err != nil {
		t.Fatal(err)
	}
	setDirectory := func(dir string) {
		t.Helper()
		if _, err := db.DB().Exec("UPDATE sessions SET directory = ? WHERE id = ?", dir, effective); err != nil {
			t.Fatal(err)
		}
	}
	setDirectory(nested)
	handler := New(db, 0).Handler()
	resolve := func(dir string, status int) {
		t.Helper()
		body, err := json.Marshal(map[string]string{"id": root, "project": project, "directory": dir, "ownership_mode": mode})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/runtime-sessions/resolve", bytes.NewReader(body)))
		if w.Code != status {
			t.Fatalf("resolve %s = %d, want %d: %s", dir, w.Code, status, w.Body)
		}
	}
	resolve(nested, http.StatusOK)
	resolve(linked, http.StatusConflict)
	setDirectory(linked)
	resolve(primary, http.StatusConflict)
	if err := db.EndSession(effective, "fixture ended"); err != nil {
		t.Fatal(err)
	}
	resolve(linked, http.StatusConflict)
	resolve(primary, http.StatusNotFound)
}

func TestRuntimeResolveChecksEachDirectoryAfterReleasingStoreConnection(t *testing.T) {
	t.Run("live continuation", func(t *testing.T) { checkRuntimeResolveGitScope(t, false, false) })
	t.Run("continuation ended during Git", func(t *testing.T) { checkRuntimeResolveGitScope(t, true, false) })
	t.Run("Git fallback can recover", func(t *testing.T) { checkRuntimeResolveGitScope(t, false, true) })
}

func checkRuntimeResolveGitScope(t *testing.T, endWhileChecking, failFirstScopeCheck bool) {
	t.Helper()
	const root, project = "codex-scope-host", "fixture-project"
	mode := store.SessionOwnershipProjectOwned
	db := newServerTestStore(t)
	dir := projectpkg.DirectoryIdentity(t.TempDir())
	if err := db.StartSessionWithOwnershipMode(root, project, dir, mode); err != nil {
		t.Fatal(err)
	}
	if err := db.EndSession(root, "fixture ended"); err != nil {
		t.Fatal(err)
	}
	effective, err := db.ResumeSessionWithOwnershipMode(root, project, dir, mode)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	installCurrentProjectGitFixture(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ENGRAM_HTTP_TOKEN", "")
	t.Setenv(currentProjectGitEchoDirectory, "1")
	gate, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Close() }()
	t.Setenv(currentProjectGitFixture, gate.Addr().String())

	secondGit := make(chan struct{})
	releaseGit := make(chan struct{})
	calls := make(chan int, 1)
	go func() {
		count := 0
		for {
			conn, err := gate.AcceptTCP()
			if err != nil {
				calls <- count
				return
			}
			count++
			if count == 2 {
				close(secondGit)
				<-releaseGit
			}
			if count != 2 || !failFirstScopeCheck {
				_, _ = conn.Write([]byte{1})
			}
			_ = conn.Close()
		}
	}()

	body, err := json.Marshal(map[string]string{"id": root, "project": project, "directory": dir, "ownership_mode": mode})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	srv := New(db, 0)
	go func() {
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodPost, "/runtime-sessions/resolve", bytes.NewReader(body)))
		done <- w
	}()

	select {
	case <-secondGit:
		readCtx, stop := context.WithTimeout(ctx, 350*time.Millisecond)
		var value int
		err = db.DB().QueryRowContext(readCtx, "SELECT 1").Scan(&value)
		stop()
		if err != nil || value != 1 {
			t.Errorf("Git held the store connection: value=%d error=%v", value, err)
		}
		if endWhileChecking && err == nil {
			if err := db.EndSession(effective, "ended during Git"); err != nil {
				t.Error(err)
			}
		}
	case <-ctx.Done():
		t.Error("filesystem scope confirmation did not start")
	}
	close(releaseGit)

	w := <-done
	_ = gate.Close()
	wantCalls := 2
	if failFirstScopeCheck {
		wantCalls++
	}
	if count := <-calls; count != wantCalls {
		t.Errorf("resolve started Git %d times for one distinct worktree, want %d including HTTP preparation", count, wantCalls)
	}
	if endWhileChecking {
		if w.Code != http.StatusNotFound {
			t.Fatalf("resolve used ended snapshot: %d %s", w.Code, w.Body)
		}
		return
	}
	var ack map[string]string
	want := map[string]string{"id": effective, "status": "resolved", "resumed_from": root}
	if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil || w.Code != http.StatusOK || !reflect.DeepEqual(want, ack) {
		t.Fatalf("resolve = %d, %s, %v", w.Code, w.Body, err)
	}
}
