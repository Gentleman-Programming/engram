package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

type promptSourceAttestationVerifier interface {
	VerifyPromptSourceAttestation(context.Context, int64, string, string, string, string, string) (bool, error)
}

// handleVerifyPromptSourceAttestation checks a prior audit assertion without
// registering authority, claiming a pair, appending an audit row, or admitting deletion.
func (s *CloudServer) handleVerifyPromptSourceAttestation(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if s.auth == nil || !ok || strings.TrimSpace(principal.ID) == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if s.principalProject == nil {
		http.Error(w, "project authorization unavailable", http.StatusForbidden)
		return
	}
	var p struct {
		AuditID       int64  `json:"audit_id"`
		SessionID     string `json:"session_id"`
		SourceInboxID string `json:"source_inbox_id"`
		SyncID        string `json:"sync_id"`
		OwnerProject  string `json:"owner_project"`
		PromptProject string `json:"prompt_project"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.pushBodyLimit()))
	decoder.DisallowUnknownFields()
	invalid := func(err error) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "verification request too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid verification request", http.StatusBadRequest)
		}
	}
	if err := decoder.Decode(&p); err != nil {
		invalid(err)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		invalid(err)
		return
	}
	if p.AuditID <= 0 || strings.TrimSpace(p.SessionID) == "" || strings.TrimSpace(p.SourceInboxID) == "" || strings.TrimSpace(p.SyncID) == "" || strings.TrimSpace(p.OwnerProject) == "" || strings.TrimSpace(p.PromptProject) == "" {
		http.Error(w, "audit id and all verification fields are required", http.StatusBadRequest)
		return
	}
	owner, _ := store.NormalizeProject(strings.TrimSpace(p.OwnerProject))
	prompt, _ := store.NormalizeProject(strings.TrimSpace(p.PromptProject))
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(prompt) == "" {
		http.Error(w, "projects are required", http.StatusBadRequest)
		return
	}
	// Current grants to both projects must precede the global audit lookup.
	if err := s.principalProject.AuthorizeProjectForPrincipal(r.Context(), principal, owner); err != nil {
		http.Error(w, "owner project forbidden", http.StatusForbidden)
		return
	}
	if err := s.principalProject.AuthorizeProjectForPrincipal(r.Context(), principal, prompt); err != nil {
		http.Error(w, "prompt project forbidden", http.StatusForbidden)
		return
	}
	verifier, ok := s.store.(promptSourceAttestationVerifier)
	if !ok {
		http.Error(w, "attestation store unavailable", http.StatusInternalServerError)
		return
	}
	found, err := verifier.VerifyPromptSourceAttestation(r.Context(), p.AuditID, p.SessionID, p.SourceInboxID, p.SyncID, owner, prompt)
	if err != nil {
		http.Error(w, "verification storage unavailable", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "attestation binding conflict", http.StatusConflict)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "attestation_id": p.AuditID})
}

type promptSourceAttestationStore interface {
	RegisterSessionAuthority(context.Context, string, string, string) error
	ClaimPromptPair(context.Context, string, string, string, string, string) error
	AttestPromptSource(context.Context, string, string, string, string, string, string) (*cloudstore.PromptSourceAttestation, error)
}

func (s *CloudServer) handlePromptSourceAttestation(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if s.auth == nil || !ok || strings.TrimSpace(principal.ID) == "" {
		http.Error(w, "human authentication required", http.StatusUnauthorized)
		return
	}
	if principal.Kind != cloudauth.PrincipalKindHuman {
		http.Error(w, "human principal required", http.StatusForbidden)
		return
	}
	if s.principalProject == nil {
		http.Error(w, "project authorization unavailable", http.StatusForbidden)
		return
	}
	var p struct {
		SessionID     string `json:"session_id"`
		SourceInboxID string `json:"source_inbox_id"`
		SyncID        string `json:"sync_id"`
		OwnerProject  string `json:"owner_project"`
		PromptProject string `json:"prompt_project"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.pushBodyLimit()))
	decoder.DisallowUnknownFields()
	invalid := func(err error) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "attestation request too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid attestation request", http.StatusBadRequest)
		}
	}
	if err := decoder.Decode(&p); err != nil {
		invalid(err)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		invalid(err)
		return
	}
	session, inbox, syncID := strings.TrimSpace(p.SessionID), strings.TrimSpace(p.SourceInboxID), strings.TrimSpace(p.SyncID)
	owner, prompt := strings.TrimSpace(p.OwnerProject), strings.TrimSpace(p.PromptProject)
	if session == "" || inbox == "" || syncID == "" || owner == "" || prompt == "" {
		http.Error(w, "all attestation fields are required", http.StatusBadRequest)
		return
	}
	owner, _ = store.NormalizeProject(owner)
	prompt, _ = store.NormalizeProject(prompt)
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(prompt) == "" {
		http.Error(w, "projects are required", http.StatusBadRequest)
		return
	}
	// Both current grants precede any global session identity access.
	if err := s.principalProject.AuthorizeProjectForPrincipal(r.Context(), principal, owner); err != nil {
		http.Error(w, "owner project forbidden", http.StatusForbidden)
		return
	}
	if err := s.principalProject.AuthorizeProjectForPrincipal(r.Context(), principal, prompt); err != nil {
		http.Error(w, "prompt project forbidden", http.StatusForbidden)
		return
	}
	attestations, ok := s.store.(promptSourceAttestationStore)
	if !ok {
		http.Error(w, "attestation store unavailable", http.StatusInternalServerError)
		return
	}
	actor := strings.TrimSpace(principal.ID)
	if err := attestations.RegisterSessionAuthority(r.Context(), session, owner, actor); err != nil {
		if errors.Is(err, cloudstore.ErrSessionAuthorityConflict) {
			http.Error(w, "session authority conflict", http.StatusConflict)
		} else {
			http.Error(w, "registration storage unavailable", http.StatusInternalServerError)
		}
		return
	}
	if err := attestations.ClaimPromptPair(r.Context(), session, inbox, syncID, prompt, actor); err != nil {
		if errors.Is(err, cloudstore.ErrPromptPairClaimConflict) {
			http.Error(w, "prompt pair conflict", http.StatusConflict)
		} else if errors.Is(err, cloudstore.ErrSessionAuthorityNotFound) {
			sessionAuthorityUnavailable(w)
		} else {
			http.Error(w, "claim storage unavailable", http.StatusInternalServerError)
		}
		return
	}
	record, err := attestations.AttestPromptSource(r.Context(), session, inbox, syncID, owner, prompt, actor)
	if err != nil {
		if errors.Is(err, cloudstore.ErrPromptSourceAttestationUnbound) {
			http.Error(w, "attestation binding conflict", http.StatusConflict)
		} else {
			http.Error(w, "attestation storage unavailable", http.StatusInternalServerError)
		}
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "attestation_id": record.ID})
}
