//go:build e2e

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCallerBindingPublicReadOnly(t *testing.T) {
	st, ts := newE2EServer(t)
	client := ts.Client()
	// HTTP deliberately hides leases; inspect them through the read-only Store API.
	type leaseState struct {
		Present bool
		Value   string
	}
	leases := func(t *testing.T) [2]leaseState {
		t.Helper()
		var states [2]leaseState
		for i, id := range []string{"caller-root", "caller-root:resume:2"} {
			session, err := st.GetSession(id)
			if err != nil {
				t.Fatalf("read fixture session: %v", err)
			}
			if session.RuntimeLeaseExpiresAt != nil {
				states[i] = leaseState{Present: true, Value: *session.RuntimeLeaseExpiresAt}
			}
		}
		return states
	}
	post := func(t *testing.T, path, body string, want int) string {
		t.Helper()
		var beforeLeases [2]leaseState
		if path == "/doctor/caller-binding" {
			beforeLeases = leases(t)
			if !beforeLeases[1].Present || beforeLeases[1].Value == "" {
				t.Fatal("fixture must contain a live continuation lease")
			}
		}
		resp, err := client.Post(ts.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s: %d want %d: %s", path, resp.StatusCode, want, b)
		}
		if path == "/doctor/caller-binding" && leases(t) != beforeLeases {
			t.Fatal("diagnosis changed a runtime lease")
		}
		return string(b)
	}
	get := func(path string) string {
		t.Helper()
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	post(t, "/sessions", `{"id":"caller-root","project":"fixture"}`, 201)
	post(t, "/observations", `{"session_id":"caller-root","project":"fixture","type":"note","title":"fixture","content":"private synthetic content"}`, 201)
	post(t, "/sessions/caller-root/end", `{"summary":"synthetic"}`, 200)
	post(t, "/sessions", `{"id":"caller-root:resume:2","project":"fixture"}`, 201)
	var health struct {
		Summary struct{ Total, OK, Warnings, Blocked, Errors int }
	}
	if err := json.Unmarshal([]byte(get("/doctor?project=fixture")), &health); err != nil {
		t.Fatal(err)
	}
	if health.Summary.Total == 0 || health.Summary.OK != health.Summary.Total || health.Summary.Warnings != 0 || health.Summary.Blocked != 0 || health.Summary.Errors != 0 {
		t.Fatalf("project health: %#v", health)
	}
	paths := []string{"/sessions/caller-root", "/sessions/caller-root:resume:2", "/sessions/caller-root:resume:3", "/observations?project=fixture", "/stats"}
	before := make([]string, len(paths))
	for i, p := range paths {
		before[i] = get(p)
	}
	const collect = "Collect caller context without registering a session."
	const inspect = "Inspect the runtime binding; diagnosis does not repair it."
	const resume = "Resume through a host that can persist effective identity; do not reopen the ended session."
	for _, tc := range []struct{ name, effective, host, reason, status, next string }{
		{"ended root", "caller-root", `{"append_entry_available":false,"branch_available":true}`, "ended_session_without_persistence", "blocked", resume},
		{"safe resume", "caller-root", `{"append_entry_available":true,"branch_available":true}`, "resume_required", "warning", resume},
		{"mapped live", "caller-root:resume:2", `{"append_entry_available":false,"branch_available":false}`, "binding_observed", "ok", "No action required; a later write is not guaranteed."},
		{"missing effective", "absent", `{}`, "effective_session_missing", "unknown", inspect},
		{"unregistered noncanonical mapping", "caller-root:resume:02", `{}`, "effective_session_missing", "unknown", inspect},
		{"unknown host", "caller-root", `{"append_entry_available":null}`, "host_context_missing", "unknown", collect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := post(t, "/doctor/caller-binding", `{"project":" FIXTURE ","runtime_session_id":"caller-root","effective_session_id":"`+tc.effective+`","host_context":`+tc.host+`}`, 200)
			var out map[string]any
			if err := json.Unmarshal([]byte(body), &out); err != nil {
				t.Fatal(err)
			}
			if len(out) != 6 || out["reason_code"] != tc.reason || out["status"] != tc.status || out["safe_next_step"] != tc.next || out["write_success_guaranteed"] != false {
				t.Fatalf("assessment: %s", body)
			}
			if strings.Contains(body, "caller-root") || strings.Contains(body, "fixture") {
				t.Fatalf("identity leaked: %s", body)
			}
		})
	}
	for _, tc := range []struct{ body, reason, next string }{
		{`{}`, "caller_context_missing", collect},
		{`{"project":"/","runtime_session_id":"caller-root","effective_session_id":"caller-root:resume:2"}`, "caller_context_missing", collect},
		{`{"project":"private\u0000name","runtime_session_id":"caller-root","effective_session_id":"caller-root:resume:2"}`, "caller_context_missing", collect},
		{`{"project":"fixture","runtime_session_id":"absent","effective_session_id":"absent"}`, "session_not_registered", "Use the normal runtime registration path; diagnosis does not register sessions."},
	} {
		var out map[string]any
		body := post(t, "/doctor/caller-binding", tc.body, 200)
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		if len(out) != 6 || strings.Contains(body, "private") || strings.Contains(body, "caller-root") || out["status"] != "unknown" || out["reason_code"] != tc.reason || out["safe_next_step"] != tc.next || out["write_success_guaranteed"] != false {
			t.Fatalf("assessment: %s", body)
		}
	}
	for _, body := range []string{`null`, `[]`, `{"unknown":"private"}`, `{"runtime_session_id":3}`, `{"host_context":{"branch_available":"yes"}}`, `{} {}`, `{"project":"` + strings.Repeat("x", 17000) + `"}`} {
		rejected := post(t, "/doctor/caller-binding", body, http.StatusBadRequest)
		var out map[string]any
		if err := json.Unmarshal([]byte(rejected), &out); err != nil {
			t.Fatal(err)
		}
		// Rejections are sanitized transport errors, not successful assessments.
		if len(out) != 1 || out["error"] != "invalid caller-binding request" {
			t.Fatalf("rejection leaked evidence or claimed readiness: %s", rejected)
		}
	}
	for i, p := range paths {
		if after := get(p); after != before[i] {
			t.Fatalf("diagnosis changed %s: %s => %s", p, before[i], after)
		}
	}
}
