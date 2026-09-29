package cloudserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/v2/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v2/internal/cloud/cloudstore"
)

type claimAuthOnly struct{}

func (claimAuthOnly) Authorize(*http.Request) error { return nil }

type pairClaimStore struct {
	fakeStore
	owner                                  string
	lookup, claims                         int
	session, inbox, syncID, project, actor string
	lookupErr, claimErr                    error
}

func (s *pairClaimStore) GetSessionAuthority(_ context.Context, _ string) (*cloudstore.SessionAuthority, error) {
	s.lookup++
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	if s.owner == "" {
		return nil, nil
	}
	return &cloudstore.SessionAuthority{OwnerProject: s.owner}, nil
}
func (s *pairClaimStore) ClaimPromptPair(_ context.Context, session, inbox, syncID, project, actor string) error {
	s.claims++
	s.session, s.inbox, s.syncID, s.project, s.actor = session, inbox, syncID, project, actor
	return s.claimErr
}

func TestPromptPairClaimAdmission(t *testing.T) {
	principal := cloudauth.Principal{ID: "managed", Kind: cloudauth.PrincipalKindHuman, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	body := `{"session_id":" s ","source_inbox_id":" i ","sync_id":" y ","owner_project":"alpha","project":"beta"}`
	cases := []struct {
		name, body, owner     string
		grants                []string
		token                 string
		lookupErr, claimErr   error
		limit                 int64
		want, lookups, claims int
		requests              int
	}{
		{name: "dual grant", body: body, owner: "alpha", grants: []string{"alpha", "beta"}, token: "token", want: 200, lookups: 1, claims: 1},
		{name: "beta only", body: body, owner: "alpha", grants: []string{"beta"}, token: "token", want: 403},
		{name: "alpha only", body: body, owner: "alpha", grants: []string{"alpha"}, token: "token", want: 403},
		{name: "missing", body: body, grants: []string{"alpha", "beta"}, token: "token", want: 404, lookups: 1},
		{name: "wrong selector", body: body, owner: "other", grants: []string{"alpha", "beta"}, token: "token", want: 404, lookups: 1},
		{name: "replay", body: body, owner: "alpha", grants: []string{"alpha", "beta"}, token: "token", want: 200, lookups: 2, claims: 2, requests: 2},
		{name: "conflict", body: body, owner: "alpha", grants: []string{"alpha", "beta"}, token: "token", claimErr: cloudstore.ErrPromptPairClaimConflict, want: 409, lookups: 1, claims: 1},
		{name: "claim failure", body: body, owner: "alpha", grants: []string{"alpha", "beta"}, token: "token", claimErr: errors.New("secret database failure"), want: 500, lookups: 1, claims: 1},
		{name: "authority vanished", body: body, owner: "alpha", grants: []string{"alpha", "beta"}, token: "token", claimErr: cloudstore.ErrSessionAuthorityNotFound, want: 404, lookups: 1, claims: 1},
		{name: "lookup failure", body: body, grants: []string{"alpha", "beta"}, token: "token", lookupErr: errors.New("secret database failure"), want: 500, lookups: 1},
		{name: "no token", body: body, grants: []string{"alpha", "beta"}, want: 401},
		{name: "spoof actor", body: strings.TrimSuffix(body, "}") + `,"actor":"admin"}`, grants: []string{"alpha", "beta"}, token: "token", want: 400},
		{name: "unknown field", body: strings.TrimSuffix(body, "}") + `,"unknown":1}`, grants: []string{"alpha", "beta"}, token: "token", want: 400},
		{name: "empty identity", body: strings.Replace(body, `"sync_id":" y "`, `"sync_id":" "`, 1), grants: []string{"alpha", "beta"}, token: "token", want: 400},
		{name: "malformed", body: `{`, grants: []string{"alpha", "beta"}, token: "token", want: 400},
		{name: "trailing", body: body + ` {}`, grants: []string{"alpha", "beta"}, token: "token", want: 400},
		{name: "oversize", body: body, grants: []string{"alpha", "beta"}, token: "token", limit: 20, want: 413},
		{name: "oversize trailing", body: body + strings.Repeat(" ", 100), grants: []string{"alpha", "beta"}, token: "token", limit: int64(len(body) + 10), want: 413},
	}
	var unavailableBody string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &pairClaimStore{owner: tc.owner, lookupErr: tc.lookupErr, claimErr: tc.claimErr}
			opts := []Option{WithPrincipalProjectAuthorizer(managedGrantAuthorizer{grants: map[string][]string{principal.ID: tc.grants}})}
			if tc.limit > 0 {
				opts = append(opts, WithMaxPushBodyBytes(tc.limit))
			}
			srv := New(st, resolvingAuth{principals: map[string]cloudauth.Principal{"token": principal}}, 0, opts...)
			requests := tc.requests
			if requests == 0 {
				requests = 1
			}
			var w *httptest.ResponseRecorder
			for i := 0; i < requests; i++ {
				req := httptest.NewRequest(http.MethodPost, "/sync/prompt-pair-claims", strings.NewReader(tc.body))
				if tc.token != "" {
					req.Header.Set("Authorization", "Bearer "+tc.token)
				}
				w = httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, req)
				if w.Code != tc.want {
					t.Fatalf("request %d status=%d body=%q; want %d", i, w.Code, w.Body.String(), tc.want)
				}
			}
			if st.lookup != tc.lookups || st.claims != tc.claims {
				t.Fatalf("lookup=%d claims=%d; want %d/%d", st.lookup, st.claims, tc.lookups, tc.claims)
			}
			if st.claims != 0 && (st.session != "s" || st.inbox != "i" || st.syncID != "y" || st.project != "beta" || st.actor != principal.ID) {
				t.Fatalf("claim fields: %+v", st)
			}
			if tc.name == "missing" || tc.name == "wrong selector" || tc.name == "authority vanished" {
				var payload struct {
					ErrorCode string `json:"error_code"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || payload.ErrorCode != "session_authority_unavailable" {
					t.Fatalf("unexpected authority response: %q (%v)", w.Body.String(), err)
				}
				if unavailableBody != "" && unavailableBody != w.Body.String() {
					t.Fatalf("authority response differs: %q vs %q", unavailableBody, w.Body.String())
				}
				unavailableBody = w.Body.String()
			}
			if tc.want == 500 && strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("database detail leaked: %q", w.Body.String())
			}
		})
	}
}

func TestPromptPairClaimOwnerGrantAliasDoesNotChangeIdentity(t *testing.T) {
	principal := cloudauth.Principal{ID: "managed", Kind: cloudauth.PrincipalKindHuman, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	st := &pairClaimStore{owner: "alpha/foo"}
	srv := New(st, resolvingAuth{principals: map[string]cloudauth.Principal{"token": principal}}, 0,
		WithPrincipalProjectAuthorizer(normalizedAuthorityGrants{grants: map[string]string{principal.ID: "alpha-foo"}}))
	body := `{"session_id":"s","source_inbox_id":"i","sync_id":"y","owner_project":"alpha/foo","project":"alpha/foo"}`
	req := httptest.NewRequest(http.MethodPost, "/sync/prompt-pair-claims", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 || st.lookup != 1 || st.claims != 1 || st.project != "alpha/foo" {
		t.Fatalf("status=%d lookup=%d claims=%d project=%q body=%q", w.Code, st.lookup, st.claims, st.project, w.Body.String())
	}
}

func TestPromptPairClaimRejectsMissingProjectPolicy(t *testing.T) {
	principal := cloudauth.Principal{ID: "managed", Kind: cloudauth.PrincipalKindHuman, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	legacy, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	legacy.SetBearerToken("legacy-token")
	body := `{"session_id":"s","source_inbox_id":"i","sync_id":"y","owner_project":"alpha","project":"beta"}`
	for _, tc := range []struct {
		name, token string
		auth        Authenticator
		opts        []Option
	}{
		{"managed lacks grants", "token", resolvingAuth{principals: map[string]cloudauth.Principal{"token": principal}}, []Option{WithProjectAuthorizer(legacy)}},
		{"legacy lacks allowlist", "legacy-token", claimAuthOnly{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &pairClaimStore{owner: "alpha"}
			srv := New(st, tc.auth, 0, tc.opts...)
			req := httptest.NewRequest(http.MethodPost, "/sync/prompt-pair-claims", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != 403 || st.lookup != 0 || st.claims != 0 {
				t.Fatalf("status=%d lookup=%d claims=%d body=%q", w.Code, st.lookup, st.claims, w.Body.String())
			}
		})
	}
}

func TestPromptPairClaimPersistsOverHTTP(t *testing.T) {
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("cloudserver_pair_claim_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	cs, err := cloudstore.New(cloud.Config{DSN: dsn + sep + "search_path=" + schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	principal := cloudauth.Principal{ID: "p-owner", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleMember, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	srv := New(cs, resolvingAuth{principals: map[string]cloudauth.Principal{"token": principal}}, 0,
		WithPrincipalProjectAuthorizer(managedGrantAuthorizer{grants: map[string][]string{principal.ID: {"alpha", "beta"}}}))
	post := func(path, body string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w.Code
	}
	registration := `{"session_id":"session","project":"alpha"}`
	claim := `{"session_id":"session","source_inbox_id":"inbox","sync_id":"sync","owner_project":"alpha","project":"beta"}`
	if code := post("/sync/session-authorities", registration); code != 200 {
		t.Fatalf("registration status=%d", code)
	}
	for _, want := range []int{200, 200} {
		if code := post("/sync/prompt-pair-claims", claim); code != want {
			t.Fatalf("claim status=%d want=%d", code, want)
		}
	}
	if code := post("/sync/prompt-pair-claims", strings.Replace(claim, `"sync_id":"sync"`, `"sync_id":"other"`, 1)); code != 409 {
		t.Fatalf("conflict status=%d", code)
	}
	persisted, err := cs.GetPromptPairClaim(ctx, "session", "inbox")
	if err != nil || persisted == nil || persisted.SyncID != "sync" || persisted.PromptProject != "beta" || persisted.ClaimedBy != principal.ID {
		t.Fatalf("persisted claim=%+v err=%v", persisted, err)
	}
}

func TestPromptPairClaimInsecureAndLegacy(t *testing.T) {
	legacy, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	legacy.SetBearerToken("legacy-token")
	legacy.SetAllowedProjects([]string{"alpha", "beta"})
	body := `{"session_id":"s","source_inbox_id":"i","sync_id":"y","owner_project":"alpha","project":"beta"}`
	for _, tc := range []struct {
		name                  string
		auth                  Authenticator
		project               string
		want, lookups, claims int
	}{
		{"legacy allowed", legacy, "beta", 200, 1, 1}, {"legacy denied", legacy, "gamma", 403, 0, 0},
		{"insecure allowed", nil, "beta", 401, 0, 0}, {"insecure forbidden", nil, "gamma", 401, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &pairClaimStore{owner: "alpha"}
			srv := New(st, tc.auth, 0, WithProjectAuthorizer(legacy))
			req := httptest.NewRequest(http.MethodPost, "/sync/prompt-pair-claims", strings.NewReader(strings.Replace(body, `"project":"beta"`, `"project":"`+tc.project+`"`, 1)))
			if tc.auth != nil {
				req.Header.Set("Authorization", "Bearer legacy-token")
			}
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != tc.want || st.lookup != tc.lookups || st.claims != tc.claims {
				t.Fatalf("status=%d lookup=%d claims=%d body=%q", w.Code, st.lookup, st.claims, w.Body.String())
			}
			if st.claims > 0 && st.actor != "legacy:sync" {
				t.Fatalf("actor=%q", st.actor)
			}
		})
	}
}
