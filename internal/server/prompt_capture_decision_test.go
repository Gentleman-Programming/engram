package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestPromptCaptureDecision verifies the public read-only decision and persistence contracts.
func TestPromptCaptureDecision(t *testing.T) {
	t.Setenv("ENGRAM_PROJECT", "capture-project")
	st := newServerTestStore(t)
	srv := New(st, 0)
	cwd := t.TempDir()
	request := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, bytes.NewBufferString(body)))
		return rec
	}
	stats := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/stats?all_projects=true", nil))
		if rec.Code != 200 {
			t.Fatalf("stats = %d: %s", rec.Code, rec.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := stats()
	unsupported := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unsupported, httptest.NewRequest("GET", "/prompts/capture-decision", nil))
	if unsupported.Code != 405 || unsupported.Body.String() != "Method Not Allowed\n" {
		t.Fatalf("unsupported method = %d: %q", unsupported.Code, unsupported.Body.String())
	}
	if !reflect.DeepEqual(before, stats()) {
		t.Fatal("unsupported method changed public stats")
	}
	for _, tc := range []struct {
		name, body, decision string
		status               int
	}{
		{"human", `{"source":"claude-code","cwd":` + quoteCapture(cwd) + `,"content":"  help me  "}`, "capture", 200},
		{"task", `{"source":"claude-code","cwd":` + quoteCapture(cwd) + `,"content":" \n<task-notification>done"}`, "skip", 200},
		{"agent", `{"source":"claude-code","cwd":` + quoteCapture(cwd) + `,"content":"\t<agent-message from=x>"}`, "skip", 200},
		{"blank", `{"source":"claude-code","cwd":"unused","content":"  "}`, "skip", 200},
		{"empty", `{"source":"claude-code","cwd":"unused","content":""}`, "skip", 200},
		{"embedded", `{"source":"claude-code","cwd":` + quoteCapture(cwd) + `,"content":"explain <agent-message"}`, "capture", 200},
		{"malformed", `{`, "", 400},
		{"missing", `{"source":"claude-code","cwd":"x"}`, "", 400},
		{"null", `{"source":"claude-code","cwd":"x","content":null}`, "", 400},
		{"wrong cwd type", `{"source":"claude-code","cwd":4,"content":"hello"}`, "", 400},
		{"wrong source type", `{"source":true,"cwd":"x","content":"hello"}`, "", 400},
		{"wrong type", `{"source":"claude-code","cwd":"x","content":7}`, "", 400},
		{"source", `{"source":"pi","cwd":"x","content":"hello"}`, "", 400},
		{"missing source", `{"cwd":"x","content":"hello"}`, "", 400},
		{"cwd", `{"source":"claude-code","cwd":" ","content":"hello"}`, "", 400},
		{"unknown field", `{"source":"claude-code","cwd":"x","content":"hello","extra":true}`, "", 400},
		{"trailing", `{"source":"claude-code","cwd":"x","content":"hello"} {}`, "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request("/prompts/capture-decision", tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.status == 400 {
				want := "source must be claude-code, cwd must be nonblank, and content must be a string"
				switch tc.name {
				case "malformed", "wrong cwd type", "wrong source type", "wrong type", "unknown field", "trailing":
					want = "invalid json"
				}
				if body["error"] != want {
					t.Fatalf("error = %v, want %q", body["error"], want)
				}
			}
			if !reflect.DeepEqual(before, stats()) {
				t.Fatal("decision changed public stats")
			}
			if tc.decision != "" {
				if body["decision"] != tc.decision {
					t.Fatalf("body = %v", body)
				}
				if tc.decision == "capture" {
					current := httptest.NewRecorder()
					srv.Handler().ServeHTTP(current, httptest.NewRequest("GET", "/project/current?cwd="+cwd, nil))
					var metadata map[string]any
					if err := json.Unmarshal(current.Body.Bytes(), &metadata); err != nil {
						t.Fatal(err)
					}
					delete(body, "decision")
					if !reflect.DeepEqual(body, metadata) {
						t.Fatalf("metadata = %v, want %v", body, metadata)
					}
				} else if len(body) != 1 {
					t.Fatalf("skip body = %v", body)
				}
			}
		})
	}
	t.Setenv("ENGRAM_PROJECT", "invalid/project")
	rec := request("/prompts/capture-decision", `{"source":"claude-code","cwd":"x","content":"human"}`)
	if rec.Code != 400 {
		t.Fatalf("project error = %d: %s", rec.Code, rec.Body.String())
	}
	after := stats()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("decision mutated stats: before=%+v after=%+v", before, after)
	}
	t.Setenv("ENGRAM_PROJECT", "capture-project")
	if err := st.CreateSession("capture-session", "capture-project", cwd); err != nil {
		t.Fatal(err)
	}
	original := "  human content\n "
	rec = request("/prompts", `{"session_id":"capture-session","project":"capture-project","content":`+quoteCapture(original)+`}`)
	if rec.Code != 201 {
		t.Fatalf("persist = %d: %s", rec.Code, rec.Body.String())
	}
	recent := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recent, httptest.NewRequest("GET", "/prompts/recent?project=capture-project", nil))
	if recent.Code != 200 {
		t.Fatalf("recent prompts = %d: %s", recent.Code, recent.Body.String())
	}
	var prompts []struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(recent.Body.Bytes(), &prompts); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || prompts[0].Content != strings.TrimSpace(original) {
		t.Fatalf("prompts = %+v", prompts)
	}
}

// TestPromptCaptureDecisionInspectsUnboundGit verifies decisions never establish Git bindings.
func TestPromptCaptureDecisionInspectsUnboundGit(t *testing.T) {
	t.Setenv("ENGRAM_PROJECT", "")
	root := t.TempDir()
	if output, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	st := newServerTestStore(t)
	h := New(st, 0).Handler()
	stats := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/stats?all_projects=true", nil))
		if rec.Code != 200 {
			t.Fatalf("stats: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	snapshot := func() map[string]string {
		t.Helper()
		files := map[string]string{}
		err := filepath.WalkDir(filepath.Join(root, ".git"), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				files[path] = string(content)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	beforeStats, beforeGit := stats(), snapshot()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/prompts/capture-decision", strings.NewReader(`{"source":"claude-code","cwd":`+quoteCapture(root)+`,"content":"human"}`)))
	if !reflect.DeepEqual(beforeGit, snapshot()) {
		t.Error("decision created or modified Git metadata")
	}
	if beforeStats != stats() {
		t.Error("decision changed public database stats")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || body["decision"] != "capture" || body["project_source"] != "unbound_git" {
		t.Fatalf("inspection response = %d: %s", rec.Code, rec.Body.String())
	}
	if err := st.CreateSession("historical", "different-project", root); err != nil {
		t.Fatal(err)
	}
	beforeStats = stats()
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/prompts/capture-decision", strings.NewReader(`{"source":"claude-code","cwd":`+quoteCapture(root)+`,"content":"human"}`)))
	if rec.Code != 409 || strings.Contains(rec.Body.String(), `"decision":"capture"`) {
		t.Fatalf("conflicting history = %d: %s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(beforeGit, snapshot()) {
		t.Error("conflicting decision changed Git metadata")
	}
	if beforeStats != stats() {
		t.Error("conflicting decision changed public database stats")
	}
}

// quoteCapture encodes a string for public HTTP request fixtures.
func quoteCapture(s string) string { b, _ := json.Marshal(s); return string(b) }
