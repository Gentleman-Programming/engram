package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

type attestationTestStore struct {
	fakeStore
	registration, claim, attest          int
	actor                                string
	registrationErr, claimErr, attestErr error
}

func (s *attestationTestStore) RegisterSessionAuthority(_ context.Context, _, _, actor string) error {
	s.registration++
	s.actor = actor
	return s.registrationErr
}
func (s *attestationTestStore) ClaimPromptPair(_ context.Context, _, _, _, _, actor string) error {
	s.claim++
	s.actor = actor
	return s.claimErr
}
func (s *attestationTestStore) AttestPromptSource(_ context.Context, _, _, _, _, _, actor string) (*cloudstore.PromptSourceAttestation, error) {
	s.attest++
	s.actor = actor
	if s.attestErr != nil {
		return nil, s.attestErr
	}
	return &cloudstore.PromptSourceAttestation{ID: 42, ActorID: actor}, nil
}

type verifyAttestationStore struct {
	attestationTestStore
	calls  int
	found  bool
	err    error
	id     int64
	fields [5]string
}

func (s *verifyAttestationStore) VerifyPromptSourceAttestation(_ context.Context, id int64, session, inbox, syncID, owner, prompt string) (bool, error) {
	s.calls++
	s.id = id
	s.fields = [5]string{session, inbox, syncID, owner, prompt}
	return s.found, s.err
}

