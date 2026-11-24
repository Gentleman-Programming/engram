package server

import (
	"database/sql"
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
	case errors.Is(err, store.ErrManagedObservationUpdateEmpty),
		errors.Is(err, store.ErrExpectedProjectRequired),
		errors.Is(err, store.ErrSessionIDRequired),
		errors.Is(err, store.ErrProjectRequired),
		errors.Is(err, store.ErrInvalidSessionOwnershipMode),
		errors.Is(err, store.ErrObservationTitleRequired),
		errors.Is(err, store.ErrObservationContentRequired),
		errors.Is(err, store.ErrObservationFindReplacePairRequired),
		errors.Is(err, store.ErrObservationFindReplaceContentConflict),
		errors.Is(err, store.ErrObservationFindReplaceInputTooLarge),
		errors.Is(err, store.ErrObservationFindReplaceResultTooLarge),
		errors.Is(err, store.ErrObservationFindReplaceLegacyContentLarge):
		jsonError(w, http.StatusBadRequest, "invalid managed observation update fields")
	case errors.Is(err, store.ErrObservationProjectImmutable):
		jsonError(w, http.StatusConflict, "managed observation project cannot change")
	case errors.Is(err, store.ErrManagedRuntimeHostIdentity),
		errors.Is(err, store.ErrRuntimeSessionEffectiveIDMismatch),
		errors.Is(err, store.ErrRuntimeSessionScopeConflict),
		errors.Is(err, store.ErrSessionOwnershipMismatch),
		errors.Is(err, store.ErrSessionAlreadyEnded),
		errors.Is(err, sql.ErrNoRows):
		jsonError(w, http.StatusConflict, "managed observation update requires the current attachment")
	case err != nil:
		jsonError(w, http.StatusInternalServerError, "managed observation update failed")
	default:
		if !result.Replayed {
			s.notifyWrite()
		}
		jsonResponse(w, http.StatusOK, result)
	}
}
