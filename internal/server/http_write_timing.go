package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

const httpWriteTimingEnvironment = "ENGRAM_HTTP_WRITE_TIMING"

var httpWriteTimingOutput io.Writer = os.Stderr

type httpWriteTimingKey struct{}

type httpWriteTiming struct {
	operation string
	started   time.Time
	timings   store.WriteTimings
}

func beginHTTPWriteTiming(r *http.Request) *httpWriteTiming {
	if os.Getenv(httpWriteTimingEnvironment) != "1" {
		return nil
	}
	operation := ""
	switch r.Method + " " + r.URL.Path {
	case http.MethodPost + " /sessions":
		operation = "session_registration"
	case http.MethodPost + " /observations":
		operation = "observation_save"
	case http.MethodPost + " /observations/passive":
		operation = "passive_capture"
	}
	if operation == "" {
		return nil
	}
	return &httpWriteTiming{operation: operation, started: time.Now()}
}

func requestWriteTimings(r *http.Request) *store.WriteTimings {
	timing, _ := r.Context().Value(httpWriteTimingKey{}).(*httpWriteTiming)
	if timing == nil {
		return nil
	}
	return &timing.timings
}

func (t *httpWriteTiming) finish(writer *timedResponseWriter) {
	outcome := "completed"
	responseWrite := "attempted"
	switch {
	case writer.writeFailed:
		outcome, responseWrite = "response_write_failed", "failed"
	case writer.writes == 0:
		outcome, responseWrite = "no_response", "not_attempted"
	case writer.status >= http.StatusInternalServerError:
		outcome = "failed"
	case writer.status >= http.StatusBadRequest:
		outcome = "rejected"
	}
	record := struct {
		Operation        string  `json:"operation"`
		Outcome          string  `json:"outcome"`
		Attempts         int     `json:"attempts"`
		RequestMS        float64 `json:"request_ms"`
		ConnectionWaitMS float64 `json:"connection_wait_ms"`
		TransactionMS    float64 `json:"transaction_ms"`
		CommitMS         float64 `json:"commit_ms"`
		ResponseWriteMS  float64 `json:"response_write_ms"`
		ResponseWrite    string  `json:"response_write"`
	}{
		t.operation, outcome, t.timings.Attempts, milliseconds(time.Since(t.started)),
		milliseconds(t.timings.ConnectionWait), milliseconds(t.timings.Transaction),
		milliseconds(t.timings.Commit), milliseconds(writer.writeDuration), responseWrite,
	}
	if data, err := json.Marshal(record); err == nil {
		_, _ = fmt.Fprintf(httpWriteTimingOutput, "engram: http_write_timing %s\n", data)
	}
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

type timedResponseWriter struct {
	http.ResponseWriter
	status        int
	writes        int
	writeFailed   bool
	writeDuration time.Duration
}

func (w *timedResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *timedResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	started := time.Now()
	w.ResponseWriter.WriteHeader(status)
	w.writeDuration += time.Since(started)
}

func (w *timedResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	w.writes++
	started := time.Now()
	n, err := w.ResponseWriter.Write(body)
	w.writeDuration += time.Since(started)
	if err != nil {
		w.writeFailed = true
	}
	return n, err
}
