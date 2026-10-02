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

type promptPairClaimStore interface {
	GetSessionAuthority(context.Context, string) (*cloudstore.SessionAuthority, error)
	ClaimPromptPair(context.Context, string, string, string, string, string) error
}

func sessionAuthorityUnavailable(w http.ResponseWriter) {
	jsonResponse(w, http.StatusNotFound, map[string]string{
		"error": "session authority unavailable", "error_code": "session_authority_unavailable",
	})
}

func (s *CloudServer) handlePromptPairClaim(w http.ResponseWriter, r *http.Request) {
	// Even if project policy is configured, an insecure server cannot issue claims.
	principal, hasPrincipal := PrincipalFromContext(r.Context())
	usablePrincipal := hasPrincipal && strings.TrimSpace(principal.ID) != ""
	if s.auth == nil || (s.principalAuth != nil && !usablePrincipal) {
		http.Error(w, "authentication unavailable", http.StatusUnauthorized)
		return
	}
	// Never fall back to a legacy allowlist for a managed principal, or to
	// unrestricted policy when the required project authorizer is missing.
	if usablePrincipal && usesManagedProjectGrants(principal) {
		if s.principalProject == nil {
			http.Error(w, "project authorization unavailable", http.StatusForbidden)
			return
		}
	} else if s.projectAuth == nil {
		http.Error(w, "project authorization unavailable", http.StatusForbidden)
		return
	}
	var payload struct {
		SessionID     string `json:"session_id"`
		SourceInboxID string `json:"source_inbox_id"`
		SyncID        string `json:"sync_id"`
		OwnerProject  string `json:"owner_project"`
		Project       string `json:"project"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.pushBodyLimit()))
	decoder.DisallowUnknownFields()
	invalid := func(err error) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "prompt pair claim request too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid prompt pair claim request", http.StatusBadRequest)
	}
	if err := decoder.Decode(&payload); err != nil {
		invalid(err)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		invalid(err)
		return
	}
	sessionID := strings.TrimSpace(payload.SessionID)
	inboxID := strings.TrimSpace(payload.SourceInboxID)
	syncID := strings.TrimSpace(payload.SyncID)
	owner := strings.TrimSpace(payload.OwnerProject)
	project := strings.TrimSpace(payload.Project)
	if sessionID == "" || inboxID == "" || syncID == "" || owner == "" || project == "" {
		http.Error(w, "session_id, source_inbox_id, sync_id, owner_project and project are required", http.StatusBadRequest)
		return
	}
	owner, _ = store.NormalizeProject(owner)
	project, _ = store.NormalizeProject(project)
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(project) == "" {
		http.Error(w, "owner_project and project are required", http.StatusBadRequest)
		return
	}
	// Check BOTH grants before looking up the globally unique session ID.
	if !s.authorizeProjectScope(r.Context(), w, owner) || !s.authorizeProjectScope(r.Context(), w, project) {
		return
	}
	claims, ok := s.store.(promptPairClaimStore)
	if !ok {
		http.Error(w, "prompt pair claim store unavailable", http.StatusInternalServerError)
		return
	}
	authority, err := claims.GetSessionAuthority(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "session authority storage unavailable", http.StatusInternalServerError)
		return
	}
	if authority == nil || authority.OwnerProject != owner {
		sessionAuthorityUnavailable(w)
		return
	}
	actor := "legacy:authenticated"
	if usablePrincipal {
		actor = strings.TrimSpace(principal.ID)
	}
	if err := claims.ClaimPromptPair(r.Context(), sessionID, inboxID, syncID, project, actor); err != nil {
		if errors.Is(err, cloudstore.ErrPromptPairClaimConflict) {
			http.Error(w, "prompt pair claim conflict", http.StatusConflict)
			return
		}
		if errors.Is(err, cloudstore.ErrSessionAuthorityNotFound) {
			sessionAuthorityUnavailable(w)
			return
		}
		http.Error(w, "prompt pair claim storage unavailable", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok"})
}
