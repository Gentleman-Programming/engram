package cloudserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

type adminTestStore struct {
	fakeStore

	users             []cloudstore.HumanUser
	tokens            []cloudstore.PrincipalToken
	grants            []cloudstore.ProjectGrant
	auditEvents       []cloudstore.AuthAuditEvent
	auditErr          error
	createUserErr     error
	listHumanUsersErr error

	// auditMu guards auditEvents against concurrent InsertAuthAuditEvent
	// calls. Real cloudstore-backed audit inserts are independent Postgres
	// INSERTs with no shared in-process state; this fake's plain slice needs
	// its own lock to be safely callable from concurrent goroutines (see
	// TestDashboardBootstrapSubmitConcurrentRequestsCreateExactlyOneAdmin).
	auditMu sync.Mutex

	// PR4 review remediation (FIX C): per-method error injection so tests can
	// prove mutation handlers surface store failures as errors instead of
	// partial success, without adding a partial-success audit event.
	setEnabledErr  error
	createTokenErr error
	revokeTokenErr error
	createGrantErr error
	revokeGrantErr error

	createUserCalls  int
	setEnabledCalls  int
	createTokenCalls int
	revokeTokenCalls int
	createGrantCalls int
	revokeGrantCalls int
}

func newAdminTestStore() *adminTestStore {
	return &adminTestStore{fakeStore: fakeStore{chunks: make(map[string][]byte)}}
}

func (s *adminTestStore) CreateHumanUser(_ context.Context, params cloudstore.CreateHumanUserParams) (cloudstore.HumanUser, error) {
	s.createUserCalls++
	if s.createUserErr != nil {
		return cloudstore.HumanUser{}, s.createUserErr
	}
	user := cloudstore.HumanUser{
		PrincipalID: "p-user-" + params.Username,
		Username:    strings.TrimSpace(params.Username),
		Email:       strings.TrimSpace(params.Email),
		DisplayName: strings.TrimSpace(params.DisplayName),
		Role:        strings.TrimSpace(params.Role),
		Enabled:     true,
		CreatedAt:   time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
	}
	if user.DisplayName == "" {
		user.DisplayName = user.Username
	}
	s.users = append(s.users, user)
	return user, nil
}

func (s *adminTestStore) ListHumanUsers(context.Context) ([]cloudstore.HumanUser, error) {
	if s.listHumanUsersErr != nil {
		return nil, s.listHumanUsersErr
	}
	return append([]cloudstore.HumanUser(nil), s.users...), nil
}

func (s *adminTestStore) SetHumanUserEnabled(_ context.Context, principalID string, enabled bool) error {
	s.setEnabledCalls++
	if s.setEnabledErr != nil {
		return s.setEnabledErr
	}
	for i := range s.users {
		if s.users[i].PrincipalID == principalID {
			s.users[i].Enabled = enabled
			return nil
		}
	}
	return errors.New("human user not found")
}

func (s *adminTestStore) CreatePrincipalToken(_ context.Context, params cloudstore.CreatePrincipalTokenParams) (cloudstore.PrincipalToken, error) {
	s.createTokenCalls++
	if s.createTokenErr != nil {
		return cloudstore.PrincipalToken{}, s.createTokenErr
	}
	token := s.principalTokenFromParams(params)
	s.tokens = append(s.tokens, token)
	return token, nil
}

func (s *adminTestStore) CreatePrincipalTokenWithAudit(_ context.Context, params cloudstore.CreatePrincipalTokenParams, audit cloudstore.AuthAuditEvent) (cloudstore.PrincipalToken, error) {
	s.createTokenCalls++
	if s.createTokenErr != nil {
		return cloudstore.PrincipalToken{}, s.createTokenErr
	}
	if s.auditErr != nil {
		return cloudstore.PrincipalToken{}, fmt.Errorf("%w: %w", cloudstore.ErrAuthAuditInsertFailed, s.auditErr)
	}
	if strings.TrimSpace(audit.ActorSource) == "" {
		return cloudstore.PrincipalToken{}, fmt.Errorf("%w: actor source is required", cloudstore.ErrAuthAuditInsertFailed)
	}
	token := s.principalTokenFromParams(params)
	s.auditMu.Lock()
	s.auditEvents = append(s.auditEvents, audit)
	s.auditMu.Unlock()
	s.tokens = append(s.tokens, token)
	return token, nil
}

