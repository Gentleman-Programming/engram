package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func TestHookLocalIdentityMatchesServeHomeFallback(t *testing.T) {
	for _, mode := range []string{"fallback", "override", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("ENGRAM_DATA_DIR", "")
			t.Setenv("USERPROFILE", "")
			t.Setenv("HOME", home)
			t.Setenv("LOCALAPPDATA", "")
			oldConfig := storeDefaultConfig
			storeDefaultConfig = func() (store.Config, error) { return store.Config{}, errors.New("home unavailable") }
			t.Cleanup(func() { storeDefaultConfig = oldConfig })
			dir := filepath.Join(home, ".engram")
			if mode == "override" {
				dir = filepath.Join(home, "explicit override")
				t.Setenv("ENGRAM_DATA_DIR", dir)
			}
			if mode == "unavailable" {
				t.Setenv("HOME", "")
			}
			expected, err := store.EnsureInstanceID(dir)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "engram", "instance_id": expected})
			}))
			defer server.Close()
			err = confirmHookLocalIdentity(context.Background(), server.Client(), server.URL, "windows")
			if (err == nil) != (mode != "unavailable") {
				t.Fatalf("identity confirmation in %s: %v", mode, err)
			}
		})
	}
}

func TestHookLocalIdentityReleaseGuidance(t *testing.T) {
	previousVersion := version
	version = "3.7.2"
	t.Cleanup(func() { version = previousVersion })
	previousLaunch := hookLocalServerLaunch
	hookLocalServerLaunch = func(context.Context, string, string, []string) error {
		t.Error("a received health response must never trigger a launch")
		return errors.New("unexpected launch")
	}
	t.Cleanup(func() { hookLocalServerLaunch = previousLaunch })
	t.Setenv("ENGRAM_URL", "")
	t.Setenv("ENGRAM_SOCKET", "")
	dir := t.TempDir()
	t.Setenv("ENGRAM_DATA_DIR", dir)
	expected, err := store.EnsureInstanceID(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, service, identity string
		reported                any
		httpStatus              int
		wantGuidance, confirmed bool
	}{
		{name: "legacy listener", service: "engram", reported: "0.1.0", httpStatus: http.StatusOK, wantGuidance: true},
		{name: "foreign instance", service: "engram", identity: "other-instance", reported: "v0.1.0+build", httpStatus: http.StatusOK, wantGuidance: true},
		{name: "same release", service: "engram", reported: "v3.7.2+different", httpStatus: http.StatusOK},
		{name: "invalid release", service: "engram", reported: "private\nserver release", httpStatus: http.StatusOK},
		{name: "missing release", service: "engram", httpStatus: http.StatusOK},
		{name: "overlong release", service: "engram", reported: "0.1.0-" + strings.Repeat("a", 64), httpStatus: http.StatusOK},
		{name: "unrelated listener", service: "other", reported: "0.1.0", httpStatus: http.StatusOK},
		{name: "unhealthy listener", service: "engram", reported: "0.1.0", httpStatus: http.StatusInternalServerError},
		{name: "matching with different release", service: "engram", identity: expected, reported: "0.1.0", httpStatus: http.StatusOK, confirmed: true},
		{name: "matching with nonstring release", service: "engram", identity: expected, reported: 1, httpStatus: http.StatusOK, confirmed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case "/health":
					w.WriteHeader(tc.httpStatus)
					body := map[string]any{"status": "ok", "service": tc.service, "version": tc.reported}
					if tc.identity != "" {
						body["instance_id"] = tc.identity
					}
					_ = json.NewEncoder(w).Encode(body)
				case "/sessions/private-session":
					_, _ = w.Write([]byte(`{"id":"private-session","project":"owner"}`))
				case "/sessions":
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"private-session","status":"created"}`))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Setenv("ENGRAM_PORT", strings.TrimPrefix(listener.URL, "http://127.0.0.1:"))
			id, err := confirmHookSessionForPlatform("private-session", "private-cwd", true, "windows")
			listener.Close()
			if tc.confirmed {
				if err != nil || id != "private-session" {
					t.Fatalf("matching instance = %q, %v", id, err)
				}
				return
			}
			const generic = "managed local server identity unconfirmed: expected current data directory instance"
			if id != "" || err == nil || !strings.HasPrefix(err.Error(), generic) {
				t.Fatalf("identity denial = %q, %v", id, err)
			}
			if got := strings.Join(paths, ","); got != "/health" {
				t.Fatalf("requests before denial = %s, want only health", got)
			}
			if tc.wantGuidance {
				for _, detail := range []string{"server 0.1.0", "installed CLI 3.7.2", "restart", "existing supervisor"} {
					if !strings.Contains(err.Error(), detail) {
						t.Errorf("missing %q in denial: %v", detail, err)
					}
				}
			} else if err.Error() != generic {
				t.Errorf("expected generic denial, got %v", err)
			}
		})
	}
}
