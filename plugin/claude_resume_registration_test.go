package plugin_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestClaudeSessionLookupFailureDoesNotGuessOwner(t *testing.T) {
	requireHookBinaries(t)
	for _, script := range []string{"session-start.sh", "post-compaction.sh"} {
		for _, failure := range []string{"transient-503", "transient-disconnect", "persistent-503", "forbidden", "missing"} {
			t.Run(script+"/"+failure, func(t *testing.T) {
				db, err := store.New(store.FallbackConfig(t.TempDir()))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				const host = "lookup-resume-host"
				if failure != "missing" {
					if err := db.StartSessionWithOwnershipMode(host, "persisted-owner", "/synthetic", store.SessionOwnershipProjectOwned); err != nil {
						t.Fatal(err)
					}
					if err := db.EndSession(host, "finished"); err != nil {
						t.Fatal(err)
					}
				}
				production := server.New(db, 0).Handler()
				var lookups, posts atomic.Int64
				endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/project/current" {
						_, _ = io.WriteString(w, `{"project":"new-cwd-owner","project_source":"git_remote"}`)
						return
					}
					if r.URL.Path == "/sessions/"+host {
						n := lookups.Add(1)
						if failure == "forbidden" {
							w.WriteHeader(http.StatusForbidden)
							return
						}
						if failure == "persistent-503" || failure == "transient-503" && n == 1 {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
						if failure == "transient-disconnect" && n == 1 {
							conn, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							_ = conn.Close()
							return
						}
					}
					if r.URL.Path == "/sessions" && r.Method == http.MethodPost {
						posts.Add(1)
					}
					production.ServeHTTP(w, r)
				}))
				defer endpoint.Close()
				stubDir := t.TempDir()
				if err := os.WriteFile(filepath.Join(stubDir, "engram"), []byte("#!/bin/bash\nexit 0\n"), 0755); err != nil {
					t.Fatal(err)
				}
				_, stderr := runHookWithStderr(t, script, `{"session_id":"`+host+`","cwd":`+strconv.Quote(t.TempDir())+`}`, map[string]string{"ENGRAM_URL": endpoint.URL, "PATH": stubDir + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME": t.TempDir()})
				wantLookups, wantPosts := int64(2), int64(1)
				if failure == "missing" {
					wantLookups = 1
				}
				if failure == "persistent-503" || failure == "forbidden" {
					wantPosts = 0
					if failure == "forbidden" {
						wantLookups = 1
					}
					if !strings.Contains(stderr, "session lookup failed") {
						t.Fatalf("lookup failure silent: %q", stderr)
					}
				} else {
					id, project := host+":resume:2", "persisted-owner"
					if failure == "missing" {
						id, project = host, "new-cwd-owner"
					}
					row, err := db.GetSession(id)
					if err != nil || row.Project != project || row.EndedAt != nil {
						t.Fatalf("registered owner: %+v %v", row, err)
					}
				}
				if lookups.Load() != wantLookups || posts.Load() != wantPosts {
					t.Fatalf("lookups/posts=%d/%d want=%d/%d", lookups.Load(), posts.Load(), wantLookups, wantPosts)
				}
			})
		}
	}
}

func TestClaudeResumeRetainsPersistedProject(t *testing.T) {
	requireHookBinaries(t)
	for _, script := range []string{"session-start.sh", "post-compaction.sh"} {
		for _, mode := range []string{store.SessionOwnershipProjectOwned, store.SessionOwnershipShared} {
			t.Run(script+"/"+mode, func(t *testing.T) {
				db, err := store.New(store.FallbackConfig(t.TempDir()))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				const host = "resume-project-drift"
				if err := db.StartSessionWithOwnershipMode(host, "persisted-owner", "/synthetic/previous", mode); err != nil {
					t.Fatal(err)
				}
				if err := db.EndSession(host, "finished"); err != nil {
					t.Fatal(err)
				}
				production := server.New(db, 0).Handler()
				endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/project/current" {
						_, _ = io.WriteString(w, `{"project":"new-cwd-owner","project_source":"git_remote"}`)
						return
					}
					production.ServeHTTP(w, r)
				}))
				defer endpoint.Close()
				stubDir := t.TempDir()
				if err := os.WriteFile(filepath.Join(stubDir, "engram"), []byte("#!/bin/bash\nexit 0\n"), 0755); err != nil {
					t.Fatal(err)
				}
				env := map[string]string{"ENGRAM_URL": endpoint.URL, "PATH": stubDir + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME": t.TempDir()}
				input := `{"session_id":"` + host + `","source":"resume","cwd":` + strconv.Quote(t.TempDir()) + `}`
				for range 2 {
					runHook(t, script, input, env)
				}
				root, err := db.GetSession(host)
				if err != nil || root.EndedAt == nil || root.Project != "persisted-owner" {
					t.Fatalf("root changed: %+v %v", root, err)
				}
				continuation, err := db.GetSession(host + ":resume:2")
				if err != nil || continuation.Project != "persisted-owner" || continuation.OwnershipMode != mode || continuation.EndedAt != nil {
					t.Fatalf("resume did not preserve authoritative owner: %+v %v", continuation, err)
				}
				if _, err := db.GetSession(host + ":resume:3"); err == nil {
					t.Fatal("repeated start created another continuation")
				}
			})
		}
	}
}

func TestClaudeRegistrationFailureIsVisible(t *testing.T) {
	requireHookBinaries(t)
	for _, script := range []string{"session-start.sh", "post-compaction.sh"} {
		for _, status := range []int{http.StatusConflict, http.StatusServiceUnavailable, 0} {
			t.Run(script+"/"+strconv.Itoa(status), func(t *testing.T) {
				endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/project/current" {
						_, _ = io.WriteString(w, `{"project":"demo","project_source":"config"}`)
						return
					}
					if r.URL.Path == "/sessions" && r.Method == http.MethodPost {
						if status == 0 {
							conn, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							_ = conn.Close()
							return
						}
						w.WriteHeader(status)
						_ = json.NewEncoder(w).Encode(map[string]string{"code": "session_project_conflict", "error": "private-response-do-not-log"})
						return
					}
					http.NotFound(w, r)
				}))
				defer endpoint.Close()
				stubDir := t.TempDir()
				if err := os.WriteFile(filepath.Join(stubDir, "engram"), []byte("#!/bin/bash\nexit 0\n"), 0755); err != nil {
					t.Fatal(err)
				}
				out, stderr := runHookWithStderr(t, script, `{"session_id":"private-host-do-not-log","cwd":`+strconv.Quote(t.TempDir())+`}`, map[string]string{"ENGRAM_URL": endpoint.URL, "PATH": stubDir + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME": t.TempDir()})
				if !strings.Contains(stderr, "Engram session registration failed") {
					t.Fatalf("registration failure was silent: %q", stderr)
				}
				for _, private := range []string{"private-response-do-not-log", "private-host-do-not-log"} {
					if strings.Contains(out+stderr, private) {
						t.Fatalf("registration diagnostic exposed %s", private)
					}
				}
			})
		}
	}
}
