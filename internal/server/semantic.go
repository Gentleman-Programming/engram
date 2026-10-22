package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/Gentleman-Programming/engram/v3/internal/embedding"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Classification applies only to the explicit semantic extension. Preserve the
// lexical route's historical status and error text.
func semanticStatus(err error) int {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout
	case errors.Is(err, store.ErrSemanticIndex), errors.Is(err, store.ErrSemanticChanged), errors.Is(err, embedding.ErrModelChanged):
		return http.StatusConflict
	case errors.Is(err, store.ErrSemanticUnconfigured), errors.Is(err, embedding.ErrUnavailable), errors.Is(err, embedding.ErrConfig), errors.Is(err, embedding.ErrModel), errors.Is(err, embedding.ErrRemoteModel):
		return http.StatusServiceUnavailable
	case errors.Is(err, embedding.ErrResponse), errors.Is(err, embedding.ErrVectors), errors.Is(err, store.ErrSemanticVector):
		return http.StatusBadGateway
	case errors.Is(err, embedding.ErrInput):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func writeSearchError(w http.ResponseWriter, mode string, err error) {
	if mode != "semantic" {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonError(w, semanticStatus(err), err.Error()+"; configure ENGRAM_EMBEDDING_MODEL and ENGRAM_EMBEDDING_URL for an installed local Ollama model; rebuild via engram embed with the same project/scope/type filters (no lexical fallback)")
}
