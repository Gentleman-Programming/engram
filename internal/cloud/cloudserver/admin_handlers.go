package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/constants"
)

const (
	authAuditOutcomeSuccess = "success"

	authAuditActionUserCreate  = "user.create"
	authAuditActionUserEnable  = "user.enable"
	authAuditActionUserDisable = "user.disable"
	authAuditActionTokenCreate = "token.create"
	authAuditActionTokenRevoke = "token.revoke"
	authAuditActionGrantCreate = "grant.create"
	authAuditActionGrantRevoke = "grant.revoke"
)

// AdminIdentityStore is the storage boundary used by cloudserver admin API
// handlers. CloudStore satisfies it through the identity methods added in the
// storage foundation slice.
type AdminIdentityStore interface {
	CreateHumanUser(ctx context.Context, params cloudstore.CreateHumanUserParams) (cloudstore.HumanUser, error)
	ListHumanUsers(ctx context.Context) ([]cloudstore.HumanUser, error)
	SetHumanUserEnabled(ctx context.Context, principalID string, enabled bool) error
	CreatePrincipalTokenWithAudit(ctx context.Context, params cloudstore.CreatePrincipalTokenParams, audit cloudstore.AuthAuditEvent) (cloudstore.PrincipalToken, error)
	ListPrincipalTokens(ctx context.Context, principalID string) ([]cloudstore.PrincipalToken, error)
	RevokePrincipalToken(ctx context.Context, tokenID, revokedByPrincipalID, reason string) error
	CreateProjectGrant(ctx context.Context, params cloudstore.CreateProjectGrantParams) (cloudstore.ProjectGrant, error)
	ListProjectGrants(ctx context.Context, principalID string) ([]cloudstore.ProjectGrant, error)
	RevokeProjectGrant(ctx context.Context, principalID, project string) error
	InsertAuthAuditEvent(ctx context.Context, event cloudstore.AuthAuditEvent) error
}

type createAdminUserRequest struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type createAdminTokenRequest struct {
	Name string `json:"name"`
}

type revokeAdminTokenRequest struct {
	Reason string `json:"reason"`
}

type createAdminGrantRequest struct {
	Project string `json:"project"`
}

type adminTokenMetadata struct {
	ID                   string     `json:"id"`
	PrincipalID          string     `json:"principal_id"`
	TokenPrefix          string     `json:"token_prefix"`
	Name                 string     `json:"name"`
	CreatedByPrincipalID string     `json:"created_by_principal_id"`
	CreatedAt            time.Time  `json:"created_at"`
	LastUsedAt           *time.Time `json:"last_used_at,omitempty"`
	RevokedAt            *time.Time `json:"revoked_at,omitempty"`
	RevokedByPrincipalID string     `json:"revoked_by_principal_id,omitempty"`
	RevocationReason     string     `json:"revocation_reason,omitempty"`
}

func (s *CloudServer) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireManagedAdmin(w, r); !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	users, err := store.ListHumanUsers(r.Context())
	if err != nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("list users: %v", err))
		return
	}
	jsonResponse(w, http.StatusOK, users)
}

func (s *CloudServer) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	var req createAdminUserRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid user payload: %v", err))
		return
	}
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = string(cloudauth.RoleMember)
	}
	user, err := store.CreateHumanUser(r.Context(), cloudstore.CreateHumanUserParams{Username: req.Username, Email: req.Email, DisplayName: req.DisplayName, Role: role})
	if err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("create user: %v", err))
		return
	}
	if err := s.recordAdminAudit(r.Context(), store, actor, authAuditActionUserCreate, user.PrincipalID, "", map[string]any{"kind": string(cloudauth.PrincipalKindHuman), "role": role, "username": strings.TrimSpace(req.Username)}); err != nil {
		writeAuditFailure(w, err)
		return
	}
	jsonResponse(w, http.StatusCreated, user)
}

func (s *CloudServer) handleAdminEnableUser(w http.ResponseWriter, r *http.Request) {
	s.handleAdminSetUserEnabled(w, r, true, authAuditActionUserEnable)
}

func (s *CloudServer) handleAdminDisableUser(w http.ResponseWriter, r *http.Request) {
	s.handleAdminSetUserEnabled(w, r, false, authAuditActionUserDisable)
}

