package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/hostbind"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func (s *Server) handleManagedRuntimeScopes(w http.ResponseWriter, r *http.Request) {
	if authorized, _ := r.Context().Value(ownedAuthKey{}).(bool); !authorized {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawQuery != "" {
		jsonError(w, http.StatusBadRequest, "managed runtime scope enumeration takes no arguments")
		return
	}
	scopes, err := s.store.ListManagedRuntimeScopes(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "managed runtime enumeration failed")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"scopes": scopes})
}

func (s *Server) handleManagedRuntime(w http.ResponseWriter, r *http.Request) {
	// Only the private Core owner uses these routes. The supervisor keeps its
	// owner bearer out of the managed host and separately verifies process exit.
	if authorized, _ := r.Context().Value(ownedAuthKey{}).(bool); !authorized {
		http.NotFound(w, r)
		return
	}
	attach := r.URL.Path == "/internal/managed-runtime/attach"
	save := r.URL.Path == "/internal/managed-runtime/save"
	var body struct {
		Root          string                      `json:"root"`
		EffectiveID   string                      `json:"effective_id"`
		Project       string                      `json:"project"`
		Directory     string                      `json:"directory"`
		Mode          string                      `json:"ownership_mode"`
		Generation    int64                       `json:"generation"`
		OperationID   string                      `json:"operation_id"`
		Observation   *store.AddObservationParams `json:"observation"`
		Host          *store.ManagedRuntimeHost   `json:"host"`
		Resume        *bool                       `json:"resume"`
		ForRetirement *bool                       `json:"for_retirement"`
	}
	var input io.Reader = r.Body
	switch r.URL.Path {
	case "/internal/managed-runtime/write-intent", "/internal/managed-runtime/save":
		// Use the same wire allowance as ordinary observation saves, so
		// the Store can perform its normal redaction and truncation.
	default:
		input = http.MaxBytesReader(w, r.Body, 8<<10)
	}
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.Decode(new(any)) != io.EOF ||
		strings.TrimSpace(body.Root) == "" ||
		((!attach || body.EffectiveID != "") && strings.TrimSpace(body.EffectiveID) == "") ||
		strings.TrimSpace(body.Project) == "" || strings.TrimSpace(body.Directory) == "" ||
		(body.Mode != store.SessionOwnershipProjectOwned && body.Mode != store.SessionOwnershipShared) {
		jsonError(w, http.StatusBadRequest, "invalid managed runtime scope")
		return
	}
	scope := store.ManagedRuntimeScope{Root: body.Root, EffectiveID: body.EffectiveID, Project: body.Project, Directory: body.Directory, Mode: body.Mode}
	var observation store.AddObservationResult
	var recovered int
	var moreWrites bool
	var err error
	if observation := body.Observation; observation != nil {
		if !save && r.URL.Path != "/internal/managed-runtime/write-intent" {
			jsonError(w, http.StatusBadRequest, "observation is only accepted for a managed write")
			return
		}
		if observation.OperationID != "" || observation.SessionID != "" || observation.Project != "" {
			jsonError(w, http.StatusBadRequest, "observation identity comes from its managed scope")
			return
		}
	}
	if body.ForRetirement != nil && r.URL.Path != "/internal/managed-runtime/hosts" {
		jsonError(w, http.StatusBadRequest, "only host lookup takes a retirement option")
		return
	}
	var hosts []store.ManagedRuntimeHost
	if attach != (body.Resume != nil) {
		jsonError(w, http.StatusBadRequest, "only attachment takes a resume decision")
		return
	}
	if body.Host != nil && !attach && r.URL.Path != "/internal/managed-runtime/admit" && r.URL.Path != "/internal/managed-runtime/retire" {
		jsonError(w, http.StatusBadRequest, "host is only accepted for admission and retirement")
		return
	}
	switch r.URL.Path {
	case "/internal/managed-runtime/attach", "/internal/managed-runtime/admit":
		if body.Generation != 0 || body.OperationID != "" || body.Host == nil ||
			body.Host.PID <= 1 || strings.TrimSpace(body.Host.Start) == "" || body.Host.Domain != "" {
			jsonError(w, http.StatusBadRequest, "admission requires a host PID and start token")
			return
		}
		body.Host.Domain, err = hostbind.ProcessDomain()
		if err != nil {
			jsonError(w, http.StatusServiceUnavailable, "process domain unavailable")
			return
		}
		start, inspectErr := hostbind.ProcessStart(body.Host.PID)
		if inspectErr != nil || start != body.Host.Start {
			jsonError(w, http.StatusConflict, "host identity is not current")
			return
		}
		if attach {
			scope.EffectiveID, body.Generation, err = s.store.AttachManagedRuntime(scope, *body.Host, *body.Resume)
			if err == nil {
				s.notifyWrite()
			}
		} else {
			body.Generation, err = s.store.AdmitManagedRuntime(scope, *body.Host)
		}
	case "/internal/managed-runtime/hosts":
		if body.Generation != 0 || body.OperationID != "" {
			jsonError(w, http.StatusBadRequest, "host lookup takes only a runtime scope")
			return
		}
		if body.ForRetirement != nil && *body.ForRetirement {
			body.Generation, hosts, err = s.store.ListManagedRuntimeRetirementHosts(scope)
		} else {
			body.Generation, hosts, err = s.store.ListManagedRuntimeHosts(scope)
		}
	case "/internal/managed-runtime/retire":
		if body.Generation <= 0 || body.OperationID != "" || body.Host == nil ||
			body.Host.PID <= 1 || strings.TrimSpace(body.Host.Start) == "" || strings.TrimSpace(body.Host.Domain) == "" {
			jsonError(w, http.StatusBadRequest, "retirement requires a generation and complete host identity")
			return
		}
		exited, inspectErr := hostbind.ProcessTerminated(body.Host.PID, body.Host.Start, body.Host.Domain)
		if inspectErr != nil || !exited {
			jsonError(w, http.StatusConflict, "host exit cannot be verified")
			return
		}
		err = s.store.RetireManagedRuntimeHost(scope, body.Generation, *body.Host)
	case "/internal/managed-runtime/write-intent":
		if body.Generation <= 0 || strings.TrimSpace(body.OperationID) == "" {
			jsonError(w, http.StatusBadRequest, "write intent requires a generation and an operation ID")
			return
		}
		if body.Observation == nil {
			err = s.store.RecordManagedRuntimeWrite(scope, body.Generation, body.OperationID)
		} else {
			err = s.store.RecordManagedRuntimeObservation(scope, body.Generation, body.OperationID, *body.Observation)
		}
	case "/internal/managed-runtime/save":
		if body.Generation <= 0 || strings.TrimSpace(body.OperationID) == "" || body.Observation == nil {
			jsonError(w, http.StatusBadRequest, "managed save requires a scoped intent and an observation")
			return
		}
		body.Observation.OperationID = body.OperationID
		observation, err = s.store.SaveManagedRuntimeObservation(scope, body.Generation, *body.Observation)
		if err == nil && !observation.Replayed {
			s.notifyWrite()
		}
	case "/internal/managed-runtime/recover-writes":
		if body.Generation <= 0 || body.OperationID != "" {
			jsonError(w, http.StatusBadRequest, "write recovery requires the current generation")
			return
		}
		recovered, moreWrites, err = s.store.RecoverManagedRuntimeObservations(scope, body.Generation)
		if recovered > 0 {
			s.notifyWrite()
		}
	case "/internal/managed-runtime/close":
		if body.Generation <= 0 || body.OperationID != "" {
			jsonError(w, http.StatusBadRequest, "close requires the terminal generation")
			return
		}
		var applied bool
		applied, err = s.store.CloseManagedRuntime(scope, body.Generation)
		if applied {
			s.notifyWrite()
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, store.ErrManagedRuntimeHostIdentity):
			jsonError(w, http.StatusConflict, err.Error())
		case errors.Is(err, store.ErrManagedRuntimeScopeEnded):
			jsonErrorWithFields(w, http.StatusConflict, err.Error(), map[string]any{"code": "managed_runtime_scope_ended"})
		case errors.Is(err, store.ErrManagedRuntimeHostsRemain):
			jsonErrorWithFields(w, http.StatusConflict, err.Error(), map[string]any{"code": "managed_runtime_hosts_remain"})
		case errors.Is(err, store.ErrManagedRuntimeWriteUnresolved):
			jsonErrorWithFields(w, http.StatusConflict, err.Error(), map[string]any{"code": "managed_runtime_write_unresolved"})
		case errors.Is(err, store.ErrManagedRuntimeGeneration), errors.Is(err, store.ErrRuntimeSessionEffectiveIDMismatch), errors.Is(err, store.ErrRuntimeSessionScopeConflict),
			errors.Is(err, store.ErrSessionAlreadyEnded), errors.Is(err, store.ErrSessionOwnershipMismatch):
			jsonError(w, http.StatusConflict, err.Error())
		case errors.Is(err, store.ErrObservationTitleRequired), errors.Is(err, store.ErrObservationContentRequired):
			jsonError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, store.ErrObservationOperationConflict):
			jsonErrorWithFields(w, http.StatusConflict, err.Error(), map[string]any{"code": "managed_runtime_write_conflict"})
		case errors.Is(err, sql.ErrNoRows):
			jsonError(w, http.StatusNotFound, "managed runtime scope not found")
		default:
			jsonError(w, http.StatusInternalServerError, "managed runtime operation failed")
		}
		return
	}
	if save {
		jsonResponse(w, http.StatusCreated, map[string]any{"id": observation.ID, "status": "saved", "replayed": observation.Replayed})
		return
	}
	if r.URL.Path == "/internal/managed-runtime/recover-writes" {
		jsonResponse(w, http.StatusOK, map[string]any{"effective_id": scope.EffectiveID, "generation": body.Generation,
			"recovered": recovered, "more": moreWrites})
		return
	}
	response := map[string]any{"effective_id": scope.EffectiveID, "generation": body.Generation}
	if r.URL.Path == "/internal/managed-runtime/hosts" {
		response["hosts"] = hosts
	}
	jsonResponse(w, http.StatusOK, response)
}
