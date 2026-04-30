package cloudserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
)

// ─── Fakes for mutation tests ─────────────────────────────────────────────────

type fakeMutationStore struct {
	fakeStore
	mutations      []MutationEntry
	syncEnabledMap map[string]bool // project → sync enabled
	errInsert      error
	errList        error
	// Audit capture (for REQ-404, REQ-406, REQ-412 tests).
	auditCalls     []cloudstore.AuditEntry
	errAuditInsert error
}

// InsertAuditEntry records the call for test assertions.
func (s *fakeMutationStore) InsertAuditEntry(_ context.Context, entry cloudstore.AuditEntry) error {
	if s.errAuditInsert != nil {
		return s.errAuditInsert
	}
	s.auditCalls = append(s.auditCalls, entry)
	return nil
}

func newFakeMutationStore() *fakeMutationStore {
	return &fakeMutationStore{
		fakeStore:      fakeStore{chunks: make(map[string][]byte)},
		syncEnabledMap: make(map[string]bool),
	}
}

func (s *fakeMutationStore) IsProjectSyncEnabled(project string) (bool, error) {
	if enabled, ok := s.syncEnabledMap[project]; ok {
		return enabled, nil
	}
	return true, nil // default: enabled
}

func (s *fakeMutationStore) WriteChunk(ctx context.Context, project string, chunkID, createdBy, clientCreatedAt string, payload []byte) error {
	if _, exists := s.chunks[chunkID]; exists {
		return s.fakeStore.WriteChunk(ctx, project, chunkID, createdBy, clientCreatedAt, payload)
	}
	if err := s.fakeStore.WriteChunk(ctx, project, chunkID, createdBy, clientCreatedAt, payload); err != nil {
		return err
	}
	var chunk engramsync.ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return err
	}
	batch := make([]MutationEntry, 0, len(chunk.Sessions)+len(chunk.Observations)+len(chunk.Prompts))
	for _, session := range chunk.Sessions {
		body, _ := json.Marshal(session)
		batch = append(batch, MutationEntry{Project: project, Entity: store.SyncEntitySession, EntityKey: strings.TrimSpace(session.ID), Op: store.SyncOpUpsert, Payload: body})
	}
	for _, observation := range chunk.Observations {
		body, _ := json.Marshal(observation)
		batch = append(batch, MutationEntry{Project: project, Entity: store.SyncEntityObservation, EntityKey: strings.TrimSpace(observation.SyncID), Op: store.SyncOpUpsert, Payload: body})
	}
	for _, prompt := range chunk.Prompts {
		body, _ := json.Marshal(prompt)
		batch = append(batch, MutationEntry{Project: project, Entity: store.SyncEntityPrompt, EntityKey: strings.TrimSpace(prompt.SyncID), Op: store.SyncOpUpsert, Payload: body})
	}
	_, err := s.InsertMutationBatch(ctx, batch)
	return err
}

func TestChunkPushMaterializesMutationsForAutosyncPull(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})
	body := strings.NewReader(`{
		"project":"proj-a",
		"created_by":"tester",
		"data":{
			"sessions":[{"id":"s-1","directory":"/tmp/s-1","started_at":"2026-04-29T10:00:00Z"}],
			"observations":[{"sync_id":"obs-1","session_id":"s-1","type":"decision","title":"Decision","content":"Content","scope":"project","created_at":"2026-04-29T10:01:00Z","updated_at":"2026-04-29T10:01:00Z"}],
			"prompts":[{"sync_id":"prompt-1","session_id":"s-1","content":"Prompt","created_at":"2026-04-29T10:02:00Z"}]
		}
	}`)

	pushRec := httptest.NewRecorder()
	pushReq := httptest.NewRequest(http.MethodPost, "/sync/push", body)
	pushReq.Header.Set("Authorization", "Bearer secret")
	pushReq.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(pushRec, pushReq)
	if pushRec.Code != http.StatusOK {
		t.Fatalf("expected chunk push 200, got %d body=%q", pushRec.Code, pushRec.Body.String())
	}
	if len(ms.chunks) != 1 {
		t.Fatalf("expected one stored chunk, got %d", len(ms.chunks))
	}
	if len(ms.mutations) != 3 {
		t.Fatalf("expected 3 materialized mutations, got %d: %+v", len(ms.mutations), ms.mutations)
	}
	if ms.mutations[0].Entity != store.SyncEntitySession || ms.mutations[1].Entity != store.SyncEntityObservation || ms.mutations[2].Entity != store.SyncEntityPrompt {
		t.Fatalf("expected session/observation/prompt order, got %+v", ms.mutations)
	}
	if ms.mutations[1].Project != "proj-a" || ms.mutations[1].EntityKey != "obs-1" || ms.mutations[1].Op != store.SyncOpUpsert {
		t.Fatalf("unexpected materialized observation mutation: %+v", ms.mutations[1])
	}

	pullRec := httptest.NewRecorder()
	pullReq := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	pullReq.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(pullRec, pullReq)
	if pullRec.Code != http.StatusOK {
		t.Fatalf("expected mutation pull 200, got %d body=%q", pullRec.Code, pullRec.Body.String())
	}
	var pulled struct {
		Mutations []StoredMutation `json:"mutations"`
	}
	if err := json.NewDecoder(pullRec.Body).Decode(&pulled); err != nil {
		t.Fatalf("decode pull response: %v", err)
	}
	if len(pulled.Mutations) != 3 || pulled.Mutations[1].Entity != store.SyncEntityObservation || pulled.Mutations[1].EntityKey != "obs-1" {
		t.Fatalf("expected pulled observation mutation after chunk push, got %+v", pulled.Mutations)
	}
}