func (s *adminTestStore) principalTokenFromParams(params cloudstore.CreatePrincipalTokenParams) cloudstore.PrincipalToken {
	return cloudstore.PrincipalToken{
		ID:                   "tok-" + params.PrincipalID,
		PrincipalID:          strings.TrimSpace(params.PrincipalID),
		TokenPrefix:          strings.TrimSpace(params.TokenPrefix),
		TokenHash:            "",
		Name:                 strings.TrimSpace(params.Name),
		CreatedByPrincipalID: strings.TrimSpace(params.CreatedByPrincipalID),
		CreatedAt:            time.Date(2026, 7, 3, 12, 1, 0, 0, time.UTC),
	}
}

func (s *adminTestStore) ListPrincipalTokens(_ context.Context, principalID string) ([]cloudstore.PrincipalToken, error) {
	out := make([]cloudstore.PrincipalToken, 0)
	for _, token := range s.tokens {
		if token.PrincipalID == principalID {
			out = append(out, token)
		}
	}
	return out, nil
}

func (s *adminTestStore) RevokePrincipalToken(_ context.Context, tokenID, revokedByPrincipalID, reason string) error {
	s.revokeTokenCalls++
	if s.revokeTokenErr != nil {
		return s.revokeTokenErr
	}
	now := time.Date(2026, 7, 3, 12, 2, 0, 0, time.UTC)
	for i := range s.tokens {
		if s.tokens[i].ID == tokenID {
			s.tokens[i].RevokedAt = &now
			s.tokens[i].RevokedByPrincipalID = strings.TrimSpace(revokedByPrincipalID)
			s.tokens[i].RevocationReason = strings.TrimSpace(reason)
			return nil
		}
	}
	return errors.New("token not found")
}

func (s *adminTestStore) CreateProjectGrant(_ context.Context, params cloudstore.CreateProjectGrantParams) (cloudstore.ProjectGrant, error) {
	s.createGrantCalls++
	if s.createGrantErr != nil {
		return cloudstore.ProjectGrant{}, s.createGrantErr
	}
	grant := cloudstore.ProjectGrant{
		PrincipalID:          strings.TrimSpace(params.PrincipalID),
		Project:              strings.TrimSpace(params.Project),
		GrantedByPrincipalID: strings.TrimSpace(params.GrantedByPrincipalID),
		CreatedAt:            time.Date(2026, 7, 3, 12, 3, 0, 0, time.UTC),
	}
	s.grants = append(s.grants, grant)
	return grant, nil
}

func (s *adminTestStore) ListProjectGrants(_ context.Context, principalID string) ([]cloudstore.ProjectGrant, error) {
	out := make([]cloudstore.ProjectGrant, 0)
	for _, grant := range s.grants {
		if grant.PrincipalID == principalID {
			out = append(out, grant)
		}
	}
	return out, nil
}

func (s *adminTestStore) RevokeProjectGrant(_ context.Context, principalID, project string) error {
	s.revokeGrantCalls++
	if s.revokeGrantErr != nil {
		return s.revokeGrantErr
	}
	kept := s.grants[:0]
	for _, grant := range s.grants {
		if grant.PrincipalID == principalID && grant.Project == project {
			continue
		}
		kept = append(kept, grant)
	}
	s.grants = kept
	return nil
}

// InsertAuthAuditEvent replicates the real cloudstore.InsertAuthAuditEvent
// validation (actor source is required) so tests catch call sites that would
// silently fail to persist an audit event against the production store.
func (s *adminTestStore) InsertAuthAuditEvent(_ context.Context, event cloudstore.AuthAuditEvent) error {
	if s.auditErr != nil {
		return s.auditErr
	}
	if strings.TrimSpace(event.ActorSource) == "" {
		return errors.New("actor source is required")
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	s.auditEvents = append(s.auditEvents, event)
	return nil
}

func adminHandlerTestServer(t *testing.T, principal cloudauth.Principal, store *adminTestStore) *CloudServer {
	t.Helper()
	hasher, err := cloudauth.NewManagedTokenHasher([]byte("test-token-pepper-at-least-32-bytes"))
	if err != nil {
		t.Fatalf("new token hasher: %v", err)
	}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"actor-token": principal}}
	return New(store, authn, 0, WithAdminIdentityStore(store), WithManagedTokenHasher(hasher))
}

