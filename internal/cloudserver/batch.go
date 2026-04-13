package cloudserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

// BatchOperation represents a single operation in a batch request.
type BatchOperation struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// BatchResult represents the result of a single operation in a batch.
type BatchResult struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// handleBatch executes multiple operations sequentially in a single HTTP round trip.
// Each operation runs independently — partial failure is possible.
func handleBatch(handler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Operations []BatchOperation `json:"operations"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid batch request"})
			return
		}

		if len(req.Operations) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty operations"})
			return
		}
		if len(req.Operations) > 20 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max 20 operations per batch"})
			return
		}

		results := make([]BatchResult, len(req.Operations))
		for i, op := range req.Operations {
			// Create a sub-request that inherits auth context from the parent
			var body *bytes.Reader
			if op.Body != nil {
				body = bytes.NewReader(op.Body)
			} else {
				body = bytes.NewReader(nil)
			}

			subReq, err := http.NewRequestWithContext(r.Context(), op.Method, op.Path, body)
			if err != nil {
				results[i] = BatchResult{
					Status: http.StatusBadRequest,
					Body:   marshalJSON(map[string]string{"error": "invalid operation"}),
				}
				continue
			}

			// Copy auth headers from parent request
			subReq.Header.Set("Authorization", r.Header.Get("Authorization"))
			subReq.Header.Set("X-Engram-Protocol", r.Header.Get("X-Engram-Protocol"))
			subReq.Header.Set("Content-Type", "application/json")

			// Execute against the router
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, subReq)

			results[i] = BatchResult{
				Status: rec.Code,
				Body:   rec.Body.Bytes(),
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	}
}

func marshalJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
