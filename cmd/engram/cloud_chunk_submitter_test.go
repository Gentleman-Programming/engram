package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

func TestCloudRuntimeRecordsManagedChunkSubmitter(t *testing.T) {
	const legacy = "fixture-legacy-sync-token"
	const pepper = "fixture-managed-token-pepper-32-bytes"
	const project = "submitter-fixture"
	const display = "same-display"
	t.Setenv("ENGRAM_CLOUD_TOKEN", legacy)
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")
	dsn := openIsolatedCloudRuntimeSchema(t)
	ctx := t.Context()
	opened, err := newCloudRuntime(ctx, cloud.Config{
		DSN: dsn, JWTSecret: "fixture-managed-chunk-submitters-jwt-secret",
		TokenPepper: pepper, AllowedProjects: []string{project}, MaxPushBodyBytes: cloud.DefaultMaxPushBodyBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := opened.(*defaultCloudRuntime)
	t.Cleanup(func() { _ = runtime.store.Close() })
	handler := runtime.server.Handler()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	hasher, err := cloudauth.NewManagedTokenHasher([]byte(pepper))
	if err != nil {
		t.Fatal(err)
	}
	managed := func(name, role string) (principal, bearer string) {
		t.Helper()
		u, err := runtime.store.CreateHumanUser(ctx, cloudstore.CreateHumanUserParams{
			Username: name, Email: name + "@example.invalid", DisplayName: display, Role: role,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.store.CreateProjectGrant(ctx, cloudstore.CreateProjectGrantParams{
			PrincipalID: u.PrincipalID, Project: project, GrantedByPrincipalID: u.PrincipalID,
		}); err != nil {
			t.Fatal(err)
		}
		token, err := cloudauth.GenerateManagedToken("fixture")
		if err != nil {
			t.Fatal(err)
		}
		hash, err := hasher.Hash(token.Raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.store.CreatePrincipalToken(ctx, cloudstore.CreatePrincipalTokenParams{
			PrincipalID: u.PrincipalID, TokenPrefix: token.Prefix, TokenHash: hash, Name: "fixture",
		}); err != nil {
			t.Fatal(err)
		}
		return u.PrincipalID, token.Raw
	}
	firstID, first := managed("fixture-first", cloudstore.PrincipalRoleAdmin)
	secondID, second := managed("fixture-second", cloudstore.PrincipalRoleAdmin)
	post := func(token, path string, payload any, want int) {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "https://example.invalid"+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s status=%d want=%d: %s", path, res.Code, want, res.Body.String())
		}
	}
	session := func(id string) map[string]string {
		return map[string]string{"id": id, "directory": "/fixture", "started_at": "2026-10-01T00:00:00Z"}
	}
	push := func(id, author string) map[string]any {
		return map[string]any{"project": project, "created_by": author,
			"submitted_by_principal_id": "client-claim",
			"data":                      map[string]any{"sessions": []any{session(id)}}}
	}
	batch := func(id string) map[string]any {
		return map[string]any{"created_by": "client-claim", "entries": []map[string]any{{
			"project": project, "entity": "session", "entity_key": id, "op": "upsert", "payload": session(id),
			"submitted_by_principal_id": "client-claim",
		}}}
	}
	post(first, "/sync/push", push("credited-direct", "historic-credited-author"), http.StatusOK)
	post(first, "/sync/mutations/push", batch("first-mutation"), http.StatusOK)
	post(second, "/sync/mutations/push", batch("second-mutation"), http.StatusOK)
	post(legacy, "/sync/push", push("unverified-history", display), http.StatusOK)

	type chunkCredit struct {
		author    string
		submitter sql.NullString
	}
	read := func() map[string]chunkCredit {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT chunk_id, created_by, submitted_by_principal_id::text
			FROM cloud_chunks WHERE project_name=$1`, project)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		credits := map[string]chunkCredit{}
		for rows.Next() {
			var id string
			var credit chunkCredit
			if err := rows.Scan(&id, &credit.author, &credit.submitter); err != nil {
				t.Fatal(err)
			}
			credits[id] = credit
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return credits
	}
	prior := read()
	counts := map[chunkCredit]int{}
	for _, credit := range prior {
		counts[credit]++
	}
	want := map[chunkCredit]int{
		{"historic-credited-author", sql.NullString{String: firstID, Valid: true}}: 1,
		{display, sql.NullString{String: firstID, Valid: true}}:                    1,
		{display, sql.NullString{String: secondID, Valid: true}}:                   1,
		{display, sql.NullString{}}:                                                1,
	}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("unexpected stored submitters or credited authors: %v", counts)
	}
	post(second, "/sync/push", push("credited-direct", "client-cannot-change-credited-author"), http.StatusOK)
	post(second, "/sync/push", push("unverified-history", display), http.StatusOK)
	post(first, "/sync/mutations/push", batch("second-mutation"), http.StatusOK)
	if after := read(); !reflect.DeepEqual(after, prior) {
		t.Fatal("a replay changed immutable chunk submission metadata")
	}
	manifest, err := runtime.store.ReadManifest(ctx, project)
	if err != nil || len(manifest.Chunks) != 4 {
		t.Fatalf("unexpected manifest: %+v %v", manifest, err)
	}
	for _, entry := range manifest.Chunks {
		if entry.CreatedBy != prior[entry.ID].author {
			t.Fatal("manifest credit changed")
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO cloud_chunks (project_name, chunk_id, created_by, payload, submitted_by_principal_id)
		VALUES ('outside-grant', 'hidden-chunk', 'hidden-author', '{}', $1::bigint)`, firstID); err != nil {
		t.Fatal(err)
	}
	view, err := runtime.store.DashboardStoreForProjectsContext(ctx, []string{project})
	if err != nil {
		t.Fatal(err)
	}
	submitters, err := view.ListAuthenticatedChunkSubmitters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(submitters) != 2 ||
		submitters[0].Username != "fixture-first" || submitters[0].Chunks != 2 || submitters[0].Projects != 1 ||
		submitters[1].Username != "fixture-second" || submitters[1].Chunks != 1 || submitters[1].Projects != 1 {
		t.Fatalf("expected one row per managed credential owner: %+v", submitters)
	}
	emptyView, err := runtime.store.DashboardStoreForProjectsContext(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := emptyView.ListAuthenticatedChunkSubmitters(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("view without grants: %v, %v", rows, err)
	}

	const submissionsPath = "/dashboard/contributors/views/submissions"
	page := func(bearer, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(dashboardLoginCookie(t, handler, bearer))
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	contributors := page(first, "/dashboard/contributors")
	if contributors.Code != http.StatusOK || !strings.Contains(contributors.Body.String(), submissionsPath) {
		t.Fatalf("managed admin could not find the submissions view: %d", contributors.Code)
	}
	submissions := page(first, submissionsPath)
	if body := submissions.Body.String(); submissions.Code != http.StatusOK ||
		!strings.Contains(body, "@fixture-first") || !strings.Contains(body, "@fixture-second") || strings.Contains(body, "hidden-author") {
		t.Fatalf("unexpected submissions view: %d %s", submissions.Code, body)
	}
	_, member := managed("fixture-member", cloudstore.PrincipalRoleMember)
	if res := page(member, submissionsPath); res.Code != http.StatusForbidden {
		t.Fatalf("expected member access to be forbidden: %d", res.Code)
	}
}