func performAdminRequest(srv *CloudServer, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer actor-token")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAdminHandlersRequireManagedAdminAndLeaveNoStateForMembers(t *testing.T) {
	forbiddenPrincipals := []struct {
		name      string
		principal cloudauth.Principal
	}{
		{name: "managed member", principal: cloudauth.Principal{ID: "p-member", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleMember, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}},
		{name: "legacy admin", principal: cloudauth.Principal{ID: "legacy:admin", Kind: cloudauth.PrincipalKindLegacy, Role: cloudauth.RoleAdmin, Source: cloudauth.PrincipalSourceLegacyEnvAdmin, Enabled: true}},
		{name: "bootstrap cli human admin", principal: cloudauth.Principal{ID: "p-bootstrap", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleAdmin, Source: cloudauth.PrincipalSourceBootstrapCLI, Enabled: true}},
		{name: "insecure dev service admin", principal: cloudauth.Principal{ID: "p-dev", Kind: cloudauth.PrincipalKindServiceAccount, Role: cloudauth.RoleAdmin, Source: cloudauth.PrincipalSourceInsecureDev, Enabled: true}},
	}

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create user", method: http.MethodPost, path: "/admin/users", body: `{"username":"alice","role":"member"}`},
		{name: "disable user", method: http.MethodPost, path: "/admin/users/p-target/disable"},
		{name: "enable user", method: http.MethodPost, path: "/admin/users/p-target/enable"},
		{name: "create token", method: http.MethodPost, path: "/admin/users/p-target/tokens", body: `{"name":"laptop"}`},
		{name: "revoke token", method: http.MethodPost, path: "/admin/tokens/tok-target/revoke", body: `{"reason":"lost"}`},
		{name: "create grant", method: http.MethodPost, path: "/admin/users/p-target/grants", body: `{"project":"beta"}`},
		{name: "revoke grant", method: http.MethodPost, path: "/admin/users/p-target/grants/alpha/revoke"},
	}

	for _, forbidden := range forbiddenPrincipals {
		t.Run(forbidden.name, func(t *testing.T) {
			store := newAdminTestStore()
			store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target", DisplayName: "Target", Role: "member", Enabled: true})
			store.tokens = append(store.tokens, cloudstore.PrincipalToken{ID: "tok-target", PrincipalID: "p-target", TokenPrefix: "egc_live_safe", Name: "sync"})
			store.grants = append(store.grants, cloudstore.ProjectGrant{PrincipalID: "p-target", Project: "alpha"})
			srv := adminHandlerTestServer(t, forbidden.principal, store)

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					rec := performAdminRequest(srv, tc.method, tc.path, tc.body)
					if rec.Code != http.StatusForbidden {
						t.Fatalf("expected forbidden response, got %d body=%q", rec.Code, rec.Body.String())
					}
				})
			}

			if store.createUserCalls != 0 || store.setEnabledCalls != 0 || store.createTokenCalls != 0 || store.revokeTokenCalls != 0 || store.createGrantCalls != 0 || store.revokeGrantCalls != 0 {
				t.Fatalf("forbidden request mutated state: user=%d enabled=%d token=%d revokeToken=%d grant=%d revokeGrant=%d", store.createUserCalls, store.setEnabledCalls, store.createTokenCalls, store.revokeTokenCalls, store.createGrantCalls, store.revokeGrantCalls)
			}
			if len(store.auditEvents) != 0 {
				t.Fatalf("forbidden requests must not create success audit events, got %+v", store.auditEvents)
			}
			if len(store.users) != 1 || len(store.tokens) != 1 || len(store.grants) != 1 {
				t.Fatalf("forbidden request changed collections: users=%d tokens=%d grants=%d", len(store.users), len(store.tokens), len(store.grants))
			}
		})
	}
}