func TestChunkPushReplayDoesNotDuplicateMaterializedMutations(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})
	body := `{"project":"proj-a","created_by":"tester","data":{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}],"observations":[{"sync_id":"obs-1","session_id":"s-1","type":"decision","title":"Decision","content":"Content","scope":"project"}]}}`

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/sync/push", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Content-Type", "application/json")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("push %d expected 200, got %d body=%q", i+1, rec.Code, rec.Body.String())
		}
	}
	if len(ms.chunks) != 1 {
		t.Fatalf("expected one stored chunk after replay, got %d", len(ms.chunks))
	}
	if len(ms.mutations) != 2 {
		t.Fatalf("expected replay to keep 2 materialized mutations, got %d: %+v", len(ms.mutations), ms.mutations)
	}
}

func TestMalformedChunkRejectsBeforeMaterialization(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/push", strings.NewReader(`{"project":"proj-a","created_by":"tester","data":{"observations":[{"sync_id":"obs-1","session_id":"missing","type":"decision","title":"Decision","content":"Content","scope":"project"}]}}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected malformed chunk 400, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.chunks) != 0 || len(ms.mutations) != 0 {
		t.Fatalf("expected no chunk or mutation after malformed push, chunks=%d mutations=%d", len(ms.chunks), len(ms.mutations))
	}
}

func (s *fakeMutationStore) InsertMutationBatch(ctx context.Context, batch []MutationEntry) ([]int64, error) {
	if s.errInsert != nil {
		return nil, s.errInsert
	}
	seqs := make([]int64, len(batch))
	for i := range batch {
		seq := int64(len(s.mutations) + i + 1)
		seqs[i] = seq
		s.mutations = append(s.mutations, batch[i])
	}
	return seqs, nil
}

func (s *fakeMutationStore) ListMutationsSince(ctx context.Context, sinceSeq int64, limit int, allowedProjects []string) ([]StoredMutation, bool, int64, error) {
	if s.errList != nil {
		return nil, false, 0, s.errList
	}
	allowed := make(map[string]struct{})
	for _, p := range allowedProjects {
		allowed[p] = struct{}{}
	}
	// allowedProjects == nil means no enrollment filter; non-nil (even empty) means filter by enrollment.
	useFilter := allowedProjects != nil
	var all []StoredMutation
	for i, m := range s.mutations {
		seq := int64(i + 1)
		if seq <= sinceSeq {
			continue
		}
		if useFilter {
			if _, ok := allowed[m.Project]; !ok {
				continue
			}
		}
		all = append(all, StoredMutation{
			Seq:        seq,
			Project:    m.Project,
			Entity:     m.Entity,
			EntityKey:  m.EntityKey,
			Op:         m.Op,
			Payload:    m.Payload,
			OccurredAt: time.Now().UTC().Format(time.RFC3339),
		})
	}

	hasMore := false
	latestSeq := int64(0)
	if len(all) > limit {
		all = all[:limit]
		hasMore = true
	}
	if len(all) > 0 {
		latestSeq = all[len(all)-1].Seq
	}
	return all, hasMore, latestSeq, nil
}

// multiProjectAuth authorizes specific projects per token.
type multiProjectAuth struct {
	token    string
	projects []string // projects this token is enrolled in
}

func (a multiProjectAuth) Authorize(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != a.token {
		return fmt.Errorf("unauthorized")
	}
	return nil
}

func (a multiProjectAuth) AuthorizeProject(project string) error {
	for _, p := range a.projects {
		if p == project {
			return nil
		}
	}
	return fmt.Errorf("project %q not enrolled", project)
}

func (a multiProjectAuth) EnrolledProjects() []string {
	return a.projects
}

// ─── Push endpoint tests ─────────────────────────────────────────────────────

func TestMutationPushEndpointAccepted(t *testing.T) {
	// REQ-200 happy path: 5 entries → HTTP 200, accepted_seqs has 5 items
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(5, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		AcceptedSeqs []int64 `json:"accepted_seqs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.AcceptedSeqs) != 5 {
		t.Fatalf("expected 5 accepted_seqs, got %d", len(resp.AcceptedSeqs))
	}
}

func TestMutationPushEndpointUnauth(t *testing.T) {
	// REQ-200 missing token → 401
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	// No Authorization header
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestMutationPushEndpointBatchTooLarge(t *testing.T) {
	// REQ-200: 101 entries → 400
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(101, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestMutationPushEndpointEmptyBatch(t *testing.T) {
	// JC1: empty batch → 400 empty_batch (changed from prior 200 behavior).
	// Empty batches carry no project info; they cannot be pause-gated or audited.
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(0, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty batch, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestMutationPushEmptyBatchRejectedWith400 verifies JC1: empty batch must return
// HTTP 400 with error_code=empty_batch. Empty batches carry no project info so they
// cannot be pause-gated; forcing 400 gives deterministic client feedback.
func TestMutationPushEmptyBatchRejectedWith400(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	body := marshalPushRequest(t, []MutationEntry{}) // empty slice

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty batch, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != "empty_batch" {
		t.Errorf("expected error_code=empty_batch, got %q", resp.ErrorCode)
	}
}

// ─── Pull endpoint tests ──────────────────────────────────────────────────────

func TestMutationPullEndpointSinceSeq(t *testing.T) {
	// REQ-201: since_seq=5, 10 stored mutations → returns 5 (seqs 6–10)
	ms := newFakeMutationStore()
	// Pre-load 10 mutations
	for i := 0; i < 10; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-a", Entity: "obs", EntityKey: fmt.Sprintf("k%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=5&limit=100", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		Mutations []json.RawMessage `json:"mutations"`
		HasMore   bool              `json:"has_more"`
		LatestSeq int64             `json:"latest_seq"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Mutations) != 5 {
		t.Fatalf("expected 5 mutations, got %d", len(resp.Mutations))
	}
	if resp.HasMore {
		t.Fatal("expected has_more=false")
	}
	if resp.LatestSeq != 10 {
		t.Fatalf("expected latest_seq=10, got %d", resp.LatestSeq)
	}
}