func (s *CloudServer) handleAdminSetUserEnabled(w http.ResponseWriter, r *http.Request, enabled bool, action string) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	principalID := strings.TrimSpace(r.PathValue("principalID"))
	if principalID == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "principal id is required")
		return
	}
	if err := store.SetHumanUserEnabled(r.Context(), principalID, enabled); err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("set user enabled: %v", err))
		return
	}
	if err := s.recordAdminAudit(r.Context(), store, actor, action, principalID, "", map[string]any{"enabled": enabled}); err != nil {
		writeAuditFailure(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "principal_id": principalID, "enabled": enabled})
}

func (s *CloudServer) handleAdminListTokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireManagedAdmin(w, r); !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	principalID := strings.TrimSpace(r.PathValue("principalID"))
	if principalID == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "principal id is required")
		return
	}
	tokens, err := store.ListPrincipalTokens(r.Context(), principalID)
	if err != nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("list tokens: %v", err))
		return
	}
	jsonResponse(w, http.StatusOK, sanitizeTokenList(tokens))
}

func (s *CloudServer) handleAdminCreateToken(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	if s.managedHasher == nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, "managed token hasher is not configured")
		return
	}
	principalID := strings.TrimSpace(r.PathValue("principalID"))
	if principalID == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "principal id is required")
		return
	}
	var req createAdminTokenRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid token payload: %v", err))
		return
	}
	if user, found, err := findManagedUserByPrincipalID(r.Context(), store, principalID); err != nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("list users: %v", err))
		return
	} else if !found {
		writeActionableError(w, http.StatusNotFound, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "managed user not found")
		return
	} else if !user.Enabled {
		writeActionableError(w, http.StatusConflict, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, disabledManagedUserTokenMessage(principalID))
		return
	}
	managedToken, err := cloudauth.GenerateManagedToken("live")
	if err != nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("generate token: %v", err))
		return
	}
	tokenHash, err := s.managedHasher.Hash(managedToken.Raw)
	if err != nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("hash token: %v", err))
		return
	}
	token, err := store.CreatePrincipalTokenWithAudit(r.Context(),
		cloudstore.CreatePrincipalTokenParams{PrincipalID: principalID, TokenPrefix: managedToken.Prefix, TokenHash: tokenHash, Name: req.Name, CreatedByPrincipalID: actor.ID},
		adminAuditEvent(actor, authAuditActionTokenCreate, principalID, "", map[string]any{"token_prefix": managedToken.Prefix, "name": strings.TrimSpace(req.Name)}),
	)
	if err != nil {
		if errors.Is(err, cloudstore.ErrPrincipalDisabled) {
			writeActionableError(w, http.StatusConflict, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, disabledManagedUserTokenMessage(principalID))
			return
		}
		if errors.Is(err, cloudstore.ErrPrincipalNotFound) {
			writeActionableError(w, http.StatusNotFound, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "managed user not found")
			return
		}
		if errors.Is(err, cloudstore.ErrAuthAuditInsertFailed) {
			writeAuditFailure(w, err)
			return
		}
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("create token: %v", err))
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]any{"raw_token": managedToken.Raw, "token": sanitizeToken(token)})
}

func findManagedUserByPrincipalID(ctx context.Context, store AdminIdentityStore, principalID string) (cloudstore.HumanUser, bool, error) {
	users, err := store.ListHumanUsers(ctx)
	if err != nil {
		return cloudstore.HumanUser{}, false, err
	}
	for _, user := range users {
		if user.PrincipalID == principalID {
			return user, true, nil
		}
	}
	return cloudstore.HumanUser{}, false, nil
}

func disabledManagedUserTokenMessage(principalID string) string {
	return fmt.Sprintf("managed user %q is disabled; enable the user before creating a managed token", principalID)
}

func (s *CloudServer) handleAdminRevokeToken(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	tokenID := strings.TrimSpace(r.PathValue("tokenID"))
	if tokenID == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "token id is required")
		return
	}
	var req revokeAdminTokenRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid revoke payload: %v", err))
		return
	}
	if err := store.RevokePrincipalToken(r.Context(), tokenID, actor.ID, req.Reason); err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("revoke token: %v", err))
		return
	}
	if err := s.recordAdminAudit(r.Context(), store, actor, authAuditActionTokenRevoke, "", "", map[string]any{"id": tokenID, "reason": strings.TrimSpace(req.Reason)}); err != nil {
		writeAuditFailure(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "token_id": tokenID})
}

func (s *CloudServer) handleAdminListGrants(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireManagedAdmin(w, r); !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	principalID := strings.TrimSpace(r.PathValue("principalID"))
	if principalID == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "principal id is required")
		return
	}
	grants, err := store.ListProjectGrants(r.Context(), principalID)
	if err != nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("list grants: %v", err))
		return
	}
	jsonResponse(w, http.StatusOK, grants)
}

