package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func (s *Server) handleManagedObservationUpdate(w http.ResponseWriter, r *http.Request) {
	if authorized, _ := r.Context().Value(ownedAuthKey{}).(bool); !authorized {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Scope       store.ManagedRuntimeScope      `json:"scope"`
		Host        store.ManagedRuntimeHost       `json:"host"`
		OperationID string                         `json:"operation_id"`
		Update      store.ManagedObservationUpdate `json:"update"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, (1<<20)+(64<<10)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.Decode(new(any)) != io.EOF {
		jsonError(w, http.StatusBadRequest, "invalid managed observation update")
		return
	}
	result, err := s.store.UpdateManagedRuntimeObservation(r.Context(), body.Scope, body.Host, body.OperationID, body.Update)
	switch {
	case errors.Is(err, store.ErrObservationOperationConflict):
		jsonError(w, http.StatusConflict, "managed update operation conflicts with an existing write")
	case errors.Is(err, store.ErrManagedRuntimeWriteUnresolved):
		jsonError(w, http.StatusConflict, "an earlier managed save is unresolved")
	case errors.Is(err, store.ErrObservationProjectMismatch), errors.Is(err, store.ErrObservationNotFound):
		jsonError(w, http.StatusNotFound, "managed observation not found in the attached project")
	case err != nil:
		jsonError(w, http.StatusConflict, "managed observation update did not complete")
	default:
		if !result.Replayed {
			s.notifyWrite()
		}
		jsonResponse(w, http.StatusOK, result)
	}
}
