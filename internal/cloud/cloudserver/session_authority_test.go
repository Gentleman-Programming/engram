package cloudserver

import (
	"context"
	"database/sql"
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

type authorityTestStore struct {
	fakeStore
	calls                   int
	session, project, actor string
	err                     error
}

func (s *authorityTestStore) RegisterSessionAuthority(_ context.Context, session, project, actor string) error {
	s.calls++
	s.session, s.project, s.actor = session, project, actor
	return s.err
}

// Grant matching normalizes authorization keys, not persisted owner identity.
type normalizedAuthorityGrants struct{ grants map[string]string }

func (a normalizedAuthorityGrants) AuthorizeProjectForPrincipal(_ context.Context, principal cloudauth.Principal, project string) error {
	if a.grants[principal.ID] == cloudstore.NormalizeProjectGrant(project) {
		return nil
	}
	return fmt.Errorf("project grant denied")
}

func (a normalizedAuthorityGrants) EnrolledProjectsForPrincipal(_ context.Context, principal cloudauth.Principal) ([]string, error) {
	return []string{a.grants[principal.ID]}, nil
}

func TestSessionAuthorityProjectPolicy(t *testing.T) {
	legacy, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	legacy.SetBearerToken("legacy-token")
	legacy.SetAllowedProjects([]string{"alpha"})
	for _, tc := range []struct {
		name, token, project string
		auth                 Authenticator
		want, calls          int
	}{
		{"legacy allowed", "legacy-token", "alpha", legacy, 200, 1},
		{"legacy denied", "legacy-token", "beta", legacy, 403, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &authorityTestStore{}
			srv := New(st, tc.auth, 0, WithPrincipalProjectAuthorizer(normalizedAuthorityGrants{grants: map[string]string{}}))
			req := httptest.NewRequest(http.MethodPost, "/sync/session-authorities", strings.NewReader(`{"session_id":"s","project":"`+tc.project+`"}`))
			req.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != tc.want || st.calls != tc.calls {
				t.Fatalf("status=%d calls=%d body=%q", w.Code, st.calls, w.Body.String())
			}
		})
	}
	managed := cloudauth.Principal{ID: "managed", Kind: cloudauth.PrincipalKindHuman, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	st := &authorityTestStore{}
	srv := New(st, resolvingAuth{principals: map[string]cloudauth.Principal{"token": managed}}, 0,
		WithPrincipalProjectAuthorizer(normalizedAuthorityGrants{grants: map[string]string{"managed": "alpha-foo"}}))
	req := httptest.NewRequest(http.MethodPost, "/sync/session-authorities", strings.NewReader(`{"session_id":"s","project":"alpha/foo"}`))
	req.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 || st.project != "alpha/foo" || st.calls != 1 {
		t.Fatalf("status=%d project=%q calls=%d body=%q", w.Code, st.project, st.calls, w.Body.String())
	}
	for _, project := range []string{"alpha", "beta"} {
		t.Run("insecure "+project, func(t *testing.T) {
			insecureStore := &authorityTestStore{}
			insecure := New(insecureStore, nil, 0, WithProjectAuthorizer(legacy))
			w := httptest.NewRecorder()
			body := `{"session_id":"s","project":"` + project + `"}`
			insecure.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/sync/session-authorities", strings.NewReader(body)))
			if w.Code != 401 || insecureStore.calls != 0 {
				t.Fatalf("insecure registration status=%d calls=%d body=%q", w.Code, insecureStore.calls, w.Body.String())
			}
		})
	}
}

func TestSessionAuthorityRegistrationPersistsOverHTTP(t *testing.T) {
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
	schema := fmt.Sprintf("cloudserver_authority_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	cs, err := cloudstore.New(cloud.Config{DSN: dsn + separator + "search_path=" + schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	principal := cloudauth.Principal{ID: "p-owner", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleMember, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	srv := New(cs, resolvingAuth{principals: map[string]cloudauth.Principal{"token": principal}}, 0,
		WithPrincipalProjectAuthorizer(managedGrantAuthorizer{grants: map[string][]string{principal.ID: {"alpha", "beta"}}}))
	post := func(project string) int {
		t.Helper()
		body := `{"session_id":"real-store-session","project":"` + project + `"}`
		req := httptest.NewRequest(http.MethodPost, "/sync/session-authorities", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w.Code
	}
	if code := post("alpha"); code != http.StatusOK {
		t.Fatalf("registration status=%d", code)
	}
	if code := post("alpha"); code != http.StatusOK {
		t.Fatalf("replay status=%d", code)
	}
	if code := post("beta"); code != http.StatusConflict {
		t.Fatalf("owner conflict status=%d", code)
	}
	authority, err := cs.GetSessionAuthority(ctx, "real-store-session")
	if err != nil || authority == nil || authority.OwnerProject != "alpha" || authority.RegisteredBy != principal.ID {
		t.Fatalf("persisted authority=%+v err=%v", authority, err)
	}
}

func TestSessionAuthorityRegistration(t *testing.T) {
	principal := cloudauth.Principal{ID: "p-alpha", Kind: cloudauth.PrincipalKindHuman, Role: cloudauth.RoleMember, Source: cloudauth.PrincipalSourceManagedToken, Enabled: true}
	authn := resolvingAuth{principals: map[string]cloudauth.Principal{"token": principal}}
	cases := []struct {
		name, body, token string
		grants            []string
		storeErr          error
		unsupported       bool
		limit             int64
		want              int
		calls             int
	}{
		{name: "missing token", body: `{"session_id":"s","project":"alpha"}`, grants: []string{"alpha"}, want: 401},
		{name: "beta only", body: `{"session_id":"s","project":"alpha"}`, token: "token", grants: []string{"beta"}, want: 403},
		{name: "alpha registration", body: `{"session_id":" s ","project":" alpha "}`, token: "token", grants: []string{"alpha"}, want: 200, calls: 1},
		{name: "conflicting owner", body: `{"session_id":"s","project":"alpha"}`, token: "token", grants: []string{"alpha"}, storeErr: cloudstore.ErrSessionAuthorityConflict, want: 409, calls: 1},
		{name: "storage failure", body: `{"session_id":"s","project":"alpha"}`, token: "token", grants: []string{"alpha"}, storeErr: errors.New("db down"), want: 500, calls: 1},
		{name: "unsupported store", body: `{"session_id":"s","project":"alpha"}`, token: "token", grants: []string{"alpha"}, unsupported: true, want: 500},
		{name: "empty session", body: `{"session_id":" ","project":"alpha"}`, token: "token", grants: []string{"alpha"}, want: 400},
		{name: "empty project", body: `{"session_id":"s","project":" "}`, token: "token", grants: []string{"alpha"}, want: 400},
		{name: "actor spoof", body: `{"session_id":"s","project":"alpha","actor":"fake"}`, token: "token", grants: []string{"alpha"}, want: 400},
		{name: "trailing JSON", body: `{"session_id":"s","project":"alpha"} {}`, token: "token", grants: []string{"alpha"}, want: 400},
		{name: "malformed", body: `{"session_id":`, token: "token", grants: []string{"alpha"}, want: 400},
		{name: "oversize", body: `{"session_id":"s","project":"alpha"}`, token: "token", grants: []string{"alpha"}, limit: 12, want: 413},
		{name: "oversize trailing", body: `{"session_id":"s","project":"alpha"} ` + strings.Repeat(" ", 40), token: "token", grants: []string{"alpha"}, limit: 38, want: 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &authorityTestStore{}
			var backing ChunkStore = st
			if tc.unsupported {
				backing = &fakeStore{sessions: map[string]map[string]struct{}{"alpha": {"s": {}}}}
			}
			opts := []Option{WithPrincipalProjectAuthorizer(managedGrantAuthorizer{grants: map[string][]string{principal.ID: tc.grants}})}
			if tc.limit > 0 {
				opts = append(opts, WithMaxPushBodyBytes(tc.limit))
			}
			st.err = tc.storeErr
			srv := New(backing, authn, 0, opts...)
			req := httptest.NewRequest(http.MethodPost, "/sync/session-authorities", strings.NewReader(tc.body))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != tc.want || st.calls != tc.calls {
				t.Fatalf("status=%d body=%q calls=%d; want status=%d calls=%d", w.Code, w.Body.String(), st.calls, tc.want, tc.calls)
			}
			if tc.calls == 1 && (st.session != "s" || st.project != "alpha" || st.actor != principal.ID) {
				t.Fatalf("registration = %q %q %q", st.session, st.project, st.actor)
			}
		})
	}
}
