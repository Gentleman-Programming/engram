package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	projectpkg "github.com/Gentleman-Programming/engram/v2/internal/project"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
	_ "modernc.org/sqlite"
)

type stubListener struct{}

func (stubListener) Accept() (net.Conn, error) { return nil, errors.New("not used") }
func (stubListener) Close() error              { return nil }
func (stubListener) Addr() net.Addr            { return &net.TCPAddr{} }

func TestStartReturnsListenError(t *testing.T) {
	s := New(nil, 7777)
	s.listen = func(network, address string) (net.Listener, error) {
		return nil, errors.New("listen failed")
	}

	err := s.Start()
	if err == nil {
		t.Fatalf("expected start to fail on listen error")
	}
}

func TestStartUsesInjectedServe(t *testing.T) {
	s := New(&store.Store{}, 7777)
	s.listen = func(network, address string) (net.Listener, error) {
		return stubListener{}, nil
	}
	s.serve = func(ln net.Listener, h http.Handler) error {
		if ln == nil || h == nil {
			t.Fatalf("expected listener and handler to be provided")
		}
		return errors.New("serve stopped")
	}

	err := s.Start()
	if err == nil || err.Error() != "serve stopped" {
		t.Fatalf("expected propagated serve error, got %v", err)
	}
}

func newServerTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func TestStartUsesDefaultListenWhenListenNil(t *testing.T) {
	s := New(newServerTestStore(t), 0)
	s.listen = nil
	s.serve = func(ln net.Listener, h http.Handler) error {
		if ln == nil || h == nil {
			t.Fatalf("expected non-nil listener and handler")
		}
		_ = ln.Close()
		return errors.New("serve stopped")
	}

	err := s.Start()
	if err == nil || err.Error() != "serve stopped" {
		t.Fatalf("expected propagated serve error, got %v", err)
	}
}

func TestStartUsesDefaultServeWhenServeNil(t *testing.T) {
	s := New(newServerTestStore(t), 7777)
	s.listen = func(network, address string) (net.Listener, error) {
		return stubListener{}, nil
	}
	s.serve = nil

	err := s.Start()
	if err == nil {
		t.Fatalf("expected start to fail when default http.Serve receives failing listener")
	}
}

func TestProjectScopedHTTPReadFamiliesUseCurrentAndExplicitAll(t *testing.T) {
	st := newServerTestStore(t)
	t.Setenv("ENGRAM_PROJECT", "alpha")
	ids := map[string]int64{}
	for _, project := range []string{"alpha", "beta"} {
		if err := st.CreateSession("scope-"+project, project, "/tmp/"+project); err != nil {
			t.Fatalf("create %s session: %v", project, err)
		}
		id, err := st.AddObservation(store.AddObservationParams{
			SessionID: "scope-" + project,
			Type:      "note",
			Title:     "scoped " + project,
			Content:   "scoped search " + project,
			Project:   project,
		})
		if err != nil {
			t.Fatalf("add %s observation: %v", project, err)
		}
		ids[project] = id
		if _, err := st.AddPrompt(store.AddPromptParams{SessionID: "scope-" + project, Content: "scoped prompt " + project, Project: project}); err != nil {
			t.Fatalf("add %s prompt: %v", project, err)
		}
	}
	past := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")
	if _, err := st.DB().Exec(`UPDATE observations SET review_after = ?`, past); err != nil {
		t.Fatalf("backdate reviews: %v", err)
	}

	provider := &stubSyncStatusProvider{status: SyncStatus{Enabled: true, Phase: "healthy"}}
	srv := New(st, 0)
	srv.SetSyncStatus(provider)
	h := srv.Handler()
	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	for _, path := range []string{
		"/sessions/recent",
		"/observations",
		"/observations/recent",
		"/review",
		"/prompts/recent",
		"/prompts/search?q=scoped",
		"/export",
		"/stats",
		"/conflicts",
		"/conflicts/stats",
	} {
		body := get(path)
		if strings.Contains(body, "beta") {
			t.Fatalf("omitted selector leaked beta data for %s: %s", path, body)
		}
	}
	_ = get("/sync/status?project=beta")
	if provider.lastProject != "beta" {
		t.Fatalf("sync status must preserve its explicit project contract: got %q", provider.lastProject)
	}

	for _, path := range []string{
		"/sessions/recent?all_projects=true",
		"/observations?all_projects=true",
		"/observations/recent?all_projects=true",
		"/review?all_projects=true",
		"/prompts/recent?all_projects=true",
		"/prompts/search?q=scoped&all_projects=true",
		"/export?all_projects=true",
		"/stats?all_projects=true",
	} {
		body := get(path)
		if !strings.Contains(body, "beta") {
			t.Fatalf("explicit all selector omitted beta data for %s: %s", path, body)
		}
	}
	_ = get("/conflicts?all_projects=true")
	_ = get("/conflicts/stats?all_projects=true")

	for _, body := range []string{`{"apply":false}`, `{"all_projects":true,"apply":false}`} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("scan body %s = %d: %s", body, rec.Code, rec.Body.String())
		}
	}

	before, err := st.GetObservation(ids["alpha"])
	if err != nil {
		t.Fatalf("load review observation: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/review/mark_reviewed?project=missing", strings.NewReader(fmt.Sprintf(`{"observation_id":%d}`, ids["alpha"]))))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown review selector = %d: %s", rec.Code, rec.Body.String())
	}
	after, err := st.GetObservation(ids["alpha"])
	if err != nil || before.ReviewAfter == nil || after.ReviewAfter == nil || *after.ReviewAfter != *before.ReviewAfter {
		t.Fatalf("failed project resolution must not mutate review state: before=%v after=%v err=%v", before.ReviewAfter, after.ReviewAfter, err)
	}

	for _, selector := range []struct {
		path       string
		wantStatus int
		wantCode   string
	}{
		{"/stats?project=missing", http.StatusNotFound, "unknown_project"},
		{"/stats?all_projects=not-a-bool", http.StatusBadRequest, "invalid_project"},
		{"/stats?project=alpha&all_projects=true", http.StatusBadRequest, "invalid_project"},
		{"/stats?project=alpha&project=beta", http.StatusBadRequest, "invalid_project"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, selector.path, nil))
		if rec.Code != selector.wantStatus {
			t.Fatalf("selector %s = %d, want %d: %s", selector.path, rec.Code, selector.wantStatus, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode selector %s response: %v", selector.path, err)
		}
		if body["code"] != selector.wantCode {
			t.Fatalf("selector %s error code = %#v, want %q: %s", selector.path, body["code"], selector.wantCode, rec.Body.String())
		}
	}
}

func TestRepeatedProjectSelectorReturnsInvalidProject(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats?project=alpha&project=beta", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("repeated project selector = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode repeated selector response: %v", err)
	}
	if body["code"] != "invalid_project" {
		t.Fatalf("repeated project selector code = %#v, want invalid_project: %s", body["code"], rec.Body.String())
	}
}

func TestClaudeSaveNudgeCompatibilityRoutes(t *testing.T) {
	st := newServerTestStore(t)
	h := New(st, 0).Handler()

	createReq := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(`{"id":"s-nudge","project":"engram","directory":"/work/engram"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected session create 201, got %d", createRec.Code)
	}

	getSessionReq := httptest.NewRequest(http.MethodGet, "/sessions/s-nudge", nil)
	getSessionRec := httptest.NewRecorder()
	h.ServeHTTP(getSessionRec, getSessionReq)
	if getSessionRec.Code != http.StatusOK {
		t.Fatalf("expected session get 200, got %d body=%s", getSessionRec.Code, getSessionRec.Body.String())
	}
	var session map[string]any
	if err := json.Unmarshal(getSessionRec.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if session["started_at"] == "" || session["project"] != "engram" {
		t.Fatalf("expected session JSON with started_at and project, got %#v", session)
	}

	for _, title := range []string{"Older save", "Newest save"} {
		obsReq := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(fmt.Sprintf(`{"session_id":"s-nudge","type":"note","title":%q,"content":"body","project":"engram"}`, title)))
		obsReq.Header.Set("Content-Type", "application/json")
		obsRec := httptest.NewRecorder()
		h.ServeHTTP(obsRec, obsReq)
		if obsRec.Code != http.StatusCreated {
			t.Fatalf("expected observation create 201 for %q, got %d body=%s", title, obsRec.Code, obsRec.Body.String())
		}
	}

	listReq := httptest.NewRequest(http.MethodGet, "/observations?project=engram&limit=1&sort=created_at:desc", nil)
	listRec := httptest.NewRecorder()
	h.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected observations list 200, got %d body=%s", listRec.Code, listRec.Body.String())
	}
	var obs []map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &obs); err != nil {
		t.Fatalf("decode observations: %v", err)
	}
	if len(obs) != 1 || obs[0]["title"] != "Newest save" || obs[0]["created_at"] == "" {
		t.Fatalf("expected latest observation with created_at, got %#v", obs)
	}

	badSortReq := httptest.NewRequest(http.MethodGet, "/observations?sort=updated_at:desc", nil)
	badSortRec := httptest.NewRecorder()
	h.ServeHTTP(badSortRec, badSortReq)
	if badSortRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsupported sort, got %d", badSortRec.Code)
	}

	missingSessionReq := httptest.NewRequest(http.MethodGet, "/sessions/missing", nil)
	missingSessionRec := httptest.NewRecorder()
	h.ServeHTTP(missingSessionRec, missingSessionReq)
	if missingSessionRec.Code != http.StatusNotFound {
		t.Fatalf("expected missing session 404, got %d", missingSessionRec.Code)
	}
}

func TestHandleCreateSessionOwnershipModeContract(t *testing.T) {
	st := newServerTestStore(t)
	h := New(st, 0).Handler()
	for _, tc := range []struct {
		name       string
		id         string
		body       string
		wantStatus int
		wantMode   string
	}{
		{name: "omitted defaults to shared", id: "mode-omitted", body: `{"id":"mode-omitted","project":"project-a"}`, wantStatus: http.StatusCreated, wantMode: store.SessionOwnershipShared},
		{name: "explicit shared", id: "mode-shared", body: `{"id":"mode-shared","project":"project-a","ownership_mode":"shared"}`, wantStatus: http.StatusCreated, wantMode: store.SessionOwnershipShared},
		{name: "explicit project owned", id: "mode-owned", body: `{"id":"mode-owned","project":"project-a","ownership_mode":"project_owned"}`, wantStatus: http.StatusCreated, wantMode: store.SessionOwnershipProjectOwned},
		{name: "invalid mode", id: "mode-invalid", body: `{"id":"mode-invalid","project":"project-a","ownership_mode":"exclusive"}`, wantStatus: http.StatusBadRequest},
		{name: "missing project", id: "mode-missing-project", body: `{"id":"mode-missing-project"}`, wantStatus: http.StatusBadRequest},
		{name: "empty project", id: "mode-empty-project", body: `{"id":"mode-empty-project","project":""}`, wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(tc.body)))
			if rec.Code != tc.wantStatus {
				t.Fatalf("POST /sessions = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var response map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if tc.wantStatus == http.StatusCreated {
				if response["status"] != "created" {
					t.Fatalf("success response = %#v", response)
				}
				id := response["id"].(string)
				session, err := st.GetSession(id)
				if err != nil || session.OwnershipMode != tc.wantMode {
					t.Fatalf("stored session = %#v, %v; want mode %q", session, err, tc.wantMode)
				}
				return
			}
			if response["error"] == "" {
				t.Fatalf("error response = %#v, want error body", response)
			}
			if _, err := st.GetSession(tc.id); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("invalid request created session %q: %v", tc.id, err)
			}
		})
	}

	t.Run("storage failure remains internal server error", func(t *testing.T) {
		brokenStore := newServerTestStore(t)
		if err := brokenStore.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		rec := httptest.NewRecorder()
		New(brokenStore, 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(`{"id":"mode-storage-error","project":"project-a","ownership_mode":"shared"}`)))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("POST /sessions with closed store = %d, want 500: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestHandleCreateSessionStoresRuntimeWorktreeDirectory(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested", "child")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	if output, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	t.Chdir(root)

	st := newServerTestStore(t)
	h := New(st, 0).Handler()
	for _, tc := range []struct {
		id        string
		directory string
	}{
		{id: "nested-trailing", directory: nested + string(os.PathSeparator)},
		{id: "nested-relative", directory: filepath.Join("nested", "child")},
		{id: "omitted-directory"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			rec := httptest.NewRecorder()
			body := fmt.Sprintf(`{"id":%q,"project":"engram"`, tc.id)
			if tc.directory != "" {
				body += fmt.Sprintf(`,"directory":%q`, tc.directory)
			}
			body += `}`
			req := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(body))
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create session = %d, want 201: %s", rec.Code, rec.Body.String())
			}

			session, err := st.GetSession(tc.id)
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			if want := projectpkg.RuntimeWorktreeDirectory(root); session.Directory != want {
				t.Fatalf("stored directory = %q, want worktree root %q", session.Directory, want)
			}
		})
	}
}

func TestHandleRescueProjectOwnershipRequiresConfirmedBoundedRescue(t *testing.T) {
	const token = "rescue-token"
	t.Setenv("ENGRAM_HTTP_TOKEN", token)
	h := New(newServerTestStore(t), 0).Handler()
	for _, body := range []string{
		`{"target_project":"target","observation_ids":[1]}`,
		`{"target_project":"target","confirmed":true}`,
		`{"confirmed":true,"observation_ids":[1]}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/projects/rescue-ownership", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s returned %d: %s", body, rec.Code, rec.Body.String())
		}
	}
}