func (s *CloudServer) handleAdminCreateGrant(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	principalID := strings.TrimSpace(r.PathValue("principalID"))
	if principalID == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "principal id is required")
		return
	}
	var req createAdminGrantRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("invalid grant payload: %v", err))
		return
	}
	project := strings.TrimSpace(req.Project)
	if project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "project is required")
		return
	}
	grant, err := store.CreateProjectGrant(r.Context(), cloudstore.CreateProjectGrantParams{PrincipalID: principalID, Project: project, GrantedByPrincipalID: actor.ID})
	if err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("create grant: %v", err))
		return
	}
	if err := s.recordAdminAudit(r.Context(), store, actor, authAuditActionGrantCreate, principalID, project, map[string]any{"project": project}); err != nil {
		writeAuditFailure(w, err)
		return
	}
	jsonResponse(w, http.StatusCreated, grant)
}

func (s *CloudServer) handleAdminRevokeGrant(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireManagedAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.adminStore(w)
	if !ok {
		return
	}
	principalID := strings.TrimSpace(r.PathValue("principalID"))
	project := strings.TrimSpace(r.PathValue("project"))
	if principalID == "" || project == "" {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, "principal id and project are required")
		return
	}
	if err := store.RevokeProjectGrant(r.Context(), principalID, project); err != nil {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadInvalid, fmt.Sprintf("revoke grant: %v", err))
		return
	}
	if err := s.recordAdminAudit(r.Context(), store, actor, authAuditActionGrantRevoke, principalID, project, map[string]any{"project": project}); err != nil {
		writeAuditFailure(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "principal_id": principalID, "project": project})
}

func (s *CloudServer) requireManagedAdmin(w http.ResponseWriter, r *http.Request) (cloudauth.Principal, bool) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok || principal.Role != cloudauth.RoleAdmin || principal.Source != cloudauth.PrincipalSourceManagedToken {
		writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, "forbidden: managed admin principal is required")
		return cloudauth.Principal{}, false
	}
	return principal, true
}

func (s *CloudServer) adminStore(w http.ResponseWriter) (AdminIdentityStore, bool) {
	if s.adminIdentity == nil {
		writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, "admin identity store is not configured")
		return nil, false
	}
	return s.adminIdentity, true
}

func (s *CloudServer) recordAdminAudit(ctx context.Context, store AdminIdentityStore, actor cloudauth.Principal, action, targetPrincipalID, project string, metadata map[string]any) error {
	return store.InsertAuthAuditEvent(ctx, adminAuditEvent(actor, action, targetPrincipalID, project, metadata))
}

func adminAuditEvent(actor cloudauth.Principal, action, targetPrincipalID, project string, metadata map[string]any) cloudstore.AuthAuditEvent {
	return cloudstore.AuthAuditEvent{
		ActorPrincipalID:  strings.TrimSpace(actor.ID),
		ActorSource:       string(actor.Source),
		TargetPrincipalID: strings.TrimSpace(targetPrincipalID),
		Project:           strings.TrimSpace(project),
		Action:            strings.TrimSpace(action),
		Outcome:           authAuditOutcomeSuccess,
		Metadata:          metadata,
	}
}

func writeAuditFailure(w http.ResponseWriter, err error) {
	writeActionableError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, fmt.Sprintf("record auth audit: %v", err))
}

func decodeJSONBody(r *http.Request, dest any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

func sanitizeTokenList(tokens []cloudstore.PrincipalToken) []adminTokenMetadata {
	out := make([]adminTokenMetadata, 0, len(tokens))
	for _, token := range tokens {
		out = append(out, sanitizeToken(token))
	}
	return out
}

func sanitizeToken(token cloudstore.PrincipalToken) adminTokenMetadata {
	return adminTokenMetadata{
		ID:                   token.ID,
		PrincipalID:          token.PrincipalID,
		TokenPrefix:          token.TokenPrefix,
		Name:                 token.Name,
		CreatedByPrincipalID: token.CreatedByPrincipalID,
		CreatedAt:            token.CreatedAt,
		LastUsedAt:           token.LastUsedAt,
		RevokedAt:            token.RevokedAt,
		RevokedByPrincipalID: token.RevokedByPrincipalID,
		RevocationReason:     token.RevocationReason,
	}
}
