package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestHookRejectsForeignLocalIdentityBeforeSessionTraffic(t *testing.T) {
	for _, identity := range []string{"foreign", "", "matching", "redirect", "malformed", "unhealthy", "trailing", "oversized", "whitespace"} {
		t.Run(identity, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("ENGRAM_DATA_DIR", dir)
			t.Setenv("ENGRAM_URL", "")
			t.Setenv("ENGRAM_SOCKET", "")
			expected, err := store.EnsureInstanceID(dir)
			if err != nil {
				t.Fatal(err)
			}
			served := identity
			if identity == "matching" || identity == "trailing" || identity == "oversized" || identity == "whitespace" {
				served = expected
			}
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case "/health":
					if identity == "redirect" {
						http.Redirect(w, r, "/private-redirect", http.StatusFound)
						return
					}
					if identity == "malformed" {
						_, _ = w.Write([]byte("not json"))
						return
					}
					if identity == "unhealthy" {
						w.WriteHeader(http.StatusInternalServerError)
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "engram", "instance_id": served})
					switch identity {
					case "trailing":
						_, _ = w.Write([]byte("not json"))
					case "oversized":
						_, _ = w.Write([]byte(strings.Repeat(" ", hookDenialBodyLimit)))
					case "whitespace":
						_, _ = w.Write([]byte(" \t\r\n"))
					}
				case "/sessions/private-session":
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "private-session", "project": "owner"})
				case "/sessions":
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"private-session","status":"created"}`))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			t.Setenv("ENGRAM_PORT", strings.TrimPrefix(server.URL, "http://127.0.0.1:"))
			id, err := confirmHookSessionForPlatform("private-session", "private-cwd", true, "linux")
			// Close waits for all handlers, synchronizing their path writes before
			// assertions read the capture, including on a failed confirmation.
			server.Close()
			if identity == "matching" || identity == "whitespace" {
				if err != nil || id != "private-session" {
					t.Fatalf("matching instance: %q %v", id, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "identity") || id != "" {
					t.Errorf("foreign instance accepted: %q %v", id, err)
				}
				if len(paths) != 1 || paths[0] != "/health" {
					t.Errorf("private traffic before identity validation: %v", paths)
				}
			}
		})
	}
}