func TestMutationPullEndpointHasMore(t *testing.T) {
	// REQ-201: 150 mutations, limit=100 → has_more=true, 100 mutations returned
	ms := newFakeMutationStore()
	for i := 0; i < 150; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-a", Entity: "obs", EntityKey: fmt.Sprintf("k%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		Mutations []json.RawMessage `json:"mutations"`
		HasMore   bool              `json:"has_more"`
		LatestSeq int64             `json:"latest_seq"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Mutations) != 100 {
		t.Fatalf("expected 100 mutations, got %d", len(resp.Mutations))
	}
	if !resp.HasMore {
		t.Fatal("expected has_more=true")
	}
}

func TestMutationPullEndpointUnauth(t *testing.T) {
	// REQ-201 missing token → 401
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	// No Authorization header
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestMutationPullEndpointBeyondLatest(t *testing.T) {
	// REQ-201: since_seq beyond latest → empty
	ms := newFakeMutationStore()
	for i := 0; i < 5; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-a", Entity: "obs", EntityKey: fmt.Sprintf("k%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=100&limit=100", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		Mutations []json.RawMessage `json:"mutations"`
		HasMore   bool              `json:"has_more"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Mutations) != 0 {
		t.Fatalf("expected empty mutations, got %d", len(resp.Mutations))
	}
	if resp.HasMore {
		t.Fatal("expected has_more=false")
	}
}

// ─── Enrollment filter tests ──────────────────────────────────────────────────

func TestMutationPullEnrollmentFilter(t *testing.T) {
	// REQ-202: caller enrolled in "proj-a" only; both proj-a and proj-b exist
	ms := newFakeMutationStore()
	// Insert proj-a and proj-b mutations
	for i := 0; i < 3; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-a", Entity: "obs", EntityKey: fmt.Sprintf("ka%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-b", Entity: "obs", EntityKey: fmt.Sprintf("kb%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	// Caller only enrolled in proj-a
	srv := newMutationTestServer(ms, "token-a", []string{"proj-a"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	req.Header.Set("Authorization", "Bearer token-a")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Mutations []json.RawMessage `json:"mutations"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Mutations) != 3 {
		t.Fatalf("expected 3 mutations (proj-a only), got %d", len(resp.Mutations))
	}
}

func TestMutationPullCrossTenantLeak(t *testing.T) {
	// REQ-202: two callers, no cross-tenant leak
	ms := newFakeMutationStore()
	for i := 0; i < 3; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-a", Entity: "obs", EntityKey: fmt.Sprintf("ka%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-b", Entity: "obs", EntityKey: fmt.Sprintf("kb%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	srvA := newMutationTestServer(ms, "token-a", []string{"proj-a"})
	srvB := newMutationTestServer(ms, "token-b", []string{"proj-b"})

	// Caller A — should only see proj-a
	recA := httptest.NewRecorder()
	reqA := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	reqA.Header.Set("Authorization", "Bearer token-a")
	srvA.Handler().ServeHTTP(recA, reqA)

	// Caller B — should only see proj-b
	recB := httptest.NewRecorder()
	reqB := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	reqB.Header.Set("Authorization", "Bearer token-b")
	srvB.Handler().ServeHTTP(recB, reqB)

	if recA.Code != http.StatusOK || recB.Code != http.StatusOK {
		t.Fatalf("expected 200 for both, got A=%d B=%d", recA.Code, recB.Code)
	}

	var respA, respB struct {
		Mutations []struct {
			Project string `json:"project"`
		} `json:"mutations"`
	}
	_ = json.NewDecoder(recA.Body).Decode(&respA)
	_ = json.NewDecoder(recB.Body).Decode(&respB)

	for _, m := range respA.Mutations {
		if m.Project != "proj-a" {
			t.Fatalf("cross-tenant leak: caller-A received mutation for project %q", m.Project)
		}
	}
	for _, m := range respB.Mutations {
		if m.Project != "proj-b" {
			t.Fatalf("cross-tenant leak: caller-B received mutation for project %q", m.Project)
		}
	}
}

func TestMutationPullNoEnrollments(t *testing.T) {
	// REQ-202: no enrolled projects → empty 200
	ms := newFakeMutationStore()
	_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
		{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert", Payload: json.RawMessage(`{}`)},
	})

	srv := newMutationTestServer(ms, "secret", []string{}) // no enrollments

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Mutations []json.RawMessage `json:"mutations"`
		HasMore   bool              `json:"has_more"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Mutations) != 0 {
		t.Fatalf("expected empty mutations, got %d", len(resp.Mutations))
	}
	if resp.HasMore {
		t.Fatal("expected has_more=false")
	}
}

// ─── Sync-pause tests (REQ-203) ───────────────────────────────────────────────

func TestMutationPushSyncPaused409(t *testing.T) {
	// REQ-203: sync_enabled=false → 409
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false // paused

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sync-paused") {
		t.Fatalf("expected sync-paused error, got %q", rec.Body.String())
	}
}