func TestVerifyPromptSourceAttestationRoute(t *testing.T) {
	body := `{"audit_id":42,"session_id":"session","source_inbox_id":"inbox","sync_id":"sync","owner_project":"alpha","prompt_project":"beta"}`
	human := cloudauth.Principal{ID: "human", Kind: cloudauth.PrincipalKindHuman, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	service := cloudauth.Principal{ID: "service", Kind: cloudauth.PrincipalKindServiceAccount, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	cases := []struct {
		name, body    string
		principal     cloudauth.Principal
		grants        []string
		found         bool
		err           error
		limit         int64
		status, calls int
	}{
		{name: "human exact", body: body, principal: human, grants: []string{"alpha", "beta"}, found: true, status: 200, calls: 1},
		{name: "service exact", body: body, principal: service, grants: []string{"alpha", "beta"}, found: true, status: 200, calls: 1},
		{name: "missing row", body: body, principal: human, grants: []string{"alpha", "beta"}, status: 409, calls: 1},
		{name: "store error", body: body, principal: human, grants: []string{"alpha", "beta"}, err: errors.New("unavailable"), status: 500, calls: 1},
		{name: "owner denied", body: body, principal: human, grants: []string{"beta"}, status: 403},
		{name: "prompt denied", body: body, principal: human, grants: []string{"alpha"}, status: 403},
		{name: "missing principal", body: body, status: 401},
		{name: "unidentified", body: body, principal: cloudauth.Principal{Kind: cloudauth.PrincipalKindServiceAccount}, status: 401},
		{name: "zero id", body: strings.Replace(body, `42`, `0`, 1), principal: human, grants: []string{"alpha", "beta"}, status: 400},
		{name: "fractional id", body: strings.Replace(body, `42`, `1.5`, 1), principal: human, grants: []string{"alpha", "beta"}, status: 400},
		{name: "missing field", body: strings.Replace(body, `"sync"`, `""`, 1), principal: human, grants: []string{"alpha", "beta"}, status: 400},
		{name: "unknown field", body: strings.TrimSuffix(body, "}") + `,"actor_id":"spoof"}`, principal: human, grants: []string{"alpha", "beta"}, status: 400},
		{name: "trailing", body: body + ` {}`, principal: human, grants: []string{"alpha", "beta"}, status: 400},
		{name: "oversized", body: body, principal: human, grants: []string{"alpha", "beta"}, limit: 20, status: 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &verifyAttestationStore{found: tc.found, err: tc.err}
			opts := []Option{WithPrincipalProjectAuthorizer(managedGrantAuthorizer{grants: map[string][]string{tc.principal.ID: tc.grants}})}
			if tc.limit > 0 {
				opts = append(opts, WithMaxPushBodyBytes(tc.limit))
			}
			srv := New(st, claimAuthOnly{}, 0, opts...)
			req := httptest.NewRequest(http.MethodPost, "/sync/prompt-source-attestations/verify", strings.NewReader(tc.body))
			req = req.WithContext(WithPrincipal(req.Context(), tc.principal))
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != tc.status || st.calls != tc.calls || st.registration != 0 || st.claim != 0 || st.attest != 0 {
				t.Fatalf("status=%d body=%q verify=%d writes=%d/%d/%d", w.Code, w.Body.String(), st.calls, st.registration, st.claim, st.attest)
			}
			if tc.status == 200 && (st.id != 42 || st.fields != [5]string{"session", "inbox", "sync", "alpha", "beta"} || !strings.Contains(w.Body.String(), `"status":"ok"`)) {
				t.Fatalf("verification response=%q id=%d fields=%v", w.Body.String(), st.id, st.fields)
			}
		})
	}
}

func TestPromptSourceAttestationBearerBoundary(t *testing.T) {
	body := `{"session_id":"session","source_inbox_id":"inbox","sync_id":"sync","owner_project":"alpha","prompt_project":"beta"}`
	human := cloudauth.Principal{ID: "human", Kind: cloudauth.PrincipalKindHuman, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	legacy := cloudauth.Principal{ID: "legacy:sync", Kind: cloudauth.PrincipalKindLegacy, Source: cloudauth.PrincipalSourceLegacyEnvSync, Enabled: true}
	service := cloudauth.Principal{ID: "service", Kind: cloudauth.PrincipalKindServiceAccount, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	auth := resolvingAuth{
		principals: map[string]cloudauth.Principal{"human-token": human, "legacy-token": legacy, "service-token": service},
		errors:     map[string]error{"revoked-token": cloudauth.ErrTokenRevoked, "disabled-token": cloudauth.ErrPrincipalDisabled},
	}
	for _, tc := range []struct {
		name, token string
		want, calls int
	}{
		{name: "human with dual grants", token: "human-token", want: 200, calls: 1},
		{name: "missing header", want: 401},
		{name: "revoked token", token: "revoked-token", want: 401},
		{name: "disabled principal", token: "disabled-token", want: 401},
		{name: "legacy token", token: "legacy-token", want: 403},
		{name: "service token", token: "service-token", want: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &attestationTestStore{}
			srv := New(st, auth, 0, WithPrincipalProjectAuthorizer(managedGrantAuthorizer{grants: map[string][]string{"human": {"alpha", "beta"}}}))
			req := httptest.NewRequest(http.MethodPost, "/sync/prompt-source-attestations", strings.NewReader(body))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != tc.want || st.registration != tc.calls || st.claim != tc.calls || st.attest != tc.calls {
				t.Fatalf("status=%d body=%q calls=%d/%d/%d; want %d and %d each", w.Code, w.Body.String(), st.registration, st.claim, st.attest, tc.want, tc.calls)
			}
			if tc.calls == 1 {
				if st.actor != human.ID {
					t.Fatalf("actor=%q, want %q", st.actor, human.ID)
				}
				var response struct {
					Status        string `json:"status"`
					AttestationID int64  `json:"attestation_id"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Status != "ok" || response.AttestationID != 42 {
					t.Fatalf("response=%q, decoded=%+v, err=%v", w.Body.String(), response, err)
				}
			}
		})
	}
}

func TestPromptSourceAttestationAdmission(t *testing.T) {
	body := `{"session_id":"session","source_inbox_id":"inbox","sync_id":"sync","owner_project":"alpha","prompt_project":"beta"}`
	human := cloudauth.Principal{ID: "human", Kind: cloudauth.PrincipalKindHuman, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	cases := []struct {
		name, body               string
		principal                cloudauth.Principal
		grants                   []string
		regErr, claimErr, attErr error
		limit                    int64
		want, reg, claim, att    int
	}{
		{name: "dual grants", body: body, principal: human, grants: []string{"alpha", "beta"}, want: 200, reg: 1, claim: 1, att: 1},
		{name: "owner grant absent", body: body, principal: human, grants: []string{"beta"}, want: 403},
		{name: "prompt grant absent", body: body, principal: human, grants: []string{"alpha"}, want: 403},
		{name: "no principal", body: body, want: 401},
		{name: "blank principal", body: body, principal: cloudauth.Principal{Kind: cloudauth.PrincipalKindHuman}, want: 401},
		{name: "service", body: body, principal: cloudauth.Principal{ID: "service", Kind: cloudauth.PrincipalKindServiceAccount, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}, want: 403},
		{name: "owner conflict", body: body, principal: human, grants: []string{"alpha", "beta"}, regErr: cloudstore.ErrSessionAuthorityConflict, want: 409, reg: 1},
		{name: "pair conflict", body: body, principal: human, grants: []string{"alpha", "beta"}, claimErr: cloudstore.ErrPromptPairClaimConflict, want: 409, reg: 1, claim: 1},
		{name: "attestation unbound", body: body, principal: human, grants: []string{"alpha", "beta"}, attErr: cloudstore.ErrPromptSourceAttestationUnbound, want: 409, reg: 1, claim: 1, att: 1},
		{name: "persistence failure", body: body, principal: human, grants: []string{"alpha", "beta"}, attErr: errors.New("database unavailable"), want: 500, reg: 1, claim: 1, att: 1},
		{name: "unknown actor", body: strings.TrimSuffix(body, "}") + `,"actor_id":"spoof"}`, principal: human, grants: []string{"alpha", "beta"}, want: 400},
		{name: "malformed", body: "{", principal: human, grants: []string{"alpha", "beta"}, want: 400},
		{name: "blank", body: strings.Replace(body, `"sync_id":"sync"`, `"sync_id":" "`, 1), principal: human, grants: []string{"alpha", "beta"}, want: 400},
		{name: "trailing", body: body + ` {}`, principal: human, grants: []string{"alpha", "beta"}, want: 400},
		{name: "oversize", body: body, principal: human, grants: []string{"alpha", "beta"}, limit: 20, want: 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &attestationTestStore{registrationErr: tc.regErr, claimErr: tc.claimErr, attestErr: tc.attErr}
			opts := []Option{WithPrincipalProjectAuthorizer(managedGrantAuthorizer{grants: map[string][]string{tc.principal.ID: tc.grants}})}
			if tc.limit > 0 {
				opts = append(opts, WithMaxPushBodyBytes(tc.limit))
			}
			srv := New(st, claimAuthOnly{}, 0, opts...)
			req := httptest.NewRequest(http.MethodPost, "/sync/prompt-source-attestations", strings.NewReader(tc.body))
			req = req.WithContext(WithPrincipal(req.Context(), tc.principal))
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != tc.want || st.registration != tc.reg || st.claim != tc.claim || st.attest != tc.att {
				t.Fatalf("status=%d body=%q calls=%d/%d/%d, want %d and %d/%d/%d", w.Code, w.Body.String(), st.registration, st.claim, st.attest, tc.want, tc.reg, tc.claim, tc.att)
			}
			if st.attest > 0 && st.actor != "human" {
				t.Fatalf("actor=%q", st.actor)
			}
		})
	}
}
