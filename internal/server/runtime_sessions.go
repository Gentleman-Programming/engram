package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

type expectedRuntimeSessionID string

func (id *expectedRuntimeSessionID) UnmarshalJSON(data []byte) error {
	var value string
	if *id != "" || json.Unmarshal(data, &value) != nil || strings.TrimSpace(value) == "" {
		return errors.New("expected_effective_id must be a single nonblank session ID")
	}
	*id = expectedRuntimeSessionID(value)
	return nil
}

// Runtime operations never register or repair an identity. Both the root and
// selected continuation must already belong to the supplied runtime context.
func (s *Server) handleRuntimeSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID                  string                   `json:"id"`
		Project             string                   `json:"project"`
		Directory           string                   `json:"directory"`
		OwnershipMode       string                   `json:"ownership_mode"`
		Summary             string                   `json:"summary"`
		ExpectedEffectiveID expectedRuntimeSessionID `json:"expected_effective_id"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.Decode(new(any)) != io.EOF || strings.TrimSpace(body.ID) == "" || strings.TrimSpace(body.Project) == "" || strings.TrimSpace(body.Directory) == "" {
		jsonError(w, http.StatusBadRequest, "id, project and directory are required in one valid JSON object")
		return
	}
	if body.OwnershipMode == "" {
		body.OwnershipMode = store.SessionOwnershipShared
	}
	if body.OwnershipMode != store.SessionOwnershipShared && body.OwnershipMode != store.SessionOwnershipProjectOwned {
		jsonError(w, http.StatusBadRequest, "invalid ownership_mode")
		return
	}
	end := r.URL.Path == "/runtime-sessions/end"
	if !end && body.ExpectedEffectiveID != "" {
		jsonError(w, http.StatusBadRequest, "expected_effective_id is only accepted when ending a runtime session")
		return
	}
	var id string
	var err error
	if end {
		directory := projectpkg.RuntimeWorktreeDirectory(body.Directory)
		if body.ExpectedEffectiveID != "" {
			id, err = s.store.EndRuntimeSessionIfCurrent(body.ID, body.Project, directory, body.OwnershipMode, body.Summary, string(body.ExpectedEffectiveID))
		} else {
			id, err = s.store.EndRuntimeSession(body.ID, body.Project, directory, body.OwnershipMode, body.Summary)
		}
	} else {
		ctx := r.Context()
		id, err = s.store.ResolveRuntimeSessionFromDirectoryContext(ctx, body.ID, body.Project, body.Directory, body.OwnershipMode)
		if ctx.Err() != nil {
			return
		}
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		if errors.Is(err, store.ErrRuntimeSessionScopeConflict) || errors.Is(err, store.ErrRuntimeSessionEffectiveIDMismatch) {
			status = http.StatusConflict
		}
		jsonError(w, status, err.Error())
		return
	}
	ack := map[string]string{"id": id, "status": "resolved"}
	if end {
		ack["status"] = "ended"
		s.notifyWrite()
	}
	if id != body.ID {
		ack["resumed_from"] = body.ID
	}
	jsonResponse(w, http.StatusOK, ack)
}