func TestMutationPushNonPausedAccepted(t *testing.T) {
	// REQ-203: non-paused → 200
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = true

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestMutationPushPausePerProject(t *testing.T) {
	// REQ-203: alpha paused, beta active
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false
	ms.syncEnabledMap["proj-b"] = true

	srvA := newMutationTestServer(ms, "secret", []string{"proj-a"})
	srvB := newMutationTestServer(ms, "secret", []string{"proj-b"})

	// proj-a should be rejected
	recA := httptest.NewRecorder()
	reqA := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", marshalPushRequest(t, makeMutationEntries(1, "proj-a")))
	reqA.Header.Set("Authorization", "Bearer secret")
	reqA.Header.Set("Content-Type", "application/json")
	srvA.Handler().ServeHTTP(recA, reqA)

	if recA.Code != http.StatusConflict {
		t.Fatalf("proj-a: expected 409, got %d", recA.Code)
	}

	// proj-b should be accepted
	recB := httptest.NewRecorder()
	reqB := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", marshalPushRequest(t, makeMutationEntries(1, "proj-b")))
	reqB.Header.Set("Authorization", "Bearer secret")
	reqB.Header.Set("Content-Type", "application/json")
	srvB.Handler().ServeHTTP(recB, reqB)

	if recB.Code != http.StatusOK {
		t.Fatalf("proj-b: expected 200, got %d", recB.Code)
	}
}

func TestMutationPushPauseAdminStillBlocked(t *testing.T) {
	// REQ-203: admin token still gets 409 when project is paused
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false

	// Admin token is set but project is still paused (pause is a data policy)
	srv := New(ms, fakeAuth{}, 0, WithDashboardAdminToken("admin-token"), WithProjectAuthorizer(multiProjectAuth{
		token:    "admin-token",
		projects: []string{"proj-a"},
	}))

	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for admin with paused project, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── BC2: Cross-project authorization bypass tests ────────────────────────────

// TestMutationPushRejectsUnauthorizedProject verifies BC2:
// A bearer token authorized for "proj-a" MUST NOT push mutations for "proj-b".
func TestMutationPushRejectsUnauthorizedProject(t *testing.T) {
	ms := newFakeMutationStore()
	// Token authorized only for "proj-a"
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	// But we send entries for "proj-b" — must be rejected
	entries := makeMutationEntries(2, "proj-b")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unauthorized project, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestMutationPushRejectsMixedProjectBatch verifies BC2:
// A batch containing both authorized and unauthorized projects MUST be entirely rejected.
func TestMutationPushRejectsMixedProjectBatch(t *testing.T) {
	ms := newFakeMutationStore()
	// Token authorized only for "proj-a"
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	// Batch contains both "proj-a" (authorized) and "proj-b" (unauthorized)
	entries := []MutationEntry{
		{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert", Payload: json.RawMessage(`{}`)},
		{Project: "proj-b", Entity: "obs", EntityKey: "k2", Op: "upsert", Payload: json.RawMessage(`{}`)},
	}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for mixed batch with unauthorized project, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Verify nothing was stored for proj-a either (all-or-nothing rejection)
	if len(ms.mutations) != 0 {
		t.Fatalf("expected no mutations stored on mixed-batch rejection, got %d", len(ms.mutations))
	}
}

// ─── BW2: Fail-closed enrollment filter tests ──────────────────────────────

// TestMutationPullFailsClosedWithoutEnrolledProjectsProvider verifies BW2:
// When projectAuth is non-nil but does NOT implement EnrolledProjectsProvider,
// allowedProjects must default to [] (empty, not nil), returning no mutations
// instead of leaking all projects.
func TestMutationPullFailsClosedWithoutEnrolledProjectsProvider(t *testing.T) {
	ms := newFakeMutationStore()
	// Insert mutations for several projects
	for i := 0; i < 3; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-a", Entity: "obs", EntityKey: fmt.Sprintf("ka%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
			{Project: "proj-b", Entity: "obs", EntityKey: fmt.Sprintf("kb%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	// Use a ProjectAuthorizer that does NOT implement EnrolledProjectsProvider
	authWithoutEnrollment := &simpleProjectAuth{token: "secret"}
	srv := New(ms, authWithoutEnrollment, 0, WithProjectAuthorizer(authWithoutEnrollment))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		Mutations []json.RawMessage `json:"mutations"`
		HasMore   bool              `json:"has_more"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Must return empty — fail closed when enrollment provider is unavailable
	if len(resp.Mutations) != 0 {
		t.Fatalf("expected 0 mutations (fail-closed), got %d", len(resp.Mutations))
	}
}

// projectAuthWithEnrollment is an authorizer that ALSO implements
// EnrolledProjectsProvider. Mirrors the production auth.ProjectScopeAuthorizer
// and auth.Service shape.
type projectAuthWithEnrollment struct {
	token    string
	enrolled []string
}

func (p *projectAuthWithEnrollment) Authorize(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != p.token {
		return fmt.Errorf("unauthorized")
	}
	return nil
}

func (p *projectAuthWithEnrollment) AuthorizeProject(project string) error {
	for _, enrolled := range p.enrolled {
		if enrolled == project {
			return nil
		}
	}
	return fmt.Errorf("project %q not allowed", project)
}

func (p *projectAuthWithEnrollment) EnrolledProjects() []string {
	out := make([]string, len(p.enrolled))
	copy(out, p.enrolled)
	return out
}

// TestMutationPullUsesEnrollmentProviderWhenImplemented is the positive
// counterpart to TestMutationPullFailsClosedWithoutEnrolledProjectsProvider.
// When projectAuth implements EnrolledProjectsProvider (as *auth.Service and
// *auth.ProjectScopeAuthorizer both do), the pull MUST return mutations
// filtered to the enrolled set — not fail-close to empty.
//
// Regression guard for the bug where ProjectScopeAuthorizer did not implement
// EnrolledProjectsProvider, causing every mutation pull to return 0 even
// though pushes were accepted server-side.
func TestMutationPullUsesEnrollmentProviderWhenImplemented(t *testing.T) {
	ms := newFakeMutationStore()
	for i := 0; i < 3; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "engram", Entity: "obs", EntityKey: fmt.Sprintf("ka%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
			{Project: "other-tenant", Entity: "obs", EntityKey: fmt.Sprintf("kb%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	authz := &projectAuthWithEnrollment{token: "secret", enrolled: []string{"engram"}}
	srv := New(ms, authz, 0, WithProjectAuthorizer(authz))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		Mutations []struct {
			Project string `json:"project"`
		} `json:"mutations"`
		LatestSeq int64 `json:"latest_seq"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Mutations) != 3 {
		t.Fatalf("expected 3 mutations (engram only), got %d", len(resp.Mutations))
	}
	for _, m := range resp.Mutations {
		if m.Project != "engram" {
			t.Errorf("cross-tenant leak: got project %q, want only \"engram\"", m.Project)
		}
	}
	if resp.LatestSeq == 0 {
		t.Errorf("expected non-zero latest_seq, got 0")
	}
}

// ─── BW9: 409 pause gate uses writeActionableError ────────────────────────────

// TestMutationPushPauseGives409WithActionableError verifies BW9:
// The sync-paused 409 response MUST use the structured error envelope
// (error_class, error_code, error fields), not a plain JSON body.
func TestMutationPushPauseGives409WithActionableError(t *testing.T) {
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})
	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}

	var resp struct {
		ErrorClass string `json:"error_class"`
		ErrorCode  string `json:"error_code"`
		Error      string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ErrorClass == "" {
		t.Fatalf("expected error_class in 409 response, got empty; body=%q", rec.Body.String())
	}
	if resp.ErrorCode == "" {
		t.Fatalf("expected error_code in 409 response, got empty; body=%q", rec.Body.String())
	}
	if resp.Error == "" {
		t.Fatalf("expected error in 409 response, got empty; body=%q", rec.Body.String())
	}
}

// ─── BR2-1: Empty-project entry rejection ────────────────────────────────────

// TestMutationPushRejectsEmptyProjectEntries verifies BR2-1:
// Entries with an empty project field MUST be rejected with HTTP 400 before
// auth/pause checks — they must never be silently inserted into cloud_mutations.
func TestMutationPushRejectsEmptyProjectEntries(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := []MutationEntry{
		{Project: "", Entity: "obs", EntityKey: "k1", Op: "upsert", Payload: json.RawMessage(`{}`)},
	}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty-project entry, got %d body=%q", rec.Code, rec.Body.String())
	}
	// Ensure nothing was stored
	if len(ms.mutations) != 0 {
		t.Fatalf("expected no mutations stored for empty-project entry, got %d", len(ms.mutations))
	}
}

// TestMutationPushRejectsMixedEmptyProjectBatch verifies BR2-1 for batches:
// A batch with some empty and some valid projects must be rejected entirely.
func TestMutationPushRejectsMixedEmptyProjectBatch(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := []MutationEntry{
		{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert", Payload: json.RawMessage(`{}`)},
		{Project: "", Entity: "obs", EntityKey: "k2", Op: "upsert", Payload: json.RawMessage(`{}`)},
	}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for batch with empty-project entry, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.mutations) != 0 {
		t.Fatalf("expected no mutations stored when batch contains empty-project entry, got %d", len(ms.mutations))
	}
}

// simpleProjectAuth implements Authenticator + ProjectAuthorizer but NOT EnrolledProjectsProvider.
// Used to test BW2 fail-closed behavior.
type simpleProjectAuth struct {
	token string
}

func (a *simpleProjectAuth) Authorize(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != a.token {
		return fmt.Errorf("unauthorized")
	}
	return nil
}

func (a *simpleProjectAuth) AuthorizeProject(_ string) error {
	return nil // allow all projects for this auth
}

// ─── REQ-006, REQ-008: Relation payload validation tests (Phase D) ───────────

// TestHandleMutationPush_ValidRelation_Returns200 (D.1a) verifies REQ-006 happy
// path: a complete relation payload with all 6 required fields returns HTTP 200.
func TestHandleMutationPush_ValidRelation_Returns200(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	payload := json.RawMessage(`{
		"sync_id":         "rel-001",
		"source_id":       "obs-src-001",
		"target_id":       "obs-tgt-001",
		"judgment_status": "judged",
		"marked_by_actor": "alice",
		"marked_by_kind":  "human",
		"relation":        "conflicts_with",
		"project":         "proj-a"
	}`)
	entries := []MutationEntry{
		{Project: "proj-a", Entity: "relation", EntityKey: "rel-001", Op: "upsert", Payload: payload},
	}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("D.1a: expected 200 for valid relation, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.mutations) != 1 {
		t.Fatalf("D.1a: expected 1 mutation stored, got %d", len(ms.mutations))
	}
}

// TestHandleMutationPush_RelationMissingEachRequiredField (D.1b) verifies
// REQ-006 negative: each required field individually absent returns HTTP 400
// with the correct field name in the response body.
func TestHandleMutationPush_RelationMissingEachRequiredField(t *testing.T) {
	requiredFields := []struct {
		name    string
		payload json.RawMessage
	}{
		{
			name: "sync_id",
			payload: json.RawMessage(`{
				"source_id":       "obs-src",
				"target_id":       "obs-tgt",
				"judgment_status": "judged",
				"marked_by_actor": "alice",
				"marked_by_kind":  "human"
			}`),
		},
		{
			name: "source_id",
			payload: json.RawMessage(`{
				"sync_id":         "rel-001",
				"target_id":       "obs-tgt",
				"judgment_status": "judged",
				"marked_by_actor": "alice",
				"marked_by_kind":  "human"
			}`),
		},
		{
			name: "target_id",
			payload: json.RawMessage(`{
				"sync_id":         "rel-001",
				"source_id":       "obs-src",
				"judgment_status": "judged",
				"marked_by_actor": "alice",
				"marked_by_kind":  "human"
			}`),
		},
		{
			name: "judgment_status",
			payload: json.RawMessage(`{
				"sync_id":         "rel-001",
				"source_id":       "obs-src",
				"target_id":       "obs-tgt",
				"marked_by_actor": "alice",
				"marked_by_kind":  "human"
			}`),
		},
		{
			name: "marked_by_actor",
			payload: json.RawMessage(`{
				"sync_id":         "rel-001",
				"source_id":       "obs-src",
				"target_id":       "obs-tgt",
				"judgment_status": "judged",
				"marked_by_kind":  "human"
			}`),
		},
		{
			name: "marked_by_kind",
			payload: json.RawMessage(`{
				"sync_id":         "rel-001",
				"source_id":       "obs-src",
				"target_id":       "obs-tgt",
				"judgment_status": "judged",
				"marked_by_actor": "alice"
			}`),
		},
	}

	for _, tc := range requiredFields {
		t.Run("missing_"+tc.name, func(t *testing.T) {
			ms := newFakeMutationStore()
			srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

			entries := []MutationEntry{
				{Project: "proj-a", Entity: "relation", EntityKey: "rel-001", Op: "upsert", Payload: tc.payload},
			}
			body := marshalPushRequest(t, entries)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
			req.Header.Set("Authorization", "Bearer secret")
			req.Header.Set("Content-Type", "application/json")
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("D.1b missing %q: expected 400, got %d body=%q", tc.name, rec.Code, rec.Body.String())
			}
			// Verify no entry was stored (atomic batch)
			if len(ms.mutations) != 0 {
				t.Fatalf("D.1b missing %q: expected 0 mutations stored, got %d", tc.name, len(ms.mutations))
			}
			// Verify the missing field name appears in the response
			var resp struct {
				Error   string       `json:"error"`
				Invalid []struct {
					Field  string `json:"field"`
					Index  int    `json:"index"`
					Entity string `json:"entity"`
				} `json:"invalid"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("D.1b missing %q: decode 400 body: %v; body=%q", tc.name, err, rec.Body.String())
			}
			if len(resp.Invalid) == 0 {
				t.Fatalf("D.1b missing %q: expected invalid list in 400 body, got none; body=%q", tc.name, rec.Body.String())
			}
			if resp.Invalid[0].Field != tc.name {
				t.Errorf("D.1b missing %q: expected field=%q in invalid[0], got %q", tc.name, tc.name, resp.Invalid[0].Field)
			}
		})
	}
}

// TestHandleMutationPush_PartialBatch_Atomic (D.1c) verifies REQ-006 edge case:
// a 2-entry batch with one valid and one invalid relation → 400, neither stored.
func TestHandleMutationPush_PartialBatch_Atomic(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	validPayload := json.RawMessage(`{
		"sync_id":         "rel-001",
		"source_id":       "obs-src-001",
		"target_id":       "obs-tgt-001",
		"judgment_status": "judged",
		"marked_by_actor": "alice",
		"marked_by_kind":  "human"
	}`)
	// Invalid: missing target_id
	invalidPayload := json.RawMessage(`{
		"sync_id":         "rel-002",
		"source_id":       "obs-src-002",
		"judgment_status": "judged",
		"marked_by_actor": "bob",
		"marked_by_kind":  "human"
	}`)
	entries := []MutationEntry{
		{Project: "proj-a", Entity: "relation", EntityKey: "rel-001", Op: "upsert", Payload: validPayload},
		{Project: "proj-a", Entity: "relation", EntityKey: "rel-002", Op: "upsert", Payload: invalidPayload},
	}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("D.1c: expected 400 for partial-batch with invalid entry, got %d body=%q", rec.Code, rec.Body.String())
	}
	// Atomic rejection: neither entry stored
	if len(ms.mutations) != 0 {
		t.Fatalf("D.1c: expected 0 mutations stored (atomic batch rejection), got %d", len(ms.mutations))
	}
	// Response must include the offending index (1)
	var resp struct {
		Invalid []struct {
			Index int `json:"index"`
		} `json:"invalid"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("D.1c: decode 400 body: %v", err)
	}
	if len(resp.Invalid) == 0 {
		t.Fatalf("D.1c: expected invalid list in response, got none")
	}
	if resp.Invalid[0].Index != 1 {
		t.Errorf("D.1c: expected offending index=1, got %d", resp.Invalid[0].Index)
	}
}

// TestHandleMutationPush_LegacyObsMissingOptional_Returns200 (D.1d) verifies
// REQ-008: legacy observation entity with only sync_id in payload → HTTP 200.
// No new required fields for legacy entities — backwards compatibility preserved.
func TestHandleMutationPush_LegacyObsMissingOptional_Returns200(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	// Minimal observation payload: only sync_id, no optional fields
	minimalPayload := json.RawMessage(`{"sync_id":"obs-001","title":"My observation"}`)
	entries := []MutationEntry{
		{Project: "proj-a", Entity: "observation", EntityKey: "obs-001", Op: "upsert", Payload: minimalPayload},
	}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("D.1d: expected 200 for legacy obs with minimal payload, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.mutations) != 1 {
		t.Fatalf("D.1d: expected 1 mutation stored, got %d", len(ms.mutations))
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func newMutationTestServer(ms *fakeMutationStore, token string, projects []string) *CloudServer {
	auth := multiProjectAuth{token: token, projects: projects}
	return New(ms, auth, 0)
}

func makeMutationEntries(count int, project string) []MutationEntry {
	entries := make([]MutationEntry, count)
	for i := range entries {
		entries[i] = MutationEntry{
			Project:   project,
			Entity:    "observation",
			EntityKey: fmt.Sprintf("obs-%d", i),
			Op:        "upsert",
			Payload:   json.RawMessage(`{"title":"test"}`),
		}
	}
	return entries
}

func marshalPushRequest(t *testing.T, entries []MutationEntry) *bytes.Buffer {
	t.Helper()
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatalf("marshal push request: %v", err)
	}
	return bytes.NewBuffer(body)
}

func marshalPushRequestWithCreatedBy(t *testing.T, entries []MutationEntry, createdBy string) *bytes.Buffer {
	t.Helper()
	body, err := json.Marshal(map[string]any{"entries": entries, "created_by": createdBy})
	if err != nil {
		t.Fatalf("marshal push request with created_by: %v", err)
	}
	return bytes.NewBuffer(body)
}

// ─── REQ-404, REQ-406, REQ-412: Audit emission tests ─────────────────────────

// TestMutationPushPaused409EmitsAudit verifies that a paused-project 409 emits
// exactly one audit call with Action=mutation_push, Outcome=rejected_project_paused.
// REQ-404 scenario 1, 2.1.1.
func TestMutationPushPaused409EmitsAudit(t *testing.T) {
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false // paused

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})
	entries := makeMutationEntries(2, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.auditCalls) != 1 {
		t.Fatalf("expected 1 audit call on paused 409, got %d", len(ms.auditCalls))
	}
	audit := ms.auditCalls[0]
	if audit.Action != cloudstore.AuditActionMutationPush {
		t.Errorf("audit action: got %q, want %q", audit.Action, cloudstore.AuditActionMutationPush)
	}
	if audit.Outcome != cloudstore.AuditOutcomeRejectedProjectPaused {
		t.Errorf("audit outcome: got %q, want %q", audit.Outcome, cloudstore.AuditOutcomeRejectedProjectPaused)
	}
}

// TestMutationPushNonPaused200EmitsNoAudit verifies that a successful push
// emits zero audit calls. REQ-404 scenario 2, 2.1.2.
func TestMutationPushNonPaused200EmitsNoAudit(t *testing.T) {
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = true // enabled

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})
	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.auditCalls) != 0 {
		t.Errorf("expected 0 audit calls on non-paused 200, got %d", len(ms.auditCalls))
	}
}

