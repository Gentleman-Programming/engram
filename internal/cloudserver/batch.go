package cloudserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/go-chi/chi/v5"
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

// batchContext wraps a parent context but strips chi's route context key
// so sub-requests get fresh routing, while preserving cancellation, deadline,
// and auth values from the parent.
type batchContext struct {
	context.Context
}

func (c batchContext) Value(key any) any {
	// Strip chi's route context to avoid 405 on sub-requests
	if key == chi.RouteCtxKey {
		return nil
	}
	return c.Context.Value(key)
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

		// Build a context that preserves parent cancellation/deadline and auth values,
		// but strips chi's routing state. Mark as batch-internal to skip redundant auth.
		parentCtx := r.Context()
		subCtx := context.WithValue(batchContext{parentCtx}, ctxBatchInternal, true)

		results := make([]BatchResult, len(req.Operations))
		for i, op := range req.Operations {
			var body *bytes.Reader
			if op.Body != nil {
				body = bytes.NewReader(op.Body)
			} else {
				body = bytes.NewReader(nil)
			}

			subReq, err := http.NewRequestWithContext(subCtx, op.Method, op.Path, body)
			if err != nil {
				results[i] = BatchResult{
					Status: http.StatusBadRequest,
					Body:   marshalJSON(map[string]string{"error": "invalid operation"}),
				}
				continue
			}

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
