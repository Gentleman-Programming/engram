package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPWriteTimingOptInRedactsRequestData(t *testing.T) {
	oldOutput := httpWriteTimingOutput
	t.Cleanup(func() { httpWriteTimingOutput = oldOutput })

	for _, enabled := range []bool{false, true} {
		t.Run("enabled="+map[bool]string{false: "false", true: "true"}[enabled], func(t *testing.T) {
			if enabled {
				t.Setenv(httpWriteTimingEnvironment, "1")
			} else {
				t.Setenv(httpWriteTimingEnvironment, "")
			}
			var output bytes.Buffer
			httpWriteTimingOutput = &output
			srv := New(newServerTestStore(t), 0)
			sessionID, project := "private-session-marker", "private-project-marker"
			for _, request := range []struct {
				path string
				body map[string]any
				want int
			}{
				{"/sessions", map[string]any{"id": sessionID, "project": project, "directory": "C:/private-path-marker"}, http.StatusCreated},
				{"/observations", map[string]any{"session_id": sessionID, "project": project, "title": "private-title-marker", "content": "private-content-marker", "operation_id": "private-operation-marker"}, http.StatusCreated},
				{"/observations/passive", map[string]any{"session_id": sessionID, "project": project, "content": "## Key Learnings:\n- private-passive-marker is long enough to be saved"}, http.StatusOK},
			} {
				recorder := serveJSON(srv, request.path, request.body)
				if recorder.Code != request.want {
					t.Fatalf("POST %s = %d: %s", request.path, recorder.Code, recorder.Body.String())
				}
			}
			if !enabled {
				if output.Len() != 0 {
					t.Fatalf("disabled diagnostics = %q", output.String())
				}
				return
			}
			records := decodeWriteTimingRecords(t, output.String())
			if len(records) != 3 {
				t.Fatalf("records = %q", output.String())
			}
			for index, record := range records {
				if len(record) != 9 || record["outcome"] != "completed" || record["response_write"] != "attempted" || record["attempts"].(float64) < 1 {
					t.Fatalf("unexpected record: %#v", record)
				}
				if record["operation"] != []string{"session_registration", "observation_save", "passive_capture"}[index] {
					t.Fatalf("operation = %q", record["operation"])
				}
				for _, key := range []string{"request_ms", "connection_wait_ms", "transaction_ms", "commit_ms", "response_write_ms"} {
					if value, ok := record[key].(float64); !ok || value < 0 {
						t.Fatalf("%s = %v in %#v", key, record[key], record)
					}
				}
			}
			for _, secret := range []string{"private-session-marker", "private-project-marker", "private-path-marker", "private-title-marker", "private-content-marker", "private-operation-marker", "private-passive-marker"} {
				if strings.Contains(output.String(), secret) {
					t.Fatalf("private value %q leaked in %q", secret, output.String())
				}
			}
		})
	}
}

func TestHTTPWriteTimingFailuresDoNotChangeReplayOrResponseSemantics(t *testing.T) {
	t.Setenv(httpWriteTimingEnvironment, "1")
	oldOutput := httpWriteTimingOutput
	t.Cleanup(func() { httpWriteTimingOutput = oldOutput })
	srv := New(newServerTestStore(t), 0)
	httpWriteTimingOutput = failingTimingWriter{}
	if response := serveJSON(srv, "/sessions", map[string]any{"id": "timing-session", "project": "timing-project"}); response.Code != http.StatusCreated {
		t.Fatalf("session = %d: %s", response.Code, response.Body.String())
	}
	operationID := "private-replay-operation"
	first := serveJSON(srv, "/observations", map[string]any{"session_id": "timing-session", "project": "timing-project", "title": "private-title", "content": "private-content", "operation_id": operationID})
	replay := serveJSON(srv, "/observations", map[string]any{"session_id": "timing-session", "project": "timing-project", "title": "private-title", "content": "private-content", "operation_id": operationID})
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || first.Body.String() != replay.Body.String() {
		t.Fatalf("replay responses = %d %q / %d %q", first.Code, first.Body.String(), replay.Code, replay.Body.String())
	}
	stats, err := srv.store.Stats()
	if err != nil || stats.TotalObservations != 1 {
		t.Fatalf("observations = %d, err = %v", stats.TotalObservations, err)
	}

	var output bytes.Buffer
	httpWriteTimingOutput = &output
	failing := &failingResponseWriter{header: make(http.Header)}
	req := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(`{"session_id":"timing-session","project":"timing-project","title":"private-response-title","content":"private-response-content","operation_id":"private-response-operation"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(failing, req)
	if id, err := srv.store.GetObservationSaveResult("private-response-operation"); err != nil || id == 0 {
		t.Fatalf("response write failure changed save: id=%d err=%v", id, err)
	}
	records := decodeWriteTimingRecords(t, output.String())
	if len(records) != 1 || records[0]["operation"] != "observation_save" || records[0]["outcome"] != "response_write_failed" || records[0]["response_write"] != "failed" || strings.Contains(output.String(), "private-response") {
		t.Fatalf("response failure record = %q", output.String())
	}
}

func TestTimedResponseWriterUnwrapsForResponseController(t *testing.T) {
	underlying := &deadlineResponseWriter{header: make(http.Header)}
	deadline := time.Now().Add(time.Second)

	if err := http.NewResponseController(&timedResponseWriter{ResponseWriter: underlying}).SetWriteDeadline(deadline); err != nil {
		t.Fatalf("SetWriteDeadline() error = %v", err)
	}
	if !underlying.writeDeadline.Equal(deadline) {
		t.Fatalf("write deadline = %v, want %v", underlying.writeDeadline, deadline)
	}
}

func serveJSON(srv *Server, path string, body map[string]any) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(recorder, req)
	return recorder
}

func decodeWriteTimingRecords(t *testing.T, output string) []map[string]any {
	t.Helper()
	const prefix = "engram: http_write_timing "
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if !strings.HasPrefix(line, prefix) {
			t.Fatalf("unexpected diagnostic %q", line)
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, prefix)), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

type failingTimingWriter struct{}

func (failingTimingWriter) Write(data []byte) (int, error) {
	return 0, errors.New("telemetry sink failed")
}

type failingResponseWriter struct{ header http.Header }

func (w *failingResponseWriter) Header() http.Header { return w.header }
func (w *failingResponseWriter) WriteHeader(int)     {}
func (w *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("response write failed")
}

type deadlineResponseWriter struct {
	header        http.Header
	writeDeadline time.Time
}

func (w *deadlineResponseWriter) Header() http.Header { return w.header }
func (w *deadlineResponseWriter) WriteHeader(int)     {}
func (w *deadlineResponseWriter) Write([]byte) (int, error) {
	return 0, nil
}
func (w *deadlineResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.writeDeadline = deadline
	return nil
}