// TestMutationPushPausedWithCreatedBy verifies that created_by field populates
// the audit contributor. REQ-406 scenario 1, 2.1.3.
func TestMutationPushPausedWithCreatedBy(t *testing.T) {
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})
	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequestWithCreatedBy(t, entries, "alice")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.auditCalls) != 1 {
		t.Fatalf("expected 1 audit call, got %d", len(ms.auditCalls))
	}
	if ms.auditCalls[0].Contributor != "alice" {
		t.Errorf("expected contributor=alice, got %q", ms.auditCalls[0].Contributor)
	}
}

// TestMutationPushPausedWithoutCreatedByDefaultsUnknown verifies that missing
// created_by defaults contributor to "unknown". REQ-406 scenario 2, 2.1.4.
func TestMutationPushPausedWithoutCreatedByDefaultsUnknown(t *testing.T) {
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})
	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries) // no created_by

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.auditCalls) != 1 {
		t.Fatalf("expected 1 audit call, got %d", len(ms.auditCalls))
	}
	if ms.auditCalls[0].Contributor != "unknown" {
		t.Errorf("expected contributor=unknown, got %q", ms.auditCalls[0].Contributor)
	}
}

// TestMutationPushAuditInsertFailureStill409 verifies that even when InsertAuditEntry
// returns an error, the handler still returns 409 (no 5xx). REQ-404 scenario 3, 2.1.5.
func TestMutationPushAuditInsertFailureStill409(t *testing.T) {
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false
	ms.errAuditInsert = fmt.Errorf("db connection lost")

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})
	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 even when audit insert fails, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── REQ-414: Response envelope project fields ────────────────────────────────

