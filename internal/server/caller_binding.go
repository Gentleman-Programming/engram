package server

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/Gentleman-Programming/engram/v3/internal/diagnostic"
)

// handleCallerBinding is a read-only POST: caller identities stay out of URLs.
func (s *Server) handleCallerBinding(w http.ResponseWriter, r *http.Request) {
	var input *diagnostic.CallerBindingInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input == nil {
		jsonError(w, http.StatusBadRequest, "invalid caller-binding request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		jsonError(w, http.StatusBadRequest, "invalid caller-binding request")
		return
	}
	jsonResponse(w, http.StatusOK, diagnostic.AssessCallerBinding(s.store, *input))
}
