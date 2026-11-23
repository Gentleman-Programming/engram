package plugin_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestClaudeLifecycleConfirmedContext(t *testing.T) {
	requireHookBinaries(t)
	binary := bashScriptPath(t, buildEngramBinary(t))
	for _, script := range []string{"session-start.sh", "post-compaction.sh"} {
		for _, mode := range []string{"full", "slim"} {
			t.Run(script+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				db, err := store.New(store.FallbackConfig(filepath.Join(root, "data")))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				const host = "context-resume-host"
				if err := db.StartSessionWithOwnershipMode(host, "persisted-owner", root, store.SessionOwnershipProjectOwned); err != nil {
					t.Fatal(err)
				}
				if err := db.EndSession(host, "finished"); err != nil {
					t.Fatal(err)
				}
				production := server.New(db, 0).Handler()
				var registrations atomic.Int32
				endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-Fixture-Authentication") != "curl" {
						http.Error(w, "external server requires curl authentication", http.StatusUnauthorized)
						return
					}
					if r.Method == http.MethodPost && r.URL.Path == "/sessions" {
						registrations.Add(1)
					}
					switch r.URL.Path {
					case "/project/current":
						_, _ = io.WriteString(w, `{"project":"new-cwd-owner","project_source":"config"}`)
					case "/context":
						if r.URL.Query().Get("project") != "persisted-owner" {
							t.Error("context lost independent persisted project")
						}
						_, _ = io.WriteString(w, `{"context":"synthetic history"}`)
					case "/sessions/" + host:
						if registrations.Load() != 0 {
							http.Error(w, "session lookup unavailable after registration", http.StatusServiceUnavailable)
							return
						}
						production.ServeHTTP(w, r)
					default:
						production.ServeHTTP(w, r)
					}
				}))
				defer endpoint.Close()
				stubDir := t.TempDir()
				wrapper := "#!/bin/bash\nif [ \"$*\" = \"protocol-mode claude-code\" ]; then printf '%s\\n' '" + mode + "'; exit 0; fi\nexec " + codexIntentQuote(binary) + " \"$@\"\n"
				if err := os.WriteFile(filepath.Join(stubDir, "engram"), []byte(wrapper), 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{".curlrc", "_curlrc"} {
					if err := os.WriteFile(filepath.Join(root, name), []byte("header = \"X-Fixture-Authentication: curl\"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				input, _ := json.Marshal(map[string]string{"session_id": host, "cwd": root, "source": "resume"})
				output, diagnostic := runHookWithStderr(t, script, string(input), map[string]string{
					"ENGRAM_URL": endpoint.URL, "ENGRAM_DATA_DIR": filepath.Join(root, "data"),
					"PATH": stubDir + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME": root, "USERPROFILE": root, "CURL_HOME": root,
				})
				if got := registrations.Load(); got != 1 {
					t.Fatalf("lifecycle made %d authenticated registration requests, want one: %q", got, diagnostic)
				}
				if !strings.Contains(output, `Confirmed Engram session ID (JSON): "context-resume-host:resume:2"`) {
					t.Fatalf("missing effective continuation in %s context: %q", mode, output)
				}
				if !strings.Contains(output, "synthetic history") {
					t.Fatalf("history missing: %q", output)
				}
				if strings.Index(output, "Confirmed Engram session ID") > strings.Index(output, "synthetic history") {
					t.Fatal("identity must precede optional history")
				}
				if strings.Contains(output, "ACTIVE PROTOCOL") != (mode == "full") {
					t.Fatalf("protocol mode changed: %q", output)
				}
				if script == "post-compaction.sh" && !strings.Contains(output, "FIRST: Call mem_session_summary") {
					t.Fatal("compaction recovery instruction missing")
				}
			})
		}
	}
}