// mutationPushResponseEnvelope is a superset decode target used in REQ-414 tests.
type mutationPushResponseEnvelope struct {
	AcceptedSeqs  []int64 `json:"accepted_seqs"`
	Project       string  `json:"project"`
	ProjectSource string  `json:"project_source"`
	ProjectPath   string  `json:"project_path"`
}

// mutationPullResponseEnvelope is a superset decode target used in REQ-414 tests.
type mutationPullResponseEnvelope struct {
	Mutations     []json.RawMessage `json:"mutations"`
	HasMore       bool              `json:"has_more"`
	LatestSeq     int64             `json:"latest_seq"`
	Project       string            `json:"project"`
	ProjectSource string            `json:"project_source"`
	ProjectPath   string            `json:"project_path"`
}

// TestMutationPushResponseEnvelopeHasProjectFields verifies REQ-414 success path:
// a 200 OK response from handleMutationPush must include project, project_source,
// and project_path in the JSON body.
func TestMutationPushResponseEnvelopeHasProjectFields(t *testing.T) {
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = true // enabled — success path

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})
	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	bodyBytes := rec.Body.Bytes()
	var resp mutationPushResponseEnvelope
	if err := json.Unmarshal(bodyBytes, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Project != "proj-a" {
		t.Errorf("project: got %q, want %q", resp.Project, "proj-a")
	}
	if resp.ProjectSource != "request_body" {
		t.Errorf("project_source: got %q, want %q", resp.ProjectSource, "request_body")
	}
	// project_path must be present (empty string is acceptable for server-side)
	// JSON field must be emitted — check via raw decode
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(bodyBytes, &raw)
	if _, ok := raw["project_path"]; !ok {
		t.Error("project_path field must be present in response JSON")
	}
}