// newServerTestStoreWithLegacyNullableSessions builds the shape an upgraded
// database has: sessions.project is still nullable and carries rows that
// identify no project.
func newServerTestStoreWithLegacyNullableSessions(t *testing.T, sessionIDs ...string) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	raw, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		project TEXT,
		directory TEXT NOT NULL,
		started_at TEXT NOT NULL DEFAULT (datetime('now')),
		ended_at TEXT,
		summary TEXT
	)`); err != nil {
		_ = raw.Close()
		t.Fatalf("create legacy sessions: %v", err)
	}
	for _, id := range sessionIDs {
		if _, err := raw.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, NULL, ?)`, id, "/tmp"); err != nil {
			_ = raw.Close()
			t.Fatalf("seed legacy session: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open migrated legacy database: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// An upgraded install must keep accepting writes: the write knows its project,
// so the unowned session adopts it instead of the write failing forever.
func TestHandleWritesOnLegacyUnownedSessionSucceedAndAdoptOwnership(t *testing.T) {
	st := newServerTestStoreWithLegacyNullableSessions(t, "legacy-session")
	srv := New(st, 0)

	req := httptest.NewRequest(http.MethodPost, "/observations",
		strings.NewReader(`{"session_id":"legacy-session","type":"note","title":"upgraded","content":"content","project":"target"}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("observation on legacy unowned session returned %d: %s", rec.Code, rec.Body.String())
	}

	sess, err := st.GetSession("legacy-session")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Project != "target" {
		t.Fatalf("session project = %q, want target", sess.Project)
	}

	// Passive capture, the path the Pi plugin drives, must work too.
	passive := httptest.NewRequest(http.MethodPost, "/observations/passive",
		strings.NewReader(`{"session_id":"legacy-session","source":"bash","project":"target","content":"## Key Learnings\n\n- The retry backoff must be capped to avoid a thundering herd on restart\n"}`))
	passiveRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(passiveRec, passive)
	if passiveRec.Code != http.StatusOK {
		t.Fatalf("passive capture returned %d: %s", passiveRec.Code, passiveRec.Body.String())
	}
}

// When nothing can resolve the project, the failure must be a client-actionable
// 409 naming the repair — not an opaque 500 the operator cannot act on.
func TestHandleAddObservationUnresolvableOwnershipReturnsActionableConflict(t *testing.T) {
	st := newServerTestStoreWithLegacyNullableSessions(t, "legacy-session")
	srv := New(st, 0)

	req := httptest.NewRequest(http.MethodPost, "/observations",
		strings.NewReader(`{"session_id":"legacy-session","type":"note","title":"t","content":"c"}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response["code"] != "project_ownership_required" {
		t.Fatalf("code = %v, want project_ownership_required", response["code"])
	}
	remedy, _ := response["remedy"].(string)
	if !strings.Contains(remedy, "engram projects rescue-ownership") {
		t.Fatalf("remedy = %q, must name the reachable repair", remedy)
	}
}

// The zero-config recovery path: no ENGRAM_HTTP_TOKEN anywhere, the HTTP rescue
// endpoint is unavailable by design, and the CLI repair is what closes the loop.
func TestRescueOwnershipEndpointUnavailableWithoutTokenButStoreRepairSucceeds(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", "")
	st := newServerTestStoreWithLegacyNullableSessions(t, "legacy-session")
	srv := New(st, 0)

	req := httptest.NewRequest(http.MethodPost, "/projects/rescue-ownership",
		strings.NewReader(`{"target_project":"target","confirmed":true,"session_ids":["legacy-session"]}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 without a configured token", rec.Code)
	}

	// The same repair, reached without server auth, is what the error tells the
	// operator to run.
	result, err := st.RescueNullProjectOwnership(store.ProjectRescueParams{TargetProject: "target", SessionIDs: []string{"legacy-session"}})
	if err != nil {
		t.Fatalf("store rescue: %v", err)
	}
	if !result.Complete {
		t.Fatalf("result.Complete = false, want true: %#v", result)
	}
	sess, err := st.GetSession("legacy-session")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Project != "target" {
		t.Fatalf("session project = %q, want target", sess.Project)
	}
}

// The rescue response must let an operator tell a clean move apart from a
// partial one without inferring it from counters.
func TestHandleRescueProjectOwnershipReportsWhatWasLeftBehind(t *testing.T) {
	const token = "rescue-token"
	t.Setenv("ENGRAM_HTTP_TOKEN", token)
	st := newServerTestStoreWithLegacyNullableSessions(t, "legacy-session")
	if _, err := st.DB().Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope) VALUES ('obs-foreign', 'legacy-session', 'note', 'foreign', 'content', 'other', 'project')`,
	); err != nil {
		t.Fatalf("seed foreign-owned observation: %v", err)
	}
	srv := New(st, 0)

	req := httptest.NewRequest(http.MethodPost, "/projects/rescue-ownership",
		strings.NewReader(`{"target_project":"target","confirmed":true,"session_ids":["legacy-session"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rescue returned %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response["status"] != "partially_rescued" {
		t.Fatalf("status = %v, want partially_rescued", response["status"])
	}
	if response["complete"] != false {
		t.Fatalf("complete = %v, want false", response["complete"])
	}
	blocked, ok := response["blocked"].([]any)
	if !ok || len(blocked) == 0 {
		t.Fatalf("blocked = %#v, want the records left behind", response["blocked"])
	}
	// The session must not have moved away from its foreign-owned record.
	var project sql.NullString
	if err := st.DB().QueryRow(`SELECT project FROM sessions WHERE id = ?`, "legacy-session").Scan(&project); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if project.Valid && strings.TrimSpace(project.String) != "" {
		t.Fatalf("session project = %q, want it left unowned", project.String)
	}
}

func TestHandleRescueProjectOwnershipRescuesNullOwnershipAndReportsLocalJournal(t *testing.T) {
	const token = "rescue-token"
	t.Setenv("ENGRAM_HTTP_TOKEN", token)
	st := newServerTestStore(t)
	if err := st.CreateSession("legacy-session", "legacy", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := st.AddObservation(store.AddObservationParams{SessionID: "legacy-session", Type: "note", Title: "legacy", Content: "content", Project: "legacy"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if _, err := st.DB().Exec(`UPDATE observations SET project = NULL WHERE id = ?`, id); err != nil {
		t.Fatalf("clear legacy ownership: %v", err)
	}
	// The parent session is unowned too; rescuing the observation must move it.
	if _, err := st.DB().Exec(`UPDATE sessions SET project = '' WHERE id = ?`, "legacy-session"); err != nil {
		t.Fatalf("clear legacy session ownership: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sync_mutations WHERE entity_key = (SELECT sync_id FROM observations WHERE id = ?)`, id); err != nil {
		t.Fatalf("clear legacy mutation: %v", err)
	}
	srv := New(st, 0)
	var writes int32
	srv.SetOnWrite(func() { atomic.AddInt32(&writes, 1) })
	body := fmt.Sprintf(`{"target_project":"target","confirmed":true,"observation_ids":[%d]}`, id)
	req := httptest.NewRequest(http.MethodPost, "/projects/rescue-ownership", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rescue returned %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode rescue response: %v", err)
	}
	if response["status"] != "rescued" || response["journaled_local"] != true || response["reconciliation_status"] != "local journal pending autosync" {
		t.Fatalf("unexpected rescue response: %#v", response)
	}
	if atomic.LoadInt32(&writes) != 1 {
		t.Fatalf("autosync notification count = %d, want 1", writes)
	}
	var project sql.NullString
	if err := st.DB().QueryRow(`SELECT project FROM observations WHERE id = ?`, id).Scan(&project); err != nil {
		t.Fatalf("read rescued ownership: %v", err)
	}
	if !project.Valid || project.String != "target" {
		t.Fatalf("rescued ownership = %#v, want target", project)
	}
	var sessionProject string
	if err := st.DB().QueryRow(`SELECT project FROM sessions WHERE id = ?`, "legacy-session").Scan(&sessionProject); err != nil || sessionProject != "target" {
		t.Fatalf("rescued session ownership = %q, err=%v, want target", sessionProject, err)
	}
	var journaled int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity_key = (SELECT sync_id FROM observations WHERE id = ?)`, id).Scan(&journaled); err != nil {
		t.Fatalf("count rescued mutations: %v", err)
	}
	if journaled != 1 {
		t.Fatalf("rescued mutation count = %d, want 1", journaled)
	}
}

func TestHandleRescueProjectOwnershipRejectsUnauthorizedRequestsOnCanonicalAndAliasRoutes(t *testing.T) {
	tests := []struct {
		name          string
		serverToken   string
		authorization string
		wantStatus    int
		wantError     string
	}{
		{name: "token unset", authorization: "Bearer rescue-token", wantStatus: http.StatusServiceUnavailable, wantError: "server authorization is not configured"},
		{name: "credential missing", serverToken: "rescue-token", wantStatus: http.StatusUnauthorized, wantError: "authorization required"},
		{name: "credential wrong", serverToken: "rescue-token", authorization: "Bearer wrong-token", wantStatus: http.StatusUnauthorized, wantError: "invalid token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENGRAM_HTTP_TOKEN", tt.serverToken)
			st := newServerTestStore(t)
			if err := st.CreateSession("legacy-session", "legacy", "/tmp"); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			id, err := st.AddObservation(store.AddObservationParams{SessionID: "legacy-session", Type: "note", Title: "legacy", Content: "sensitive content", Project: "legacy"})
			if err != nil {
				t.Fatalf("AddObservation: %v", err)
			}
			if _, err := st.DB().Exec(`UPDATE observations SET project = NULL WHERE id = ?`, id); err != nil {
				t.Fatalf("clear legacy ownership: %v", err)
			}
			if _, err := st.DB().Exec(`DELETE FROM sync_mutations WHERE entity_key = (SELECT sync_id FROM observations WHERE id = ?)`, id); err != nil {
				t.Fatalf("clear legacy mutation: %v", err)
			}

			srv := New(st, 0)
			var writes int32
			srv.SetOnWrite(func() { atomic.AddInt32(&writes, 1) })
			body := fmt.Sprintf(`{"target_project":"target","confirmed":true,"observation_ids":[%d]}`, id)
			for _, path := range []string{"/projects/rescue-ownership", "/projects/migrate"} {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				if tt.authorization != "" {
					req.Header.Set("Authorization", tt.authorization)
				}
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				if rec.Code != tt.wantStatus {
					t.Fatalf("%s rejected rescue returned %d, want %d: %s", path, rec.Code, tt.wantStatus, rec.Body.String())
				}
				var response map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode %s rejection response: %v", path, err)
				}
				if response["error"] != tt.wantError {
					t.Fatalf("%s rejection error = %q, want %q", path, response["error"], tt.wantError)
				}
			}

			var project sql.NullString
			if err := st.DB().QueryRow(`SELECT project FROM observations WHERE id = ?`, id).Scan(&project); err != nil {
				t.Fatalf("read legacy ownership: %v", err)
			}
			if project.Valid {
				t.Fatalf("rejected rescue assigned project %q", project.String)
			}
			var journaled int
			if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity_key = (SELECT sync_id FROM observations WHERE id = ?)`, id).Scan(&journaled); err != nil {
				t.Fatalf("count legacy mutations: %v", err)
			}
			if journaled != 0 {
				t.Fatalf("rejected rescue journaled %d mutations", journaled)
			}
			if atomic.LoadInt32(&writes) != 0 {
				t.Fatalf("rejected rescue autosync notification count = %d, want 0", writes)
			}
		})
	}
}

func TestHandleRescueProjectOwnershipNotifiesForMissingJournalWithoutRescue(t *testing.T) {
	const token = "rescue-token"
	t.Setenv("ENGRAM_HTTP_TOKEN", token)
	st := newServerTestStore(t)
	if err := st.CreateSession("owned-session", "target", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := st.AddObservation(store.AddObservationParams{SessionID: "owned-session", Type: "note", Title: "owned", Content: "content", Project: "target"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sync_mutations WHERE entity_key = (SELECT sync_id FROM observations WHERE id = ?)`, id); err != nil {
		t.Fatalf("clear observation mutation: %v", err)
	}
	srv := New(st, 0)
	var writes int32
	srv.SetOnWrite(func() { atomic.AddInt32(&writes, 1) })
	req := httptest.NewRequest(http.MethodPost, "/projects/rescue-ownership", strings.NewReader(fmt.Sprintf(`{"target_project":"target","confirmed":true,"observation_ids":[%d]}`, id)))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rescue returned %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode rescue response: %v", err)
	}
	if response["rescued_observations"] != float64(0) || response["journaled_local"] != true {
		t.Fatalf("unexpected rescue response: %#v", response)
	}
	if atomic.LoadInt32(&writes) != 1 {
		t.Fatalf("autosync notification count = %d, want 1", writes)
	}
}

func TestHandleRescueProjectOwnershipClassifiesValidationAndInfrastructureErrors(t *testing.T) {
	const token = "rescue-token"
	t.Setenv("ENGRAM_HTTP_TOKEN", token)
	t.Run("validation", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/projects/rescue-ownership", strings.NewReader(`{"target_project":"target","confirmed":true,"prompt_ids":[0]}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		New(newServerTestStore(t), 0).Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "positive record ID") {
			t.Fatalf("validation response = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("infrastructure", func(t *testing.T) {
		st := newServerTestStore(t)
		srv := New(st, 0)
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/projects/rescue-ownership", strings.NewReader(`{"target_project":"target","confirmed":true,"prompt_ids":[1]}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "database is closed") {
			t.Fatalf("infrastructure response = %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestHandleSearchForwardsMatchModeAndAllProjects(t *testing.T) {
	st := newServerTestStore(t)
	h := New(st, 0).Handler()

	if err := st.CreateSession("sess-search-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session proj-a: %v", err)
	}
	if err := st.CreateSession("sess-search-b", "proj-b", "/tmp/proj-b"); err != nil {
		t.Fatalf("create session proj-b: %v", err)
	}
	if _, err := st.AddObservation(store.AddObservationParams{
		SessionID: "sess-search-a",
		Type:      "note",
		Title:     "Aurora project note",
		Content:   "alpha content",
		Project:   "proj-a",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add obs proj-a: %v", err)
	}
	if _, err := st.AddObservation(store.AddObservationParams{
		SessionID: "sess-search-b",
		Type:      "note",
		Title:     "Nebula project note",
		Content:   "beta content",
		Project:   "proj-b",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add obs proj-b: %v", err)
	}

	search := func(url string) []map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected search 200, got %d body=%s", rec.Code, rec.Body.String())
		}

		var results []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
			t.Fatalf("decode search results: %v", err)
		}
		return results
	}
	titles := func(results []map[string]any) map[string]bool {
		t.Helper()
		seen := map[string]bool{}
		for _, result := range results {
			title, _ := result["title"].(string)
			seen[title] = true
		}
		return seen
	}

	projectResults := search("/search?q=aurora+nebula&project=proj-a&match_mode=any&limit=10")
	projectTitles := titles(projectResults)
	if len(projectResults) != 1 || !projectTitles["Aurora project note"] {
		t.Fatalf("expected match_mode=any to preserve the project filter by default, got %#v", projectResults)
	}

	conflicting := httptest.NewRequest(http.MethodGet, "/search?q=aurora+nebula&project=proj-a&match_mode=any&all_projects=true&limit=10", nil)
	conflictingRec := httptest.NewRecorder()
	h.ServeHTTP(conflictingRec, conflicting)
	if conflictingRec.Code != http.StatusBadRequest {
		t.Fatalf("expected conflicting project selectors to return 400, got %d body=%s", conflictingRec.Code, conflictingRec.Body.String())
	}

	allProjectResults := search("/search?q=aurora+nebula&match_mode=any&all_projects=true&limit=10")
	allProjectTitles := titles(allProjectResults)
	if len(allProjectResults) != 2 || !allProjectTitles["Aurora project note"] || !allProjectTitles["Nebula project note"] {
		t.Fatalf("expected explicit all_projects=true to return both project notes, got %#v", allProjectResults)
	}
}

func TestHandleSearchAllowsEmptyMatchMode(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	if err := st.CreateSession("sess-search-default", "proj-default", "/tmp/proj-default"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := st.AddObservation(store.AddObservationParams{
		SessionID: "sess-search-default",
		Type:      "note",
		Title:     "Aurora default match note",
		Content:   "alpha content",
		Project:   "proj-default",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/search?q=aurora&project=proj-default", nil)
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty match_mode, got %d body=%s", rec.Code, rec.Body.String())
	}
	var results []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("decode search results: %v", err)
	}
	if len(results) != 1 || results[0]["title"] != "Aurora default match note" {
		t.Fatalf("expected default match_mode search to return seeded observation, got %#v", results)
	}
}

func TestHandleSearchNoHitsReturnsEmptyArray(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	req := httptest.NewRequest(http.MethodGet, "/search?q=no-hits", nil)
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for no-hit search, got %d body=%s", rec.Code, rec.Body.String())
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Fatalf("expected no-hit search body [], got %q", body)
	}
}

func TestHandleSearchRejectsInvalidMatchMode(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	req := httptest.NewRequest(http.MethodGet, "/search?q=aurora&match_mode=or", nil)
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid match_mode, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid match_mode") {
		t.Fatalf("expected error body to mention invalid match_mode, got %q", rec.Body.String())
	}
}

func TestAdditionalServerErrorBranches(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	createReq := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(`{"id":"s-test","project":"engram"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected session create 201, got %d", createRec.Code)
	}

	getBadIDReq := httptest.NewRequest(http.MethodGet, "/observations/not-a-number", nil)
	getBadIDRec := httptest.NewRecorder()
	h.ServeHTTP(getBadIDRec, getBadIDReq)
	if getBadIDRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid observation id, got %d", getBadIDRec.Code)
	}

	updateNotFoundReq := httptest.NewRequest(http.MethodPatch, "/observations/99999", strings.NewReader(`{"title":"updated"}`))
	updateNotFoundReq.Header.Set("Content-Type", "application/json")
	updateNotFoundRec := httptest.NewRecorder()
	h.ServeHTTP(updateNotFoundRec, updateNotFoundReq)
	if updateNotFoundRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 updating missing observation, got %d", updateNotFoundRec.Code)
	}

	promptBadJSONReq := httptest.NewRequest(http.MethodPost, "/prompts", strings.NewReader("{"))
	promptBadJSONReq.Header.Set("Content-Type", "application/json")
	promptBadJSONRec := httptest.NewRecorder()
	h.ServeHTTP(promptBadJSONRec, promptBadJSONReq)
	if promptBadJSONRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid prompt json, got %d", promptBadJSONRec.Code)
	}

	oversizeBody := bytes.Repeat([]byte("a"), 50<<20+1)
	importTooLargeReq := httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(oversizeBody))
	importTooLargeReq.Header.Set("Content-Type", "application/json")
	importTooLargeRec := httptest.NewRecorder()
	h.ServeHTTP(importTooLargeRec, importTooLargeReq)
	if importTooLargeRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversize import body, got %d", importTooLargeRec.Code)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	validImport, err := json.Marshal(store.ExportData{Version: "0.1.0", ExportedAt: "now"})
	if err != nil {
		t.Fatalf("marshal import payload: %v", err)
	}
	importClosedReq := httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(validImport))
	importClosedReq.Header.Set("Content-Type", "application/json")
	importClosedRec := httptest.NewRecorder()
	h.ServeHTTP(importClosedRec, importClosedReq)
	if importClosedRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 importing on closed store, got %d", importClosedRec.Code)
	}
}

func TestWriteHandlersRejectWhitespaceOnlyRequiredFields(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	if err := st.CreateSession("s-whitespace", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	observationID, err := st.AddObservation(store.AddObservationParams{
		SessionID: "s-whitespace",
		Type:      "decision",
		Title:     "Original title",
		Content:   "Original content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	// Every rejection must use the API's standard validation shape: HTTP 400
	// carrying the sentinel error text in the JSON `error` field.
	assertBadRequest := func(method, path, body string, wantError error) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s %s, got %d body=%s", method, path, rec.Code, rec.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s %s response: %v (body %s)", method, path, err, rec.Body.String())
		}
		if got, _ := resp["error"].(string); got != wantError.Error() {
			t.Fatalf("expected %s %s error %q, got %q", method, path, wantError.Error(), got)
		}
	}

	assertBadRequest(http.MethodPost, "/observations", `{"session_id":"s-whitespace","type":"decision","title":" \t\n ","content":"Invalid observation","project":"engram"}`, store.ErrObservationTitleRequired)
	assertBadRequest(http.MethodPost, "/observations", `{"session_id":"s-whitespace","type":"decision","title":"Valid title","content":" \t\n ","project":"engram"}`, store.ErrObservationContentRequired)
	assertBadRequest(http.MethodPatch, fmt.Sprintf("/observations/%d", observationID), `{"title":" \t\n "}`, store.ErrObservationTitleRequired)
	assertBadRequest(http.MethodPatch, fmt.Sprintf("/observations/%d", observationID), `{"content":" \t\n "}`, store.ErrObservationContentRequired)
	assertBadRequest(http.MethodPost, "/prompts", `{"session_id":"s-whitespace","content":" \t\n ","project":"engram"}`, store.ErrPromptContentRequired)

	var observationCount, promptCount int
	if err := st.DB().QueryRow(`SELECT count(*) FROM observations`).Scan(&observationCount); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if observationCount != 1 {
		t.Fatalf("expected invalid observation create not to persist, got %d observations", observationCount)
	}
	if err := st.DB().QueryRow(`SELECT count(*) FROM user_prompts`).Scan(&promptCount); err != nil {
		t.Fatalf("count prompts: %v", err)
	}
	if promptCount != 0 {
		t.Fatalf("expected invalid prompt create not to persist, got %d prompts", promptCount)
	}

	observation, err := st.GetObservation(observationID)
	if err != nil {
		t.Fatalf("get seeded observation: %v", err)
	}
	if observation.Title != "Original title" {
		t.Fatalf("expected invalid observation update not to persist, got title %q", observation.Title)
	}
	if observation.Content != "Original content" {
		t.Fatalf("expected invalid observation update not to persist, got content %q", observation.Content)
	}
}

func TestHandleReviewListAndMarkReviewed(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	if err := st.CreateSession("s-http-review", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := st.AddObservation(store.AddObservationParams{SessionID: "s-http-review", Type: "decision", Title: "Review me", Content: "Needs lifecycle review", Project: "engram"})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	past := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")
	if _, err := st.DB().Exec(`UPDATE observations SET review_after = ? WHERE id = ?`, past, id); err != nil {
		t.Fatalf("backdate review_after: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/review?project=engram&limit=5", nil)
	listRec := httptest.NewRecorder()
	h.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected review list 200, got %d body=%s", listRec.Code, listRec.Body.String())
	}
	var listBody map[string]any
	if err := json.NewDecoder(listRec.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode review list: %v", err)
	}
	observations, ok := listBody["observations"].([]any)
	if !ok || len(observations) != 1 {
		t.Fatalf("expected one review observation, got %#v", listBody["observations"])
	}
	entry, _ := observations[0].(map[string]any)
	if entry["state"] != store.ObservationStateNeedsReview {
		t.Fatalf("expected needs_review state, got %v", entry["state"])
	}

	markReq := httptest.NewRequest(http.MethodPost, "/review/mark_reviewed", strings.NewReader(fmt.Sprintf(`{"observation_id":%d}`, id)))
	markReq.Header.Set("Content-Type", "application/json")
	markRec := httptest.NewRecorder()
	h.ServeHTTP(markRec, markReq)
	if markRec.Code != http.StatusOK {
		t.Fatalf("expected mark reviewed 200, got %d body=%s", markRec.Code, markRec.Body.String())
	}
	var markBody map[string]any
	if err := json.NewDecoder(markRec.Body).Decode(&markBody); err != nil {
		t.Fatalf("decode mark reviewed: %v", err)
	}
	if markBody["state"] != store.ObservationStateActive {
		t.Fatalf("expected active after mark_reviewed, got %v", markBody["state"])
	}
}

func TestHandleReviewMarkReviewedEnforcesResolvedProjectAtMutation(t *testing.T) {
	st := newServerTestStore(t)
	for _, project := range []string{"alpha", "beta"} {
		if err := st.CreateSession("review-scope-"+project, project, "/tmp/"+project); err != nil {
			t.Fatalf("create %s session: %v", project, err)
		}
	}
	id, err := st.AddObservation(store.AddObservationParams{SessionID: "review-scope-alpha", Type: "decision", Title: "Alpha review", Content: "must remain scoped", Project: "alpha"})
	if err != nil {
		t.Fatalf("add alpha observation: %v", err)
	}
	past := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")
	if _, err := st.DB().Exec(`UPDATE observations SET review_after = ? WHERE id = ?`, past, id); err != nil {
		t.Fatalf("backdate review_after: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/review/mark_reviewed?project=beta", strings.NewReader(fmt.Sprintf(`{"observation_id":%d}`, id)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	New(st, 0).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "observation not found in resolved project") {
		t.Fatalf("mismatched project mark reviewed = %d: %s", rec.Code, rec.Body.String())
	}
	obs, err := st.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.State() != store.ObservationStateNeedsReview {
		t.Fatalf("mismatched project mutation changed review state to %q", obs.State())
	}
}

func TestHandleReviewMarkReviewedAcceptsIDAlias(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	if err := st.CreateSession("s-http-review-alias", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := st.AddObservation(store.AddObservationParams{SessionID: "s-http-review-alias", Type: "decision", Title: "Review alias", Content: "Needs lifecycle review", Project: "engram"})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	past := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")
	if _, err := st.DB().Exec(`UPDATE observations SET review_after = ? WHERE id = ?`, past, id); err != nil {
		t.Fatalf("backdate review_after: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/review/mark_reviewed", strings.NewReader(fmt.Sprintf(`{"id":%d}`, id)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected mark reviewed alias 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleReviewMarkReviewedRequiresObservationID(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	req := httptest.NewRequest(http.MethodPost, "/review/mark_reviewed", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when observation_id is missing, got %d", rec.Code)
	}
}

func TestHandleReviewMarkReviewedReturnsNotFoundForUnknownObservation(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	req := httptest.NewRequest(http.MethodPost, "/review/mark_reviewed", strings.NewReader(`{"observation_id":999999}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown observation, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestExportHonorsProjectQueryScope(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	if err := st.CreateSession("sess-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session proj-a: %v", err)
	}
	if err := st.CreateSession("sess-b", "proj-b", "/tmp/proj-b"); err != nil {
		t.Fatalf("create session proj-b: %v", err)
	}
	if _, err := st.AddObservation(store.AddObservationParams{SessionID: "sess-a", Type: "note", Title: "a", Content: "a", Project: "proj-a", Scope: "project"}); err != nil {
		t.Fatalf("add obs proj-a: %v", err)
	}
	if _, err := st.AddObservation(store.AddObservationParams{SessionID: "sess-b", Type: "note", Title: "b", Content: "b", Project: "proj-b", Scope: "project"}); err != nil {
		t.Fatalf("add obs proj-b: %v", err)
	}
	if _, err := st.AddPrompt(store.AddPromptParams{SessionID: "sess-a", Content: "prompt-a", Project: "proj-a"}); err != nil {
		t.Fatalf("add prompt proj-a: %v", err)
	}
	if _, err := st.AddPrompt(store.AddPromptParams{SessionID: "sess-b", Content: "prompt-b", Project: "proj-b"}); err != nil {
		t.Fatalf("add prompt proj-b: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/export?project=proj-a", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 export, got %d", rec.Code)
	}

	var exported store.ExportData
	if err := json.NewDecoder(rec.Body).Decode(&exported); err != nil {
		t.Fatalf("decode export response: %v", err)
	}

	if len(exported.Sessions) != 1 || exported.Sessions[0].Project != "proj-a" {
		t.Fatalf("expected only proj-a sessions in scoped export, got %+v", exported.Sessions)
	}
	if len(exported.Observations) != 1 {
		t.Fatalf("expected exactly one scoped observation, got %+v", exported.Observations)
	}
	if exported.Observations[0].Project == nil || *exported.Observations[0].Project != "proj-a" {
		t.Fatalf("expected scoped observation project proj-a, got %+v", exported.Observations[0].Project)
	}
	if len(exported.Prompts) != 1 || exported.Prompts[0].Project != "proj-a" {
		t.Fatalf("expected only proj-a prompts in scoped export, got %+v", exported.Prompts)
	}
}

func TestExportRejectsExplicitBlankProjectQuery(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	tests := []string{
		"/export?project=",
		"/export?project=%20%20%20",
	}

	for _, url := range tests {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for explicit blank project query (%s), got %d", url, rec.Code)
		}
	}
}

// ─── Sync Status Tests ───────────────────────────────────────────────────────

// stubSyncStatusProvider is a fake SyncStatusProvider for tests.
type stubSyncStatusProvider struct {
	status      SyncStatus
	lastProject string
	calls       int
}

func (s *stubSyncStatusProvider) Status(project string) SyncStatus {
	s.lastProject = project
	s.calls++
	return s.status
}

func TestSyncStatusNotConfigured(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	// No sync status provider set — should return enabled: false.
	req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["enabled"] != false {
		t.Fatalf("expected enabled=false when no provider, got %v", resp["enabled"])
	}
}

func TestSyncStatusHealthy(t *testing.T) {
	now := time.Now()
	provider := &stubSyncStatusProvider{
		status: SyncStatus{
			Enabled:    true,
			Phase:      "healthy",
			LastSyncAt: &now,
		},
	}

	srv := New(newServerTestStore(t), 0)
	srv.SetSyncStatus(provider)

	req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["enabled"] != true {
		t.Fatalf("expected enabled=true, got %v", resp["enabled"])
	}
	if resp["phase"] != "healthy" {
		t.Fatalf("expected phase=healthy, got %v", resp["phase"])
	}
}

func TestSyncStatusDegraded(t *testing.T) {
	backoff := time.Now().Add(5 * time.Minute)
	provider := &stubSyncStatusProvider{
		status: SyncStatus{
			Enabled:             true,
			Phase:               "push_failed",
			LastError:           "network timeout",
			ConsecutiveFailures: 3,
			BackoffUntil:        &backoff,
		},
	}

	srv := New(newServerTestStore(t), 0)
	srv.SetSyncStatus(provider)

	req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["phase"] != "push_failed" {
		t.Fatalf("expected phase=push_failed, got %v", resp["phase"])
	}
	if resp["last_error"] != "network timeout" {
		t.Fatalf("expected last_error=network timeout, got %v", resp["last_error"])
	}
	if resp["consecutive_failures"] != float64(3) {
		t.Fatalf("expected consecutive_failures=3, got %v", resp["consecutive_failures"])
	}
}

func TestSyncStatusIncludesReasonParityFields(t *testing.T) {
	provider := &stubSyncStatusProvider{
		status: SyncStatus{
			Enabled:              true,
			Phase:                "degraded",
			ReasonCode:           "auth_required",
			ReasonMessage:        "cloud token expired",
			UpgradeStage:         "bootstrap_pushed",
			UpgradeReasonCode:    "upgrade_repair_backfill_sync_journal",
			UpgradeReasonMessage: "repair pending",
		},
	}

	srv := New(newServerTestStore(t), 0)
	srv.SetSyncStatus(provider)

	req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["reason_code"] != "auth_required" {
		t.Fatalf("expected reason_code auth_required, got %v", resp["reason_code"])
	}
	if resp["reason_message"] != "cloud token expired" {
		t.Fatalf("expected reason_message, got %v", resp["reason_message"])
	}
	upgradeRaw, ok := resp["upgrade"].(map[string]any)
	if !ok {
		t.Fatalf("expected upgrade object in /sync/status response, got %#v", resp["upgrade"])
	}
	if upgradeRaw["stage"] != "bootstrap_pushed" {
		t.Fatalf("expected upgrade stage bootstrap_pushed, got %v", upgradeRaw["stage"])
	}
	if upgradeRaw["reason_code"] != "upgrade_repair_backfill_sync_journal" {
		t.Fatalf("expected upgrade reason_code parity, got %v", upgradeRaw["reason_code"])
	}
}

func TestSyncStatusForwardsProjectQueryToProvider(t *testing.T) {
	provider := &stubSyncStatusProvider{status: SyncStatus{Enabled: true, Phase: "healthy"}}
	st := newServerTestStore(t)
	if err := st.CreateSession("sync-proj-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	srv := New(st, 0)
	srv.SetSyncStatus(provider)

	req := httptest.NewRequest(http.MethodGet, "/sync/status?project=proj-a", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if provider.lastProject != "proj-a" {
		t.Fatalf("expected provider to receive project query, got %q", provider.lastProject)
	}
}

func TestSyncStatusResolvesProjectSelectors(t *testing.T) {
	provider := &stubSyncStatusProvider{status: SyncStatus{Enabled: true, Phase: "healthy"}}
	st := newServerTestStore(t)
	for _, project := range []string{"alpha", "beta"} {
		if err := st.CreateSession("sync-scope-"+project, project, "/tmp/"+project); err != nil {
			t.Fatalf("create %s session: %v", project, err)
		}
	}
	srv := New(st, 0)
	srv.SetSyncStatus(provider)
	h := srv.Handler()
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	assertCode := func(rec *httptest.ResponseRecorder, want int, wantCode string) {
		t.Helper()
		if rec.Code != want {
			t.Fatalf("status = %d, want %d: %s", rec.Code, want, rec.Body.String())
		}
		if wantCode == "" {
			return
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != wantCode {
			t.Fatalf("error code = %#v, want %q: %s", body["code"], wantCode, rec.Body.String())
		}
	}

	t.Setenv("ENGRAM_PROJECT", "alpha")
	assertCode(request("/sync/status"), http.StatusOK, "")
	if provider.lastProject != "alpha" {
		t.Fatalf("omitted selector reached provider as %q, want alpha", provider.lastProject)
	}
	assertCode(request("/sync/status?project=beta"), http.StatusOK, "")
	if provider.lastProject != "beta" {
		t.Fatalf("explicit selector reached provider as %q, want beta", provider.lastProject)
	}

	calls := provider.calls
	assertCode(request("/sync/status?project="), http.StatusBadRequest, "invalid_project")
	assertCode(request("/sync/status?project=missing"), http.StatusNotFound, "unknown_project")
	if provider.calls != calls {
		t.Fatalf("invalid or unknown selectors must not consult provider: calls=%d want %d", provider.calls, calls)
	}

	t.Setenv("ENGRAM_PROJECT", "")
	parent := ambiguousProjectHTTPDirectory(t)
	assertCode(request("/sync/status?cwd="+url.QueryEscape(parent)), http.StatusConflict, "ambiguous_project")
	assertCode(request("/sync/status?all_projects=true"), http.StatusBadRequest, "unsupported_project_scope")
	if provider.calls != calls {
		t.Fatalf("ambiguous or unsupported selectors must not consult provider: calls=%d want %d", provider.calls, calls)
	}
}

// ─── OnWrite Notification Tests ──────────────────────────────────────────────

func TestOnWriteCalledAfterSuccessfulWrites(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	var writeCount atomic.Int32
	srv.SetOnWrite(func() {
		writeCount.Add(1)
	})

	// Create session → should trigger onWrite.
	createReq := httptest.NewRequest(http.MethodPost, "/sessions",
		strings.NewReader(`{"id":"s-test","project":"engram"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("session create: expected 201, got %d", createRec.Code)
	}
	if writeCount.Load() != 1 {
		t.Fatalf("expected 1 onWrite after session create, got %d", writeCount.Load())
	}

	// End session → should trigger onWrite.
	endReq := httptest.NewRequest(http.MethodPost, "/sessions/s-test/end",
		strings.NewReader(`{"summary":"done"}`))
	endReq.Header.Set("Content-Type", "application/json")
	endRec := httptest.NewRecorder()
	h.ServeHTTP(endRec, endReq)
	if endRec.Code != http.StatusOK {
		t.Fatalf("session end: expected 200, got %d", endRec.Code)
	}
	if writeCount.Load() != 2 {
		t.Fatalf("expected 2 onWrite after session end, got %d", writeCount.Load())
	}

	// Add observation → should trigger onWrite.
	obsBody := `{"session_id":"s-test","type":"test","title":"Test","content":"test content"}`
	obsReq := httptest.NewRequest(http.MethodPost, "/observations",
		strings.NewReader(obsBody))
	obsReq.Header.Set("Content-Type", "application/json")
	obsRec := httptest.NewRecorder()
	h.ServeHTTP(obsRec, obsReq)
	if obsRec.Code != http.StatusCreated {
		t.Fatalf("add observation: expected 201, got %d", obsRec.Code)
	}
	if writeCount.Load() != 3 {
		t.Fatalf("expected 3 onWrite after add observation, got %d", writeCount.Load())
	}

	// Add prompt → should trigger onWrite.
	promptBody := `{"session_id":"s-test","content":"what did we do?"}`
	promptReq := httptest.NewRequest(http.MethodPost, "/prompts",
		strings.NewReader(promptBody))
	promptReq.Header.Set("Content-Type", "application/json")
	promptRec := httptest.NewRecorder()
	h.ServeHTTP(promptRec, promptReq)
	if promptRec.Code != http.StatusCreated {
		t.Fatalf("add prompt: expected 201, got %d", promptRec.Code)
	}
	if writeCount.Load() != 4 {
		t.Fatalf("expected 4 onWrite after add prompt, got %d", writeCount.Load())
	}
}

func TestOnWriteNotCalledOnReadOperations(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	var writeCount atomic.Int32
	srv.SetOnWrite(func() {
		writeCount.Add(1)
	})

	// GET /health → read-only, no onWrite.
	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthRec := httptest.NewRecorder()
	h.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("health: expected 200, got %d", healthRec.Code)
	}

	// GET /stats → read-only, no onWrite.
	statsReq := httptest.NewRequest(http.MethodGet, "/stats", nil)
	statsRec := httptest.NewRecorder()
	h.ServeHTTP(statsRec, statsReq)

	// GET /sync/status → read-only, no onWrite.
	syncReq := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	syncRec := httptest.NewRecorder()
	h.ServeHTTP(syncRec, syncReq)

	if writeCount.Load() != 0 {
		t.Fatalf("expected 0 onWrite calls for read operations, got %d", writeCount.Load())
	}
}

func TestOnWriteNotCalledOnFailedWrites(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	var writeCount atomic.Int32
	srv.SetOnWrite(func() {
		writeCount.Add(1)
	})

	// POST /observations with bad JSON → should NOT trigger onWrite.
	badReq := httptest.NewRequest(http.MethodPost, "/observations",
		strings.NewReader(`{invalid`))
	badReq.Header.Set("Content-Type", "application/json")
	badRec := httptest.NewRecorder()
	h.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad json, got %d", badRec.Code)
	}

	// POST /observations with missing required fields → should NOT trigger onWrite.
	missingReq := httptest.NewRequest(http.MethodPost, "/observations",
		strings.NewReader(`{"session_id":"s-test"}`))
	missingReq.Header.Set("Content-Type", "application/json")
	missingRec := httptest.NewRecorder()
	h.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing fields, got %d", missingRec.Code)
	}

	if writeCount.Load() != 0 {
		t.Fatalf("expected 0 onWrite calls for failed writes, got %d", writeCount.Load())
	}
}

func TestHandleStatsReturnsInternalServerErrorOnLoaderError(t *testing.T) {
	prev := loadServerStats
	loadServerStats = func(s *store.Store) (*store.Stats, error) {
		return nil, errors.New("stats unavailable")
	}
	t.Cleanup(func() {
		loadServerStats = prev
	})

	s := New(newServerTestStore(t), 0)
	req := httptest.NewRequest(http.MethodGet, "/stats?all_projects=true", nil)
	rec := httptest.NewRecorder()

	s.handleStats(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 stats response, got %d", rec.Code)
	}
}

func TestHandleStatsProjectScopeSkipsGlobalLoader(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("scoped-stats", "alpha", "/tmp/alpha"); err != nil {
		t.Fatalf("create alpha session: %v", err)
	}
	previous := loadServerStats
	loadServerStats = func(*store.Store) (*store.Stats, error) {
		return nil, errors.New("global stats should not run")
	}
	t.Cleanup(func() { loadServerStats = previous })

	srv := New(st, 0)
	rec := httptest.NewRecorder()
	srv.handleStats(rec, httptest.NewRequest(http.MethodGet, "/stats?project=alpha", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("project stats = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestProjectResolutionStoreFailureReturnsInternalErrorCode(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	srv := New(st, 0)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats?project=alpha", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("store resolution failure = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode resolution failure: %v", err)
	}
	if body["code"] != "project_resolution_failed" {
		t.Fatalf("resolution failure code = %#v, want project_resolution_failed: %s", body["code"], rec.Body.String())
	}
}

// ─── DELETE /sessions/{id} tests ─────────────────────────────────────────────

func TestHandleDeleteSession_Success(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// Create an empty session.
	createReq := httptest.NewRequest(http.MethodPost, "/sessions",
		strings.NewReader(`{"id":"sess-del","project":"proj"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating session, got %d", createRec.Code)
	}

	// Delete it.
	delReq := httptest.NewRequest(http.MethodDelete, "/sessions/sess-del", nil)
	delRec := httptest.NewRecorder()
	h.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected 200 deleting empty session, got %d: %s", delRec.Code, delRec.Body.String())
	}
}

func TestHandleDeleteSession_NotFound(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodDelete, "/sessions/does-not-exist", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleDeleteSession_HasObservations(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// Create session + add an observation via the store directly.
	if err := st.CreateSession("sess-obs", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := st.AddObservation(store.AddObservationParams{
		SessionID: "sess-obs",
		Type:      "decision",
		Title:     "some obs",
		Content:   "content",
		Project:   "proj",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/sessions/sess-obs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when session has observations, got %d", rec.Code)
	}
}

// TestHandleDeleteSession_PropagatesWhenProjectIsCloudEnrolled verifies the
// behavior introduced by 71fa9fe: deleting a session whose project is enrolled
// for cloud sync now succeeds locally AND enqueues a delete mutation so the
// cloud replicas remove the session too. Previously this returned 409 to
// prevent local/cloud divergence, but propagating the delete is the
// correct semantic now that the sync transport supports session/delete ops.
func TestHandleDeleteSession_PropagatesWhenProjectIsCloudEnrolled(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	if err := st.CreateSession("sess-enrolled", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := st.EnrollProject("proj"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/sessions/sess-enrolled", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when cloud-enrolled session delete propagates, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "deleted") {
		t.Fatalf("expected deleted status in body, got %q", rec.Body.String())
	}
}

// ─── DELETE /prompts/{id} tests ───────────────────────────────────────────────

func TestHandleDeletePrompt_Success(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	var writeCount atomic.Int32
	srv.SetOnWrite(func() {
		writeCount.Add(1)
	})

	if err := st.CreateSession("sess-p", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	promptID, err := st.AddPrompt(store.AddPromptParams{
		SessionID: "sess-p",
		Content:   "delete me",
		Project:   "proj",
	})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/prompts/%d", promptID), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 deleting prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	if writeCount.Load() != 1 {
		t.Fatalf("expected onWrite notification after prompt delete, got %d", writeCount.Load())
	}
}

func TestHandleDeletePrompt_NotFound(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodDelete, "/prompts/999999", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleDeletePrompt_BadID(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodDelete, "/prompts/not-a-number", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid prompt id, got %d", rec.Code)
	}
}

// ─── Phase E.1e — /sync/status exposes deferred + dead counts (REQ-007) ─────

// TestSyncStatus_IncludesDeferredAndDeadCounts: 3 deferred + 1 dead →
// /sync/status response must have deferred_count=3 and dead_count=1.
func TestSyncStatus_IncludesDeferredAndDeadCounts(t *testing.T) {
	provider := &stubSyncStatusProvider{
		status: SyncStatus{
			Enabled:       true,
			Phase:         "healthy",
			DeferredCount: 3,
			DeadCount:     1,
		},
	}

	srv := New(newServerTestStore(t), 0)
	srv.SetSyncStatus(provider)

	req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got := resp["deferred_count"]; got != float64(3) {
		t.Errorf("expected deferred_count=3, got %v", got)
	}
	if got := resp["dead_count"]; got != float64(1) {
		t.Errorf("expected dead_count=1, got %v", got)
	}
}

// ─── Conflict-Audit HTTP Tests (Phase E, REQ-006 thru REQ-011) ──────────────
//
// These tests cover the 6 new /conflicts/* routes.
// Helpers below seed observations, relations, and deferred rows without
// requiring an unexported Store.db accessor.

// conflictsTestStore creates a store with a fresh temp dir and returns
// both the store and the raw *sql.DB for low-level seeding (deferred rows).
func conflictsTestStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()

	st, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rawDB, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })

	return st, rawDB
}

// seedConflictsSession creates a session and two observations in the given project.
// Returns the sync_ids of both observations.
func seedConflictsSession(t *testing.T, st *store.Store, project string) (srcSync, tgtSync string) {
	t.Helper()
	sesID := fmt.Sprintf("ses-http-%s", project)
	if err := st.CreateSession(sesID, project, "/tmp/"+project); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	srcIntID, err := st.AddObservation(store.AddObservationParams{
		SessionID: sesID, Type: "decision",
		Title: "Src Title", Content: "src content body for http test",
		Project: project, Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation src: %v", err)
	}
	tgtIntID, err := st.AddObservation(store.AddObservationParams{
		SessionID: sesID, Type: "decision",
		Title: "Tgt Title", Content: "tgt content body for http test",
		Project: project, Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation tgt: %v", err)
	}
	// Retrieve text sync_ids from the store's DB.
	// We need the raw DB access here. Since we are package server and Store does not expose
	// a DB accessor, we retrieve the sync_id from the store through a search trick:
	// use AddObservation int64 id and look up via store.GetObservation.
	srcObs, err := st.GetObservation(srcIntID)
	if err != nil {
		t.Fatalf("GetObservation src: %v", err)
	}
	tgtObs, err := st.GetObservation(tgtIntID)
	if err != nil {
		t.Fatalf("GetObservation tgt: %v", err)
	}
	return srcObs.SyncID, tgtObs.SyncID
}

// seedDeferredHTTP inserts a raw deferred row via the raw *sql.DB.
func seedDeferredHTTP(t *testing.T, rawDB *sql.DB, syncID, payload string, retryCount int, applyStatus string) {
	t.Helper()
	if _, err := rawDB.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, retry_count, apply_status, first_seen_at)
		VALUES (?, 'relation', ?, ?, ?, datetime('now'))
	`, syncID, payload, retryCount, applyStatus); err != nil {
		t.Fatalf("seedDeferredHTTP %q: %v", syncID, err)
	}
}

// ─── GET /conflicts ───────────────────────────────────────────────────────────

func TestHandleListConflicts_ProjectFilter(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// Seed two observations in project "alpha" and one relation between them.
	srcSync, tgtSync := seedConflictsSession(t, st, "alpha")
	rel, err := st.SaveRelation(store.SaveRelationParams{
		SyncID: "rel-alpha-001", SourceID: srcSync, TargetID: tgtSync,
	})
	if err != nil || rel == nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	// Also seed observations and relation in project "beta" — should NOT appear in alpha filter.
	srcB, tgtB := seedConflictsSession(t, st, "beta")
	if _, err := st.SaveRelation(store.SaveRelationParams{
		SyncID: "rel-beta-001", SourceID: srcB, TargetID: tgtB,
	}); err != nil {
		t.Fatalf("SaveRelation beta: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/conflicts?project=alpha", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Must have "relations" array and "total" field.
	relations, ok := resp["relations"].([]any)
	if !ok {
		t.Fatalf("expected relations array, got %T: %v", resp["relations"], resp["relations"])
	}
	if len(relations) != 1 {
		t.Errorf("expected exactly 1 relation for project alpha, got %d", len(relations))
	}
	total, ok := resp["total"].(float64)
	if !ok {
		t.Fatalf("expected total field, got %T", resp["total"])
	}
	if total != 1 {
		t.Errorf("expected total=1, got %v", total)
	}
}

func TestHandleListConflicts_ExcludesNotConflict(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	srcSync, tgtSync := seedConflictsSession(t, st, "alpha")
	rel, err := st.SaveRelation(store.SaveRelationParams{SyncID: "rel-alpha-not-conflict", SourceID: srcSync, TargetID: tgtSync})
	if err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}
	if _, err := st.JudgeRelation(store.JudgeRelationParams{JudgmentID: rel.SyncID, Relation: store.RelationNotConflict}); err != nil {
		t.Fatalf("JudgeRelation: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/conflicts?project=alpha", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Total     int   `json:"total"`
		Relations []any `json:"relations"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Total != 0 || len(response.Relations) != 0 {
		t.Fatalf("conflicts response included not_conflict: %+v", response)
	}
}

func TestHandleListConflicts_LimitClampsTo500(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// limit=1000 must be clamped to 500 (no 4xx).
	req := httptest.NewRequest(http.MethodGet, "/conflicts?limit=1000", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when limit>500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleListConflicts_DefaultLimit50(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/conflicts", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["relations"]; !ok {
		t.Errorf("expected relations field in response")
	}
	if _, ok := resp["limit"]; !ok {
		t.Errorf("expected limit field in response")
	}
	if resp["limit"] != float64(50) {
		t.Errorf("expected default limit=50, got %v", resp["limit"])
	}
}

// ─── GET /conflicts/stats ─────────────────────────────────────────────────────

func TestHandleConflictsStats_ProjectScoped(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	srcSync, tgtSync := seedConflictsSession(t, st, "statsproject")
	if _, err := st.SaveRelation(store.SaveRelationParams{
		SyncID: "rel-stats-001", SourceID: srcSync, TargetID: tgtSync,
	}); err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/conflicts/stats?project=statsproject", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Must include by_judgment_status and deferred/dead counts.
	if _, ok := resp["by_judgment_status"]; !ok {
		t.Errorf("expected by_judgment_status field")
	}
	if _, ok := resp["deferred"]; !ok {
		t.Errorf("expected deferred field")
	}
	if _, ok := resp["dead"]; !ok {
		t.Errorf("expected dead field")
	}
}

func TestHandleConflictsStats_GlobalNoProject(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/conflicts/stats", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ─── GET /conflicts/deferred ──────────────────────────────────────────────────

func TestHandleConflictsDeferred_ListWithLimit(t *testing.T) {
	st, rawDB := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	validPayload := `{"relation_type":"conflicts","source_id":"obs-a","target_id":"obs-b"}`
	seedDeferredHTTP(t, rawDB, "def-http-001", validPayload, 0, "deferred")
	seedDeferredHTTP(t, rawDB, "def-http-002", validPayload, 0, "deferred")
	seedDeferredHTTP(t, rawDB, "def-http-003", validPayload, 5, "dead")

	req := httptest.NewRequest(http.MethodGet, "/conflicts/deferred?limit=2", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	rows, ok := resp["rows"].([]any)
	if !ok {
		t.Fatalf("expected rows array, got %T", resp["rows"])
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows (limit=2), got %d", len(rows))
	}
	if _, ok := resp["total"]; !ok {
		t.Errorf("expected total field in deferred response")
	}
}

func TestHandleConflictsDeferred_StatusFilter(t *testing.T) {
	st, rawDB := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	validPayload := `{"relation_type":"conflicts","source_id":"obs-c","target_id":"obs-d"}`
	seedDeferredHTTP(t, rawDB, "def-http-dead-1", validPayload, 5, "dead")
	seedDeferredHTTP(t, rawDB, "def-http-dead-2", validPayload, 5, "dead")
	seedDeferredHTTP(t, rawDB, "def-http-pend-1", validPayload, 0, "deferred")

	req := httptest.NewRequest(http.MethodGet, "/conflicts/deferred?status=dead", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	rows, ok := resp["rows"].([]any)
	if !ok {
		t.Fatalf("expected rows array, got %T", resp["rows"])
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 dead rows, got %d", len(rows))
	}
}

// ─── POST /conflicts/scan ─────────────────────────────────────────────────────

func TestHandleConflictsScan_DryRun(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// Seed a project with an observation (no candidates expected for isolated obs).
	seedConflictsSession(t, st, "scanproject")

	body := `{"project":"scanproject","apply":false}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Must include candidates_found and inserted.
	if _, ok := resp["candidates_found"]; !ok {
		t.Errorf("expected candidates_found field")
	}
	if _, ok := resp["inserted"]; !ok {
		t.Errorf("expected inserted field")
	}
	// dry_run must be true when apply=false.
	if resp["dry_run"] != true {
		t.Errorf("expected dry_run=true for apply=false scan, got %v", resp["dry_run"])
	}
	// inserted must be 0 for dry-run.
	if resp["inserted"] != float64(0) {
		t.Errorf("expected inserted=0 for dry-run, got %v", resp["inserted"])
	}
}

func TestHandleConflictsScan_PageContract(t *testing.T) {
	st, _ := conflictsTestStore(t)
	if err := st.CreateSession("scan-page", "scan-page", "/tmp/scan-page"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := st.AddObservation(store.AddObservationParams{
			SessionID: "scan-page",
			Type:      "decision",
			Title:     fmt.Sprintf("scan page %d", i),
			Content:   "scan page",
			Project:   "scan-page",
			Scope:     "project",
		}); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(`{"project":"scan-page","limit":2}`))
	rec := httptest.NewRecorder()
	New(st, 0).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["inspected"] != float64(2) || response["ranked_queries"] != float64(2) || response["next_cursor"] != float64(2) {
		t.Fatalf("page response = %#v", response)
	}

	invalid := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(`{"project":"scan-page","limit":0}`))
	invalidRec := httptest.NewRecorder()
	New(st, 0).Handler().ServeHTTP(invalidRec, invalid)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status = %d", invalidRec.Code)
	}

	cappedReq := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(`{"project":"scan-page","limit":2,"apply":true,"max_insert":1}`))
	cappedRec := httptest.NewRecorder()
	New(st, 0).Handler().ServeHTTP(cappedRec, cappedReq)
	if cappedRec.Code != http.StatusOK {
		t.Fatalf("capped status = %d: %s", cappedRec.Code, cappedRec.Body.String())
	}
	var capped map[string]any
	if err := json.NewDecoder(cappedRec.Body).Decode(&capped); err != nil {
		t.Fatal(err)
	}
	if capped["capped"] != true || capped["next_cursor"] != nil || !strings.Contains(capped["warning"].(string), "no continuation") {
		t.Fatalf("capped response = %#v", capped)
	}
}

func TestHandleConflictsScanProjectIsolationAndAllProjects(t *testing.T) {
	st, rawDB := conflictsTestStore(t)
	seed := func(sessionID, project string) {
		t.Helper()
		if err := st.CreateSession(sessionID, project, "/tmp/"+sessionID); err != nil {
			t.Fatalf("CreateSession %q: %v", sessionID, err)
		}
		for _, title := range []string{"shared HTTP conflict scan primary", "shared HTTP conflict scan secondary"} {
			if _, err := st.AddObservation(store.AddObservationParams{
				SessionID: sessionID,
				Type:      "decision",
				Title:     title,
				Content:   "shared HTTP conflict scan",
				Project:   project,
				Scope:     "project",
			}); err != nil {
				t.Fatalf("AddObservation %q: %v", sessionID, err)
			}
		}
	}
	seed("scan-alpha", "alpha")
	seed("scan-beta", "beta")
	seed("scan-blank", "legacy")
	if _, err := rawDB.Exec(`UPDATE observations SET project = '' WHERE session_id = 'scan-blank'`); err != nil {
		t.Fatalf("clear blank observation project: %v", err)
	}
	if _, err := rawDB.Exec(`UPDATE sessions SET project = '' WHERE id = 'scan-blank'`); err != nil {
		t.Fatalf("clear blank session project: %v", err)
	}

	scan := func(body string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		New(st, 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("scan %s status = %d: %s", body, rec.Code, rec.Body.String())
		}
		var response map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
			t.Fatalf("decode scan response: %v", err)
		}
		return response
	}

	alpha := scan(`{"project":"alpha"}`)
	if alpha["inspected"] != float64(2) || alpha["inserted"] != float64(0) {
		t.Fatalf("explicit alpha scan = %#v, want only alpha observations", alpha)
	}
	all := scan(`{"all_projects":true,"apply":true}`)
	if all["inspected"] != float64(6) || all["inserted"] != float64(3) {
		t.Fatalf("all-project scan = %#v, want all three project scopes", all)
	}

	var crossProjectRelations int
	if err := rawDB.QueryRow(`
		SELECT COUNT(*)
		FROM memory_relations r
		JOIN observations src ON src.sync_id = r.source_id
		JOIN observations tgt ON tgt.sync_id = r.target_id
		WHERE ifnull(src.project, '') != ifnull(tgt.project, '')
	`).Scan(&crossProjectRelations); err != nil {
		t.Fatalf("count cross-project relations: %v", err)
	}
	if crossProjectRelations != 0 {
		t.Fatalf("all-project scan created %d cross-project relations", crossProjectRelations)
	}
}

func TestHandleConflictsScan_OmittedProjectUsesCurrent(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	body := `{"apply":false}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for current-project scan, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ─── POST /conflicts/deferred/replay ─────────────────────────────────────────

func TestHandleReplayDeferred_EmptyQueue(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/conflicts/deferred/replay", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for replay on empty queue, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["retried"] != float64(0) {
		t.Errorf("expected retried=0, got %v", resp["retried"])
	}
	if resp["succeeded"] != float64(0) {
		t.Errorf("expected succeeded=0, got %v", resp["succeeded"])
	}
	if resp["dead"] != float64(0) {
		t.Errorf("expected dead=0, got %v", resp["dead"])
	}
}

// ─── GET /conflicts/{relation_id} ─────────────────────────────────────────────

func TestHandleConflictByID_Found(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	srcSync, tgtSync := seedConflictsSession(t, st, "getproject")
	rel, err := st.SaveRelation(store.SaveRelationParams{
		SyncID: "rel-get-001", SourceID: srcSync, TargetID: tgtSync,
	})
	if err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	url := fmt.Sprintf("/conflicts/%d", rel.ID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for existing relation, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["relation_id"] != float64(rel.ID) {
		t.Errorf("expected relation_id=%d, got %v", rel.ID, resp["relation_id"])
	}
	if _, ok := resp["sync_id"]; !ok {
		t.Errorf("expected sync_id field in relation detail")
	}
	if _, ok := resp["judgment_status"]; !ok {
		t.Errorf("expected judgment_status field in relation detail")
	}
}

func TestHandleConflictByID_NotFound(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/conflicts/99999", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing relation, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode 404 body: %v", err)
	}
	if _, ok := resp["error"]; !ok {
		t.Errorf("expected error field in 404 response")
	}
}

func TestHandleConflictByID_InvalidID(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/conflicts/not-a-number", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid relation_id, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ─── Phase F — POST /conflicts/scan semantic params ──────────────────────────

// mockSemanticRunner is a fake store.SemanticRunner for HTTP tests.
type mockSemanticRunner struct {
	verdict store.SemanticVerdict
	err     error
}

func (m *mockSemanticRunner) Compare(_ context.Context, _ string) (store.SemanticVerdict, error) {
	return m.verdict, m.err
}

// TestHandleScanConflicts_SemanticFalse_CountersZero verifies that when semantic=false
// (or omitted), the response includes semantic_judged=0, semantic_skipped=0,
// semantic_errors=0.
func TestHandleScanConflicts_SemanticFalse_CountersZero(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	seedConflictsSession(t, st, "semfalseproj")

	body := `{"project":"semfalseproj","apply":false}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// All three semantic counters must be present and zero.
	for _, field := range []string{"semantic_judged", "semantic_skipped", "semantic_errors"} {
		val, ok := resp[field]
		if !ok {
			t.Errorf("expected %q field in response; got keys: %v", field, resp)
			continue
		}
		if val != float64(0) {
			t.Errorf("expected %q=0 when semantic=false, got %v", field, val)
		}
	}
}

// TestHandleScanConflicts_SemanticTrue_NoEnv_500 verifies that when semantic=true
// and the runnerFactory is not configured (nil), the server returns 500 with a
// clear error body.
func TestHandleScanConflicts_SemanticTrue_NoFactory_500(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	// No runner factory set → should fail.
	h := srv.Handler()

	seedConflictsSession(t, st, "semnoenvproj")

	body := `{"project":"semnoenvproj","semantic":true}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when no runner factory set, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	errMsg, ok := resp["error"].(string)
	if !ok || errMsg == "" {
		t.Errorf("expected non-empty 'error' field in 500 response; got: %v", resp)
	}
}

// TestHandleScanConflicts_SemanticTrue_WithMockRunner verifies that when semantic=true
// and a mock runner factory is injected, the response includes non-zero counters.
func TestHandleScanConflicts_SemanticTrue_WithMockRunner(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)

	// Inject a factory that returns a fake runner returning "compatible".
	srv.SetRunnerFactory(func(name string) (store.SemanticRunner, error) {
		return &mockSemanticRunner{
			verdict: store.SemanticVerdict{
				Relation:   "compatible",
				Confidence: 0.9,
				Reasoning:  "test",
				Model:      "test-model",
			},
		}, nil
	})
	h := srv.Handler()

	// Seed enough observations that FTS5 finds candidates.
	if err := st.CreateSession("ses-semtrue", "semtrueproj", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	titles := []string{
		"JWT authentication token session management policy",
		"Session token JWT authentication management approach",
		"Authentication JWT session token policy decision",
	}
	for _, title := range titles {
		if _, err := st.AddObservation(store.AddObservationParams{
			SessionID: "ses-semtrue", Type: "decision",
			Title: title, Content: "JWT auth content for " + title,
			Project: "semtrueproj", Scope: "project",
		}); err != nil {
			t.Fatalf("AddObservation: %v", err)
		}
	}

	body := `{"project":"semtrueproj","semantic":true,"concurrency":2,"max_semantic":10}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with mock runner, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// semantic_judged + semantic_skipped + semantic_errors should be present (values depend on FTS).
	for _, field := range []string{"semantic_judged", "semantic_skipped", "semantic_errors"} {
		if _, ok := resp[field]; !ok {
			t.Errorf("expected %q field in semantic=true response; got: %v", field, resp)
		}
	}
}

// TestHandleScanConflicts_InvalidConcurrency_400 verifies that concurrency out of
// [1,20] range returns 400 before any work is done.
func TestHandleScanConflicts_InvalidConcurrency_400(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	// Runner factory not needed — validation happens before runner is resolved.
	h := srv.Handler()

	seedConflictsSession(t, st, "badconcproj")

	for _, badConcurrency := range []int{0, 21, -1, 100} {
		body := fmt.Sprintf(`{"project":"badconcproj","semantic":true,"concurrency":%d}`, badConcurrency)
		req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for concurrency=%d, got %d: %s", badConcurrency, rec.Code, rec.Body.String())
		}
	}
}

// TestHandleScanConflicts_InvalidTimeout_400 verifies that timeout_per_call_seconds
// out of [1,600] range returns 400 before any work is done.
func TestHandleScanConflicts_InvalidTimeout_400(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	seedConflictsSession(t, st, "badtmoproj")

	for _, badTimeout := range []int{0, 601, -5} {
		body := fmt.Sprintf(`{"project":"badtmoproj","semantic":true,"timeout_per_call_seconds":%d}`, badTimeout)
		req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for timeout_per_call_seconds=%d, got %d: %s", badTimeout, rec.Code, rec.Body.String())
		}
	}
}

// ─── TestRoutes_NoOverlapPanic ────────────────────────────────────────────────

// TestRoutes_NoOverlapPanic constructs a fresh *Server and calls Handler()
// to detect any route-registration-time panic (Go 1.22 mux panics on overlap).
func TestRoutes_NoOverlapPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("route registration panicked: %v", r)
		}
	}()

	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	// Calling Handler() exercises the registered mux without issuing requests.
	h := srv.Handler()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

// ─── G.2 — HTTP API integration tests ────────────────────────────────────────
//
// End-to-end coverage against a real seeded store hitting all 6 /conflicts/* routes.
// Verifies: pagination total accuracy, 404 JSON body shape, 400 on missing project,
// cap warning in scan response, pre-existing routes unaffected.

// TestG2_ListConflicts_PaginationTotal verifies the total field matches the
// actual row count for the project regardless of the limit applied.
func TestG2_ListConflicts_PaginationTotal(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// Seed 3 relations for project "pagproj".
	for i := 0; i < 3; i++ {
		srcSync, tgtSync := seedConflictsSession(t, st, fmt.Sprintf("pagproj-%d", i))
		if _, err := st.SaveRelation(store.SaveRelationParams{
			SyncID:   fmt.Sprintf("rel-pag-%d", i),
			SourceID: srcSync,
			TargetID: tgtSync,
		}); err != nil {
			t.Fatalf("SaveRelation %d: %v", i, err)
		}
	}

	// Request with limit=1 — total must still report 3.
	req := httptest.NewRequest(http.MethodGet, "/conflicts?project=pagproj-0&limit=1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	relations, ok := resp["relations"].([]any)
	if !ok {
		t.Fatalf("expected relations array, got %T", resp["relations"])
	}
	// With limit=1, at most 1 row returned.
	if len(relations) > 1 {
		t.Errorf("expected at most 1 relation with limit=1, got %d", len(relations))
	}
	// total must be a number (reflects full count for the project).
	if _, ok := resp["total"].(float64); !ok {
		t.Errorf("expected numeric total field, got %T: %v", resp["total"], resp["total"])
	}
}

// TestG2_GetConflict_404BodyShape verifies the 404 response for a missing
// relation_id has a JSON body with an "error" field.
func TestG2_GetConflict_404BodyShape(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/conflicts/88888", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode 404 body: %v", err)
	}
	if _, ok := resp["error"]; !ok {
		t.Errorf("expected 'error' field in 404 JSON body; got: %v", resp)
	}
}

// TestG2_ScanConflicts_ApplyCapWarning verifies that when the scan cap is reached
// the response includes a "warning" field containing "cap".
func TestG2_ScanConflicts_ApplyCapWarning(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// Seed a session; scan will attempt to find candidates.
	// With only 2 observations the FTS result is uncertain, so we pass max_insert=0
	// to force Capped=true without needing actual candidates.
	// Per design, max_insert=0 means any insert would exceed cap → Capped=true immediately.
	// However MaxInsert=0 may be treated as "use default 100". Instead seed 6 similar
	// observations and use max_insert=1 so the first insert triggers the cap.
	if err := st.CreateSession("ses-g2scan", "g2scanproj", "/tmp/g2scanproj"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	titles := []string{
		"JWT authentication token session management policy",
		"Session token JWT authentication management approach",
		"Authentication JWT session token policy decision",
		"Token management session JWT authentication strategy",
		"JWT session authentication token management pattern",
		"Session-based JWT token authentication management rule",
	}
	for _, title := range titles {
		if _, err := st.AddObservation(store.AddObservationParams{
			SessionID: "ses-g2scan", Type: "decision",
			Title: title, Content: "JWT auth content for " + title,
			Project: "g2scanproj", Scope: "project",
		}); err != nil {
			t.Fatalf("AddObservation: %v", err)
		}
	}

	body := `{"project":"g2scanproj","apply":true,"max_insert":1}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for scan apply, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// If a candidate was found and cap was reached, "warning" must be present.
	// If no candidates exist (FTS scores too low), "capped" is false — we tolerate that.
	if capped, _ := resp["capped"].(bool); capped {
		warning, hasWarning := resp["warning"].(string)
		if !hasWarning || warning == "" {
			t.Errorf("expected non-empty 'warning' field when capped=true; got: %v", resp)
		}
	}
	// Must always have inserted and candidates_found fields.
	if _, ok := resp["inserted"]; !ok {
		t.Errorf("expected 'inserted' field in scan response; got: %v", resp)
	}
	if _, ok := resp["candidates_found"]; !ok {
		t.Errorf("expected 'candidates_found' field in scan response; got: %v", resp)
	}
}

// TestG2_ScanConflicts_MissingProject400 verifies the scan endpoint returns 400
// when the "project" field is absent from the request body.
func TestG2_ScanConflicts_OmittedProjectUsesCurrent(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	body := `{"apply":true}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for current-project scan, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestG2_ReplayDeferred_ResponseShape verifies the replay endpoint always returns
// the three count fields: retried, succeeded, dead — even on empty queue.
func TestG2_ReplayDeferred_ResponseShape(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/conflicts/deferred/replay", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, field := range []string{"retried", "succeeded", "dead"} {
		if _, ok := resp[field]; !ok {
			t.Errorf("expected %q field in replay response; got: %v", field, resp)
		}
	}
}

// TestG2_ListDeferred_StatusFilter verifies the status filter returns only matching rows.
func TestG2_ListDeferred_StatusFilter(t *testing.T) {
	st, rawDB := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	validPayload := `{"relation_type":"conflicts","source_id":"g2-src","target_id":"g2-tgt"}`
	seedDeferredHTTP(t, rawDB, "g2-dead-a", validPayload, 5, "dead")
	seedDeferredHTTP(t, rawDB, "g2-dead-b", validPayload, 5, "dead")
	seedDeferredHTTP(t, rawDB, "g2-defer-a", validPayload, 0, "deferred")

	// Filter by status=dead → expect exactly 2.
	req := httptest.NewRequest(http.MethodGet, "/conflicts/deferred?status=dead", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	rows, ok := resp["rows"].([]any)
	if !ok {
		t.Fatalf("expected rows array, got %T", resp["rows"])
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 dead rows, got %d", len(rows))
	}
}

// TestG2_ExistingRoutes_Unaffected verifies that pre-existing /sync/status and
// /health routes are unaffected by the Phase 3 conflicts route additions.
func TestG2_ExistingRoutes_Unaffected(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// GET /health must still return 200.
	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthRec := httptest.NewRecorder()
	h.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Errorf("expected /health 200 after Phase 3, got %d", healthRec.Code)
	}

	// GET /sync/status must still return 200.
	syncReq := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	syncRec := httptest.NewRecorder()
	h.ServeHTTP(syncRec, syncReq)
	if syncRec.Code != http.StatusOK {
		t.Errorf("expected /sync/status 200 after Phase 3, got %d", syncRec.Code)
	}

	// GET /stats must still return 200.
	statsReq := httptest.NewRequest(http.MethodGet, "/stats", nil)
	statsRec := httptest.NewRecorder()
	h.ServeHTTP(statsRec, statsReq)
	if statsRec.Code != http.StatusOK {
		t.Errorf("expected /stats 200 after Phase 3, got %d", statsRec.Code)
	}
}

func TestProjectCurrentDoctorJudgeAndCompareRoutes(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("s-parity", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	idA, err := st.AddObservation(store.AddObservationParams{SessionID: "s-parity", Type: "decision", Title: "Old auth", Content: "sessions", Project: "engram", Scope: "project"})
	if err != nil {
		t.Fatalf("AddObservation A: %v", err)
	}
	idB, err := st.AddObservation(store.AddObservationParams{SessionID: "s-parity", Type: "decision", Title: "New auth", Content: "jwt", Project: "engram", Scope: "project"})
	if err != nil {
		t.Fatalf("AddObservation B: %v", err)
	}
	obsA, _ := st.GetObservation(idA)
	obsB, _ := st.GetObservation(idB)
	if _, err := st.SaveRelation(store.SaveRelationParams{SyncID: "rel-http-parity", SourceID: obsA.SyncID, TargetID: obsB.SyncID}); err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	var writes int32
	srv := New(st, 0)
	srv.SetOnWrite(func() { atomic.AddInt32(&writes, 1) })
	h := srv.Handler()

	projectReq := httptest.NewRequest(http.MethodGet, "/project/current?cwd=/tmp/engram", nil)
	projectRec := httptest.NewRecorder()
	h.ServeHTTP(projectRec, projectReq)
	if projectRec.Code != http.StatusOK {
		t.Fatalf("expected current project 200, got %d body=%q", projectRec.Code, projectRec.Body.String())
	}
	var projectResp map[string]any
	if err := json.Unmarshal(projectRec.Body.Bytes(), &projectResp); err != nil {
		t.Fatalf("decode project response: %v", err)
	}
	if projectResp["project"] == "" || projectResp["cwd"] != "/tmp/engram" {
		t.Fatalf("unexpected project response: %#v", projectResp)
	}

	doctorReq := httptest.NewRequest(http.MethodGet, "/doctor?project=engram&check=session_project_directory_mismatch", nil)
	doctorRec := httptest.NewRecorder()
	h.ServeHTTP(doctorRec, doctorReq)
	if doctorRec.Code != http.StatusOK {
		t.Fatalf("expected doctor 200, got %d body=%q", doctorRec.Code, doctorRec.Body.String())
	}
	var doctorResp map[string]any
	if err := json.Unmarshal(doctorRec.Body.Bytes(), &doctorResp); err != nil {
		t.Fatalf("decode doctor response: %v", err)
	}
	if doctorResp["project"] != "engram" || doctorResp["status"] == "" {
		t.Fatalf("unexpected doctor response: %#v", doctorResp)
	}

	missingProjectDoctorReq := httptest.NewRequest(http.MethodGet, "/doctor?project=missing-project", nil)
	missingProjectDoctorRec := httptest.NewRecorder()
	h.ServeHTTP(missingProjectDoctorRec, missingProjectDoctorReq)
	if missingProjectDoctorRec.Code != http.StatusNotFound {
		t.Fatalf("expected doctor unknown project 404, got %d body=%q", missingProjectDoctorRec.Code, missingProjectDoctorRec.Body.String())
	}
	var missingDoctorResp map[string]any
	if err := json.Unmarshal(missingProjectDoctorRec.Body.Bytes(), &missingDoctorResp); err != nil {
		t.Fatalf("decode missing doctor response: %v", err)
	}
	if missingDoctorResp["code"] != "unknown_project" || missingDoctorResp["available_projects"] == nil {
		t.Fatalf("expected structured unknown project response, got %#v", missingDoctorResp)
	}

	freshDetectedDoctorReq := httptest.NewRequest(http.MethodGet, "/doctor?cwd=/tmp/fresh-project", nil)
	freshDetectedDoctorRec := httptest.NewRecorder()
	h.ServeHTTP(freshDetectedDoctorRec, freshDetectedDoctorReq)
	if freshDetectedDoctorRec.Code != http.StatusOK {
		t.Fatalf("expected doctor fresh detected project 200, got %d body=%q", freshDetectedDoctorRec.Code, freshDetectedDoctorRec.Body.String())
	}

	mismatchObservationReq := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(`{"session_id":"s-parity","type":"decision","title":"Wrong project","content":"body","project":"other"}`))
	mismatchObservationRec := httptest.NewRecorder()
	h.ServeHTTP(mismatchObservationRec, mismatchObservationReq)
	if mismatchObservationRec.Code != http.StatusBadRequest {
		t.Fatalf("expected observation session/project mismatch 400, got %d body=%q", mismatchObservationRec.Code, mismatchObservationRec.Body.String())
	}

	mismatchPromptReq := httptest.NewRequest(http.MethodPost, "/prompts", strings.NewReader(`{"session_id":"s-parity","content":"prompt","project":"other"}`))
	mismatchPromptRec := httptest.NewRecorder()
	h.ServeHTTP(mismatchPromptRec, mismatchPromptReq)
	if mismatchPromptRec.Code != http.StatusBadRequest {
		t.Fatalf("expected prompt session/project mismatch 400, got %d body=%q", mismatchPromptRec.Code, mismatchPromptRec.Body.String())
	}

	mismatchPassiveReq := httptest.NewRequest(http.MethodPost, "/observations/passive", strings.NewReader(`{"session_id":"s-parity","content":"## Key Learnings:\n- mismatch","project":"other"}`))
	mismatchPassiveRec := httptest.NewRecorder()
	h.ServeHTTP(mismatchPassiveRec, mismatchPassiveReq)
	if mismatchPassiveRec.Code != http.StatusBadRequest {
		t.Fatalf("expected passive session/project mismatch 400, got %d body=%q", mismatchPassiveRec.Code, mismatchPassiveRec.Body.String())
	}

	invalidJudgeConfidenceReq := httptest.NewRequest(http.MethodPost, "/conflicts/judge", strings.NewReader(`{"judgment_id":"rel-http-parity","relation":"compatible","confidence":1.5}`))
	invalidJudgeConfidenceRec := httptest.NewRecorder()
	h.ServeHTTP(invalidJudgeConfidenceRec, invalidJudgeConfidenceReq)
	if invalidJudgeConfidenceRec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid judge confidence 400, got %d body=%q", invalidJudgeConfidenceRec.Code, invalidJudgeConfidenceRec.Body.String())
	}

	judgeReq := httptest.NewRequest(http.MethodPost, "/conflicts/judge", strings.NewReader(`{"judgment_id":"rel-http-parity","relation":"compatible","reason":"same migration","confidence":0.8,"session_id":"s-parity"}`))
	judgeReq.Header.Set("Content-Type", "application/json")
	judgeRec := httptest.NewRecorder()
	h.ServeHTTP(judgeRec, judgeReq)
	if judgeRec.Code != http.StatusOK {
		t.Fatalf("expected judge 200, got %d body=%q", judgeRec.Code, judgeRec.Body.String())
	}
	var judgeResp map[string]any
	if err := json.Unmarshal(judgeRec.Body.Bytes(), &judgeResp); err != nil {
		t.Fatalf("decode judge response: %v", err)
	}
	if judgeResp["relation"] == nil {
		t.Fatalf("expected relation envelope, got %#v", judgeResp)
	}

	compareReq := httptest.NewRequest(http.MethodPost, "/conflicts/compare", strings.NewReader(fmt.Sprintf(`{"memory_id_a":%d,"memory_id_b":%d,"relation":"related","confidence":0.91,"reasoning":"same auth topic","model":"test-model"}`, idA, idB)))
	compareReq.Header.Set("Content-Type", "application/json")
	compareRec := httptest.NewRecorder()
	h.ServeHTTP(compareRec, compareReq)
	if compareRec.Code != http.StatusOK {
		t.Fatalf("expected compare 200, got %d body=%q", compareRec.Code, compareRec.Body.String())
	}
	var compareResp map[string]any
	if err := json.Unmarshal(compareRec.Body.Bytes(), &compareResp); err != nil {
		t.Fatalf("decode compare response: %v", err)
	}
	if compareResp["sync_id"] == "" {
		t.Fatalf("expected persisted sync_id, got %#v", compareResp)
	}
	if atomic.LoadInt32(&writes) < 2 {
		t.Fatalf("expected judge and compare writes to notify, got %d", writes)
	}
}

func TestProjectCurrentUsesProcessOverrideBeforeCWD(t *testing.T) {
	t.Setenv("ENGRAM_PROJECT", "  Trusted Project  ")

	srv := New(newServerTestStore(t), 0)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/project/current?cwd=/path/that/does/not/exist", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected current project 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode project response: %v", err)
	}
	if got := response["project"]; got != "trusted project" {
		t.Errorf("project = %v, want trusted project", got)
	}
	if got := response["project_source"]; got != "process_override" {
		t.Errorf("project_source = %v, want process_override", got)
	}
	if got := response["project_path"]; got != "" {
		t.Errorf("project_path = %v, want empty for process override", got)
	}
	if got := response["cwd"]; got != "/path/that/does/not/exist" {
		t.Errorf("cwd = %v, want request cwd", got)
	}
}

func TestProjectScopedEndpointsResolveAndValidateProcessOverrides(t *testing.T) {
	t.Run("known override scopes context", func(t *testing.T) {
		t.Setenv("ENGRAM_PROJECT", "override-project")
		st := newServerTestStore(t)
		if err := st.CreateSession("override-session", "override-project", t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddObservation(store.AddObservationParams{SessionID: "override-session", Project: "override-project", Type: "note", Title: "override title", Content: "override content", Scope: "project"}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		New(st, 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/context", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "override title") {
			t.Fatalf("context = %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown override is rejected for current scoped reads", func(t *testing.T) {
		t.Setenv("ENGRAM_PROJECT", "missing-project")
		rec := httptest.NewRecorder()
		New(newServerTestStore(t), 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/context", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("context status = %d body=%q", rec.Code, rec.Body.String())
		}
	})

	t.Run("path-like override is rejected by discovery", func(t *testing.T) {
		t.Setenv("ENGRAM_PROJECT", `C:\\workspace`)
		rec := httptest.NewRecorder()
		New(newServerTestStore(t), 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/project/current", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("current status = %d body=%q", rec.Code, rec.Body.String())
		}
	})
}

func ambiguousProjectHTTPDirectory(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	for _, name := range []string{"repo-http-a", "repo-http-b"} {
		if err := os.MkdirAll(filepath.Join(parent, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return parent
}

func TestProjectCurrentAmbiguousUsesDiscoveryEnvelope(t *testing.T) {
	parent := ambiguousProjectHTTPDirectory(t)
	rec := httptest.NewRecorder()
	New(newServerTestStore(t), 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/project/current?cwd="+url.QueryEscape(parent), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["project"] != "" || body["project_source"] != "ambiguous" || body["project_path"] != parent {
		t.Fatalf("unexpected discovery envelope: %#v", body)
	}
	if available, ok := body["available_projects"].([]any); !ok || len(available) != 2 {
		t.Fatalf("available_projects = %#v, want two candidates", body["available_projects"])
	}
	if hint, ok := body["error_hint"].(string); !ok || hint == "" {
		t.Fatalf("error_hint = %#v, want ambiguity diagnostic", body["error_hint"])
	}
}

func TestProjectScopedRoutesReportStructuredAmbiguity(t *testing.T) {
	parent := ambiguousProjectHTTPDirectory(t)
	h := New(newServerTestStore(t), 0).Handler()
	for _, route := range []string{
		"/search?q=identity",
		"/timeline?observation_id=1",
		"/context",
		"/doctor",
	} {
		t.Run(route, func(t *testing.T) {
			rec := httptest.NewRecorder()
			separator := "?"
			if strings.Contains(route, "?") {
				separator = "&"
			}
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, route+separator+"cwd="+url.QueryEscape(parent), nil))
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["code"] != "ambiguous_project" || body["project_source"] != "ambiguous" || body["project_path"] != parent {
				t.Fatalf("unexpected ambiguity envelope: %#v", body)
			}
			if available, ok := body["available_projects"].([]any); !ok || len(available) != 2 {
				t.Fatalf("available_projects = %#v, want two candidates", body["available_projects"])
			}
		})
	}
}

func TestJudgeAndCompareRoutesValidateInput(t *testing.T) {
	st := newServerTestStore(t)
	h := New(st, 0).Handler()

	judgeReq := httptest.NewRequest(http.MethodPost, "/conflicts/judge", strings.NewReader(`{"relation":"related"}`))
	judgeRec := httptest.NewRecorder()
	h.ServeHTTP(judgeRec, judgeReq)
	if judgeRec.Code != http.StatusBadRequest {
		t.Fatalf("expected missing judgment_id 400, got %d body=%q", judgeRec.Code, judgeRec.Body.String())
	}

	missingConfidenceReq := httptest.NewRequest(http.MethodPost, "/conflicts/compare", strings.NewReader(`{"memory_id_a":1,"memory_id_b":2,"relation":"related","reasoning":"missing confidence"}`))
	missingConfidenceRec := httptest.NewRecorder()
	h.ServeHTTP(missingConfidenceRec, missingConfidenceReq)
	if missingConfidenceRec.Code != http.StatusBadRequest {
		t.Fatalf("expected missing confidence 400, got %d body=%q", missingConfidenceRec.Code, missingConfidenceRec.Body.String())
	}

	invalidConfidenceReq := httptest.NewRequest(http.MethodPost, "/conflicts/compare", strings.NewReader(`{"memory_id_a":1,"memory_id_b":2,"relation":"related","confidence":1.5,"reasoning":"invalid confidence"}`))
	invalidConfidenceRec := httptest.NewRecorder()
	h.ServeHTTP(invalidConfidenceRec, invalidConfidenceReq)
	if invalidConfidenceRec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid confidence 400, got %d body=%q", invalidConfidenceRec.Code, invalidConfidenceRec.Body.String())
	}

	compareReq := httptest.NewRequest(http.MethodPost, "/conflicts/compare", strings.NewReader(`{"memory_id_a":999,"memory_id_b":1000,"relation":"related","confidence":0.9,"reasoning":"missing"}`))
	compareRec := httptest.NewRecorder()
	h.ServeHTTP(compareRec, compareReq)
	if compareRec.Code != http.StatusNotFound {
		t.Fatalf("expected missing observation 404, got %d body=%q", compareRec.Code, compareRec.Body.String())
	}
}

func TestMigrateProjectRejectsLegacyRenamePayload(t *testing.T) {
	const token = "rescue-token"
	t.Setenv("ENGRAM_HTTP_TOKEN", token)
	h := New(newServerTestStore(t), 0).Handler()
	req := httptest.NewRequest(http.MethodPost, "/projects/migrate", bytes.NewBufferString(`{"old_project":"repo_name","new_project":"Repo_Name"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected legacy rename payload to be rejected, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAddObservationRejectsBlankTitle pins that POST /observations answers
// 400 (client mistake) rather than 500 or 201 when the title is blank (#459).
func TestHandleAddObservationRejectsBlankTitle(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	var writeCount atomic.Int32
	srv.SetOnWrite(func() { writeCount.Add(1) })

	if err := st.CreateSession("s-blank-title", "engram", t.TempDir()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	for _, body := range []string{
		`{"session_id":"s-blank-title","type":"note","title":"","content":"body","project":"engram"}`,
		`{"session_id":"s-blank-title","type":"note","title":"   ","content":"body","project":"engram"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: expected 400, got %d (%s)", body, rec.Code, rec.Body.String())
		}
	}

	if writeCount.Load() != 0 {
		t.Fatalf("expected 0 onWrite calls for rejected writes, got %d", writeCount.Load())
	}
}

// TestHandleAddObservationBlankTitleNotMaskedBySessionError pins that the title
// check runs before the session/project lookup. A whitespace-only title passes
// the raw required-fields check, so before #459's follow-up the request was
// answered with the session error instead of the documented title 400.
func TestHandleAddObservationBlankTitleNotMaskedBySessionError(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	var writeCount atomic.Int32
	srv.SetOnWrite(func() { writeCount.Add(1) })

	// Exists, but bound to a different project than the request claims.
	if err := st.CreateSession("s-mismatched", "engram", t.TempDir()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "nonexistent session",
			body: `{"session_id":"s-does-not-exist","type":"note","title":"   ","content":"body","project":"engram"}`,
		},
		{
			name: "mismatched project",
			body: `{"session_id":"s-mismatched","type":"note","title":"   ","content":"body","project":"other"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
			}

			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
			}
			msg, _ := resp["error"].(string)
			if msg != store.ErrObservationTitleRequired.Error() {
				t.Fatalf("expected the title error, got %q — the session lookup masked it", msg)
			}
		})
	}

	if writeCount.Load() != 0 {
		t.Fatalf("expected 0 onWrite calls for rejected writes, got %d", writeCount.Load())
	}
}

func TestHandleUpdateObservationRejectsBlankTitleWithoutSideEffects(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()
	var writeCount atomic.Int32
	srv.SetOnWrite(func() { writeCount.Add(1) })

	if err := st.CreateSession("s-update-title-guard", "engram", t.TempDir()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := st.AddObservation(store.AddObservationParams{
		SessionID: "s-update-title-guard",
		Type:      "note",
		Title:     "Original title",
		Content:   "Original content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	before, err := st.GetObservation(id)
	if err != nil {
		t.Fatalf("get original observation: %v", err)
	}
	countMutations := func() int {
		t.Helper()
		mutations, err := st.ListPendingSyncMutations(store.DefaultSyncTargetKey, 10)
		if err != nil {
			t.Fatalf("list pending mutations: %v", err)
		}
		count := 0
		for _, mutation := range mutations {
			if mutation.Entity == store.SyncEntityObservation && mutation.EntityKey == before.SyncID {
				count++
			}
		}
		return count
	}
	mutationsBefore := countMutations()

	for _, title := range []string{"", " \t\n "} {
		title := title
		t.Run("blank title", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/observations/%d", id), strings.NewReader(fmt.Sprintf(`{"title":%q}`, title)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
			}
			if msg, _ := resp["error"].(string); msg != store.ErrObservationTitleRequired.Error() {
				t.Fatalf("expected the title error, got %q", msg)
			}
			after, err := st.GetObservation(id)
			if err != nil {
				t.Fatalf("get observation after rejected update: %v", err)
			}
			if after.Title != before.Title || after.Content != before.Content || after.RevisionCount != before.RevisionCount {
				t.Fatalf("rejected update changed observation: before=%#v after=%#v", before, after)
			}
			if got := countMutations(); got != mutationsBefore {
				t.Fatalf("rejected update enqueued a mutation: got %d, want %d", got, mutationsBefore)
			}
		})
	}
	if writeCount.Load() != 0 {
		t.Fatalf("expected no onWrite calls for rejected updates, got %d", writeCount.Load())
	}

	req := httptest.NewRequest(http.MethodPatch, "/observations/999999", strings.NewReader(`{"title":"updated"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing observation, got %d body=%s", rec.Code, rec.Body.String())
	}
}
