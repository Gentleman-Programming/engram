package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Session registration is intentionally independent of the chunk-derived index.
type sessionAuthorityRegistrar interface {
	RegisterSessionAuthority(context.Context, string, string, string) error
}

func (s *CloudServer) handleRegisterSessionAuthority(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		SessionID string `json:"session_id"`
		Project   string `json:"project"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.pushBodyLimit()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "session authority request too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid session authority request", http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "session authority request too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid session authority request", http.StatusBadRequest)
		return
	}
	sessionID, project := strings.TrimSpace(payload.SessionID), strings.TrimSpace(payload.Project)
	if sessionID == "" || project == "" {
		http.Error(w, "session_id and project are required", http.StatusBadRequest)
		return
	}
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	principal, hasPrincipal := PrincipalFromContext(r.Context())
	usablePrincipal := hasPrincipal && strings.TrimSpace(principal.ID) != ""
	if !usablePrincipal && s.auth == nil {
		http.Error(w, "authentication unavailable", http.StatusUnauthorized)
		return
	}
	if !s.authorizeProjectScope(r.Context(), w, project) {
		return
	}
	actor := "legacy:authenticated"
	if usablePrincipal {
		actor = strings.TrimSpace(principal.ID)
	}
	registrar, ok := s.store.(sessionAuthorityRegistrar)
	if !ok {
		http.Error(w, "session authority store unavailable", http.StatusInternalServerError)
		return
	}
	if err := registrar.RegisterSessionAuthority(r.Context(), sessionID, project, actor); err != nil {
		if errors.Is(err, cloudstore.ErrSessionAuthorityConflict) {
			http.Error(w, "session authority conflict", http.StatusConflict)
		} else {
			http.Error(w, "session authority storage unavailable", http.StatusInternalServerError)
		}
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok"})
}