// TestMutationPushPausedResponseEnvelopeHasProjectFields verifies REQ-414 409 path:
// a 409 conflict response from handleMutationPush must include project, project_source,
// and project_path fields in the JSON body (in addition to error fields).
func TestMutationPushPausedResponseEnvelopeHasProjectFields(t *testing.T) {
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false // paused — 409 path

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})
	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}

	bodyBytes := rec.Body.Bytes()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		t.Fatalf("decode 409 body: %v; body=%s", err, bodyBytes)
	}
	for _, field := range []string{"project", "project_source", "project_path"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("409 response missing required field %q; body=%s", field, bodyBytes)
		}
	}
	var projectVal string
	if err := json.Unmarshal(raw["project"], &projectVal); err != nil || projectVal != "proj-a" {
		t.Errorf("project: got %q, want %q", projectVal, "proj-a")
	}
	var sourceVal string
	if err := json.Unmarshal(raw["project_source"], &sourceVal); err != nil || sourceVal != "request_body" {
		t.Errorf("project_source: got %q, want %q", sourceVal, "request_body")
	}
}

// TestMutationPullResponseEnvelopeHasProjectFields verifies REQ-414 for the pull path:
// the 200 pull response must include project, project_source, and project_path.
// For pull, the project reflects the primary enrolled project of the caller.
func TestMutationPullResponseEnvelopeHasProjectFields(t *testing.T) {
	ms := newFakeMutationStore()
	_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
		{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert", Payload: json.RawMessage(`{}`)},
	})

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=10", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	bodyBytes := rec.Body.Bytes()
	var resp mutationPullResponseEnvelope
	if err := json.Unmarshal(bodyBytes, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ProjectSource != "request_body" {
		t.Errorf("project_source: got %q, want %q", resp.ProjectSource, "request_body")
	}
	// project_path must be emitted (empty string acceptable)
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(bodyBytes, &raw)
	if _, ok := raw["project_path"]; !ok {
		t.Error("project_path field must be present in pull response JSON")
	}
}

