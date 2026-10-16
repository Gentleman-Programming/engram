package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCodexGuardCoreCompatibility(t *testing.T) {
	const supported = `{"status":"ok","service":"engram","capabilities":{"runtime_session_resolution":true}}`
	for _, tc := range []struct {
		name, health, resolution, reason      string
		healthStatus, resolveStatus, requests int
	}{
		{"old", `{"status":"ok","service":"engram"}`, "", "Upgrade", 200, 200, 0},
		{"resume-only", `{"status":"ok","service":"engram","capabilities":{"root_session_resume":true}}`, "", "Upgrade", 200, 200, 0},
		{"disabled", `{"status":"ok","service":"engram","capabilities":{"runtime_session_resolution":false}}`, "", "Upgrade", 200, 200, 0},
		{"malformed", `{`, "", "health response", 200, 200, 0},
		{"trailing-health", supported + `{}`, "", "health response", 200, 200, 0},
		{"wrong-service", `{"status":"ok","service":"other"}`, "", "health response", 200, 200, 0},
		{"health-error", supported, "", "health response", 503, 200, 0},
		{"missing-route", supported, `404 page not found`, "Upgrade", 200, 404, 1},
		{"unregistered", supported, `{"error":"sql: no rows in result set"}`, "not registered", 200, 404, 1},
		{"unknown-404", supported, `{"error":"other"}`, "could not be confirmed", 200, 404, 1},
		{"malformed-404", supported, `{`, "could not be confirmed", 200, 404, 1},
		{"resolver-error", supported, `{}`, "could not be confirmed", 200, 503, 1},
		{"resolver-malformed", supported, `{`, "could not be confirmed", 200, 200, 1},
		{"supported", supported, `{"id":"host","status":"resolved"}`, "", 200, 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					w.WriteHeader(tc.healthStatus)
					_, _ = io.WriteString(w, tc.health)
				case "/project/current":
					_, _ = io.WriteString(w, `{"project":"test","project_source":"config"}`)
				case "/runtime-sessions/resolve":
					requests++
					w.WriteHeader(tc.resolveStatus)
					_, _ = io.WriteString(w, tc.resolution)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			}))
			defer endpoint.Close()
			t.Setenv("ENGRAM_URL", endpoint.URL)
			input := `{"session_id":"host","cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{"session_id":"forged"}}`
			output := string(guardCodexPreToolUse([]byte(input)))
			if requests != tc.requests {
				t.Errorf("resolver requests=%d want %d", requests, tc.requests)
			}
			if tc.reason == "" {
				if !strings.Contains(output, `"permissionDecision":"allow"`) || !strings.Contains(output, `"session_id":"host"`) {
					t.Fatal(output)
				}
			} else if !strings.Contains(output, `"permissionDecision":"deny"`) || !strings.Contains(output, tc.reason) || strings.Contains(output, "updatedInput") {
				t.Fatal(output)
			}
			if tc.reason == "Upgrade" && !strings.Contains(output, "restart") {
				t.Fatal(output)
			}
			if tc.reason != "Upgrade" && strings.Contains(output, "Upgrade") {
				t.Fatal(output)
			}
		})
	}
}

func TestCodexGuardConnectionFailureIsNotCompatibility(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint.Close()
	t.Setenv("ENGRAM_URL", endpoint.URL)
	output := string(guardCodexPreToolUse([]byte(`{"session_id":"host","cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{}}`)))
	if !strings.Contains(output, `"permissionDecision":"deny"`) || strings.Contains(output, "Upgrade") {
		t.Fatal(output)
	}
}