func TestAdminHandlersManageUsersTokensAndGrantsWithRedactedResponses(t *testing.T) {
	admin := cloudauth.Principal{ID: "p-admin", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleAdmin, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	store := newAdminTestStore()
	srv := adminHandlerTestServer(t, admin, store)

	createUser := performAdminRequest(srv, http.MethodPost, "/admin/users", `{"username":"alice","email":"alice@example.com","display_name":"Alice","role":"member"}`)
	if createUser.Code != http.StatusCreated {
		t.Fatalf("expected create user 201, got %d body=%q", createUser.Code, createUser.Body.String())
	}
	var userResp cloudstore.HumanUser
	if err := json.Unmarshal(createUser.Body.Bytes(), &userResp); err != nil {
		t.Fatalf("decode user response: %v", err)
	}
	if userResp.PrincipalID != "p-user-alice" || userResp.Username != "alice" || userResp.Role != "member" || !userResp.Enabled {
		t.Fatalf("unexpected created user response: %+v", userResp)
	}

	listUsers := performAdminRequest(srv, http.MethodGet, "/admin/users", "")
	if listUsers.Code != http.StatusOK {
		t.Fatalf("expected list users 200, got %d body=%q", listUsers.Code, listUsers.Body.String())
	}
	var users []cloudstore.HumanUser
	if err := json.Unmarshal(listUsers.Body.Bytes(), &users); err != nil {
		t.Fatalf("decode users: %v", err)
	}
	if len(users) != 1 || users[0].PrincipalID != "p-user-alice" {
		t.Fatalf("expected created user in list, got %+v", users)
	}

	disableUser := performAdminRequest(srv, http.MethodPost, "/admin/users/p-user-alice/disable", "")
	if disableUser.Code != http.StatusOK {
		t.Fatalf("expected disable user 200, got %d body=%q", disableUser.Code, disableUser.Body.String())
	}
	if store.users[0].Enabled {
		t.Fatalf("disable handler should disable the user")
	}
	enableUser := performAdminRequest(srv, http.MethodPost, "/admin/users/p-user-alice/enable", "")
	if enableUser.Code != http.StatusOK {
		t.Fatalf("expected enable user 200, got %d body=%q", enableUser.Code, enableUser.Body.String())
	}
	if !store.users[0].Enabled {
		t.Fatalf("enable handler should enable the user")
	}

	createToken := performAdminRequest(srv, http.MethodPost, "/admin/users/p-user-alice/tokens", `{"name":"laptop"}`)
	if createToken.Code != http.StatusCreated {
		t.Fatalf("expected create token 201, got %d body=%q", createToken.Code, createToken.Body.String())
	}
	var tokenCreate struct {
		RawToken string             `json:"raw_token"`
		Token    adminTokenMetadata `json:"token"`
	}
	if err := json.Unmarshal(createToken.Body.Bytes(), &tokenCreate); err != nil {
		t.Fatalf("decode token create: %v", err)
	}
	if !strings.HasPrefix(tokenCreate.RawToken, "egc_live_") {
		t.Fatalf("expected raw managed token only in create response, got %q", tokenCreate.RawToken)
	}
	if strings.Contains(string(createToken.Body.Bytes()), "token_hash") {
		t.Fatalf("token create response must not expose token hash, body=%q", createToken.Body.String())
	}
	if !strings.Contains(createToken.Body.String(), "\"token_prefix\"") {
		t.Fatalf("expected snake_case token metadata, body=%q", createToken.Body.String())
	}
	if tokenCreate.Token.TokenPrefix == "" || !strings.HasPrefix(tokenCreate.RawToken, tokenCreate.Token.TokenPrefix+"_") {
		t.Fatalf("expected safe token prefix metadata, response=%+v", tokenCreate)
	}

	listTokens := performAdminRequest(srv, http.MethodGet, "/admin/users/p-user-alice/tokens", "")
	if listTokens.Code != http.StatusOK {
		t.Fatalf("expected list tokens 200, got %d body=%q", listTokens.Code, listTokens.Body.String())
	}
	if strings.Contains(listTokens.Body.String(), tokenCreate.RawToken) || strings.Contains(listTokens.Body.String(), "token_hash") || strings.Contains(listTokens.Body.String(), "raw_token") {
		t.Fatalf("token list must expose metadata only, body=%q raw=%q", listTokens.Body.String(), tokenCreate.RawToken)
	}

	createGrant := performAdminRequest(srv, http.MethodPost, "/admin/users/p-user-alice/grants", `{"project":"alpha"}`)
	if createGrant.Code != http.StatusCreated {
		t.Fatalf("expected create grant 201, got %d body=%q", createGrant.Code, createGrant.Body.String())
	}
	listGrants := performAdminRequest(srv, http.MethodGet, "/admin/users/p-user-alice/grants", "")
	if listGrants.Code != http.StatusOK {
		t.Fatalf("expected list grants 200, got %d body=%q", listGrants.Code, listGrants.Body.String())
	}
	var grants []cloudstore.ProjectGrant
	if err := json.Unmarshal(listGrants.Body.Bytes(), &grants); err != nil {
		t.Fatalf("decode grants: %v", err)
	}
	if len(grants) != 1 || grants[0].Project != "alpha" || grants[0].GrantedByPrincipalID != "p-admin" {
		t.Fatalf("unexpected grants: %+v", grants)
	}

	revokeGrant := performAdminRequest(srv, http.MethodPost, "/admin/users/p-user-alice/grants/alpha/revoke", "")
	if revokeGrant.Code != http.StatusOK {
		t.Fatalf("expected revoke grant 200, got %d body=%q", revokeGrant.Code, revokeGrant.Body.String())
	}
	if len(store.grants) != 0 {
		t.Fatalf("expected grant revoked, got %+v", store.grants)
	}

	revokeToken := performAdminRequest(srv, http.MethodPost, "/admin/tokens/tok-p-user-alice/revoke", `{"reason":"lost"}`)
	if revokeToken.Code != http.StatusOK {
		t.Fatalf("expected revoke token 200, got %d body=%q", revokeToken.Code, revokeToken.Body.String())
	}
	if store.tokens[0].RevokedAt == nil || store.tokens[0].RevokedByPrincipalID != "p-admin" || store.tokens[0].RevocationReason != "lost" {
		t.Fatalf("expected token revoked with actor and reason, got %+v", store.tokens[0])
	}
}

func TestAdminCreateTokenRejectsUnknownManagedUserWithoutMinting(t *testing.T) {
	admin := cloudauth.Principal{ID: "p-admin", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleAdmin, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	store := newAdminTestStore()
	srv := adminHandlerTestServer(t, admin, store)

	rec := performAdminRequest(srv, http.MethodPost, "/admin/users/p-missing/tokens", `{"name":"laptop"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown managed user token create, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "raw_token") || strings.Contains(body, "egc_live_") {
		t.Fatalf("unknown-user rejection must not expose raw token material, body=%q", body)
	}
	if store.createTokenCalls != 0 {
		t.Fatalf("unknown-user rejection must happen before CreatePrincipalToken, got %d calls", store.createTokenCalls)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("unknown-user rejection must not persist token metadata, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("unknown-user rejection must not record token.create success audit, got %+v", event)
		}
	}
}

func TestAdminCreateTokenRejectsDisabledManagedUserWithoutMinting(t *testing.T) {
	admin := cloudauth.Principal{ID: "p-admin", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleAdmin, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	store := newAdminTestStore()
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-disabled", Username: "disabled", DisplayName: "Disabled", Role: "member", Enabled: false})
	srv := adminHandlerTestServer(t, admin, store)

	rec := performAdminRequest(srv, http.MethodPost, "/admin/users/p-disabled/tokens", `{"name":"laptop"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict for disabled managed user token create, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "raw_token") || strings.Contains(body, "egc_live_") {
		t.Fatalf("disabled-user rejection must not expose raw token material, body=%q", body)
	}
	if store.createTokenCalls != 0 {
		t.Fatalf("disabled-user rejection must happen before CreatePrincipalToken, got %d calls", store.createTokenCalls)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("disabled-user rejection must not persist token metadata, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("disabled-user rejection must not record token.create success audit, got %+v", event)
		}
	}
}

func TestAdminCreateTokenListUsersFailureFailsClosedBeforeMinting(t *testing.T) {
	admin := cloudauth.Principal{ID: "p-admin", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleAdmin, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	store := newAdminTestStore()
	store.listHumanUsersErr = errors.New("list unavailable")
	srv := adminHandlerTestServer(t, admin, store)

	rec := performAdminRequest(srv, http.MethodPost, "/admin/users/p-target/tokens", `{"name":"laptop"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when user listing fails before token create, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "raw_token") || strings.Contains(body, "egc_live_") {
		t.Fatalf("list failure must not expose raw token material, body=%q", body)
	}
	if store.createTokenCalls != 0 {
		t.Fatalf("list failure must happen before CreatePrincipalToken, got %d calls", store.createTokenCalls)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("list failure must not persist token metadata, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("list failure must not record token.create success audit, got %+v", event)
		}
	}
}

func TestAdminCreateTokenStoreDisabledRaceReturns409WithoutPersistingOrAuditing(t *testing.T) {
	admin := cloudauth.Principal{ID: "p-admin", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleAdmin, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	store := newAdminTestStore()
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-race", Username: "race", DisplayName: "Race", Role: "member", Enabled: true})
	store.createTokenErr = cloudstore.ErrPrincipalDisabled
	srv := adminHandlerTestServer(t, admin, store)

	rec := performAdminRequest(srv, http.MethodPost, "/admin/users/p-race/tokens", `{"name":"laptop"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when store rejects disabled principal after pre-check, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "raw_token") || strings.Contains(body, "egc_live_") {
		t.Fatalf("store-level disabled rejection must not expose raw token material, body=%q", body)
	}
	if store.createTokenCalls != 1 {
		t.Fatalf("expected store boundary to be reached once for race simulation, got %d calls", store.createTokenCalls)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("store-level disabled rejection must not persist token metadata, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("store-level disabled rejection must not record token.create success audit, got %+v", event)
		}
	}
}

func TestAdminCreateTokenStoreNotFoundRaceReturns404WithoutPersistingOrAuditing(t *testing.T) {
	admin := cloudauth.Principal{ID: "p-admin", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleAdmin, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	store := newAdminTestStore()
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-race", Username: "race", DisplayName: "Race", Role: "member", Enabled: true})
	store.createTokenErr = cloudstore.ErrPrincipalNotFound
	srv := adminHandlerTestServer(t, admin, store)

	rec := performAdminRequest(srv, http.MethodPost, "/admin/users/p-race/tokens", `{"name":"laptop"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when store cannot find principal after pre-check, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "raw_token") || strings.Contains(body, "egc_live_") {
		t.Fatalf("store-level not-found rejection must not expose raw token material, body=%q", body)
	}
	if store.createTokenCalls != 1 {
		t.Fatalf("expected store boundary to be reached once for race simulation, got %d calls", store.createTokenCalls)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("store-level not-found rejection must not persist token metadata, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("store-level not-found rejection must not record token.create success audit, got %+v", event)
		}
	}
}

func TestAdminCreateTokenAuditFailureRollsBackTokenAndDoesNotReturnRawToken(t *testing.T) {
	admin := cloudauth.Principal{ID: "p-admin", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleAdmin, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	store := newAdminTestStore()
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target", DisplayName: "Target", Role: "member", Enabled: true})
	store.auditErr = errors.New("audit unavailable")
	srv := adminHandlerTestServer(t, admin, store)

	rec := performAdminRequest(srv, http.MethodPost, "/admin/users/p-target/tokens", `{"name":"laptop"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected audit failure to fail token creation, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "raw_token") || strings.Contains(body, "egc_live_") {
		t.Fatalf("audit failure must not expose show-once token material, body=%q", body)
	}
	if store.createTokenCalls != 1 {
		t.Fatalf("expected atomic token create to be attempted once, got %d calls", store.createTokenCalls)
	}
	if store.revokeTokenCalls != 0 {
		t.Fatalf("token audit atomicity must not rely on compensation revoke, got %d revoke calls", store.revokeTokenCalls)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("audit failure must roll back token persistence, got %+v", store.tokens)
	}
	for _, event := range store.auditEvents {
		if event.Action == authAuditActionTokenCreate {
			t.Fatalf("audit failure must not record token.create success audit, got %+v", event)
		}
	}
}

func TestAdminMutationsAreSynchronouslyAuditedAndFailClosedOnAuditErrors(t *testing.T) {
	admin := cloudauth.Principal{ID: "p-admin", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleAdmin, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	store := newAdminTestStore()
	store.users = append(store.users, cloudstore.HumanUser{PrincipalID: "p-target", Username: "target", DisplayName: "Target", Role: "member", Enabled: true})
	store.tokens = append(store.tokens, cloudstore.PrincipalToken{ID: "tok-target", PrincipalID: "p-target", TokenPrefix: "egc_live_safe", Name: "sync"})
	srv := adminHandlerTestServer(t, admin, store)

	mutations := []struct {
		name   string
		method string
		path   string
		body   string
		action string
	}{
		{name: "user create", method: http.MethodPost, path: "/admin/users", body: `{"username":"audited","role":"member"}`, action: "user.create"},
		{name: "user disable", method: http.MethodPost, path: "/admin/users/p-target/disable", action: "user.disable"},
		{name: "user enable", method: http.MethodPost, path: "/admin/users/p-target/enable", action: "user.enable"},
		{name: "token create", method: http.MethodPost, path: "/admin/users/p-target/tokens", body: `{"name":"workstation"}`, action: "token.create"},
		{name: "token revoke", method: http.MethodPost, path: "/admin/tokens/tok-target/revoke", body: `{"reason":"rotation"}`, action: "token.revoke"},
		{name: "grant create", method: http.MethodPost, path: "/admin/users/p-target/grants", body: `{"project":"alpha"}`, action: "grant.create"},
		{name: "grant revoke", method: http.MethodPost, path: "/admin/users/p-target/grants/alpha/revoke", action: "grant.revoke"},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			beforeAudit := len(store.auditEvents)
			rec := performAdminRequest(srv, mutation.method, mutation.path, mutation.body)
			if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
				t.Fatalf("expected mutation success, got %d body=%q", rec.Code, rec.Body.String())
			}
			if len(store.auditEvents) != beforeAudit+1 {
				t.Fatalf("expected one audit event for %s, before=%d after=%d", mutation.action, beforeAudit, len(store.auditEvents))
			}
			event := store.auditEvents[len(store.auditEvents)-1]
			if event.ActorPrincipalID != "p-admin" || event.ActorSource != string(cloudauth.PrincipalSourceManagedToken) || event.Action != mutation.action || event.Outcome != "success" {
				t.Fatalf("unexpected audit event: %+v", event)
			}
			encodedMetadata, err := json.Marshal(event.Metadata)
			if err != nil {
				t.Fatalf("marshal audit metadata: %v", err)
			}
			metadata := strings.ToLower(string(encodedMetadata))
			if strings.Contains(metadata, "raw_token") || strings.Contains(metadata, "token_hash") || strings.Contains(metadata, "token_name") || strings.Contains(metadata, "token_id") || strings.Contains(metadata, "bearer") || strings.Contains(metadata, "authorization") {
				t.Fatalf("audit metadata leaked or used rejected token metadata keys: %s", metadata)
			}
		})
	}

	failedStore := newAdminTestStore()
	failedStore.createUserErr = errors.New("user store unavailable")
	failedSrv := adminHandlerTestServer(t, admin, failedStore)
	failed := performAdminRequest(failedSrv, http.MethodPost, "/admin/users", `{"username":"failed","role":"member"}`)
	if failed.Code != http.StatusBadRequest {
		t.Fatalf("expected store failure to fail mutation, got %d body=%q", failed.Code, failed.Body.String())
	}
	if len(failedStore.auditEvents) != 0 {
		t.Fatalf("failed authoritative mutation must not create success audit events: %+v", failedStore.auditEvents)
	}

	blockedStore := newAdminTestStore()
	blockedStore.auditErr = errors.New("audit unavailable")
	blockedSrv := adminHandlerTestServer(t, admin, blockedStore)
	blocked := performAdminRequest(blockedSrv, http.MethodPost, "/admin/users", `{"username":"blocked","role":"member"}`)
	if blocked.Code != http.StatusInternalServerError {
		t.Fatalf("expected audit failure to fail response, got %d body=%q", blocked.Code, blocked.Body.String())
	}
	if blockedStore.createUserCalls != 1 || len(blockedStore.users) != 1 {
		t.Fatalf("post-mutation audit failure should occur after authoritative mutation, calls=%d users=%+v", blockedStore.createUserCalls, blockedStore.users)
	}
}