// ─── Phase G — Integration tests (G.4, G.5): server validation + backwards compat ──

// TestRelationSync_ServerValidation_MissingField (G.4) verifies REQ-006:
// Push a relation payload missing `judgment_status` → 400 with `missing` listing
// the field. Push a valid relation immediately after → 200.
func TestRelationSync_ServerValidation_MissingField(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-g4"})

	// Push 1: missing judgment_status → must return 400.
	invalidPayload := json.RawMessage(`{
		"sync_id":         "rel-g4-001",
		"source_id":       "obs-src-g4",
		"target_id":       "obs-tgt-g4",
		"marked_by_actor": "agent:test",
		"marked_by_kind":  "agent"
	}`)
	entries := []MutationEntry{
		{Project: "proj-g4", Entity: "relation", EntityKey: "rel-g4-001", Op: "upsert", Payload: invalidPayload},
	}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("G.4: expected 400 for missing judgment_status, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Verify the missing field name appears in the response body.
	var errResp struct {
		Invalid []struct {
			Field string `json:"field"`
		} `json:"invalid"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("G.4: decode 400 body: %v", err)
	}
	if len(errResp.Invalid) == 0 {
		t.Fatal("G.4: expected non-empty 'invalid' list in 400 response")
	}
	if errResp.Invalid[0].Field != "judgment_status" {
		t.Errorf("G.4: expected field='judgment_status' in invalid[0], got %q", errResp.Invalid[0].Field)
	}

	// Verify nothing was stored.
	if len(ms.mutations) != 0 {
		t.Fatalf("G.4: expected 0 mutations stored after 400, got %d", len(ms.mutations))
	}

	// Push 2: valid relation payload immediately after → must return 200.
	validPayload := json.RawMessage(`{
		"sync_id":         "rel-g4-002",
		"source_id":       "obs-src-g4",
		"target_id":       "obs-tgt-g4",
		"judgment_status": "judged",
		"marked_by_actor": "agent:test",
		"marked_by_kind":  "agent",
		"relation":        "conflicts_with",
		"project":         "proj-g4"
	}`)
	entries2 := []MutationEntry{
		{Project: "proj-g4", Entity: "relation", EntityKey: "rel-g4-002", Op: "upsert", Payload: validPayload},
	}
	body2 := marshalPushRequest(t, entries2)

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body2)
	req2.Header.Set("Authorization", "Bearer secret")
	req2.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("G.4: expected 200 for valid relation after 400, got %d body=%q", rec2.Code, rec2.Body.String())
	}
	if len(ms.mutations) != 1 {
		t.Fatalf("G.4: expected 1 mutation stored after valid push, got %d", len(ms.mutations))
	}
}

// TestRelationSync_BackwardsCompat_LegacyClient (G.5) verifies REQ-008:
// An older client that pushes only session + observation mutations (no entity='relation')
// succeeds with HTTP 200. No behavior change for legacy entities.
func TestRelationSync_BackwardsCompat_LegacyClient(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-g5"})

	// Simulate a legacy client push: session + observation only, no relation.
	sessionPayload := json.RawMessage(`{"sync_id":"ses-g5-001","directory":"/tmp/g5"}`)
	obsPayload := json.RawMessage(`{"sync_id":"obs-g5-001","title":"Legacy observation"}`)

	entries := []MutationEntry{
		{Project: "proj-g5", Entity: "session", EntityKey: "ses-g5-001", Op: "upsert", Payload: sessionPayload},
		{Project: "proj-g5", Entity: "observation", EntityKey: "obs-g5-001", Op: "upsert", Payload: obsPayload},
	}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("G.5: expected 200 for legacy client push (session+obs only), got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.mutations) != 2 {
		t.Fatalf("G.5: expected 2 mutations stored (session+obs), got %d", len(ms.mutations))
	}

	// Triangulation: verify entity types were stored correctly (no mutation is 'relation').
	for i, m := range ms.mutations {
		if m.Entity == "relation" {
			t.Errorf("G.5: mutation[%d] has entity='relation' — legacy client must not produce relation mutations; got entity=%q", i, m.Entity)
		}
	}

	// Verify the two legacy entities are in the stored mutations.
	entities := make(map[string]bool)
	for _, m := range ms.mutations {
		entities[m.Entity] = true
	}
	if !entities["session"] {
		t.Error("G.5: expected 'session' entity in stored mutations")
	}
	if !entities["observation"] {
		t.Error("G.5: expected 'observation' entity in stored mutations")
	}
}
