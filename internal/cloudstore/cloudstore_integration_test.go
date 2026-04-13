//go:build integration

package cloudstore

import (
	"context"
	"os"
	"testing"
)

const defaultTestDSN = "postgres://engram:testpass@localhost:5433/engram_test?sslmode=disable"

func testDSN() string {
	if v := os.Getenv("ENGRAM_TEST_DSN"); v != "" {
		return v
	}
	return defaultTestDSN
}

func newTestCloudStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()

	s, err := New(ctx, testDSN())
	if err != nil {
		t.Skipf("PostgreSQL not available: %v", err)
	}

	if err := s.RunMigrations(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	// Clean all tables for test isolation
	tables := []string{
		"rate_limits", "idempotency_keys", "sync_cursors",
		"observation_revisions", "prompts", "sessions", "observations",
		"project_members", "projects", "users",
	}
	for _, table := range tables {
		if _, err := s.pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clean %s: %v", table, err)
		}
	}
	// Reset seq counter
	if _, err := s.pool.Exec(ctx, "UPDATE server_seq_counter SET value = 0"); err != nil {
		t.Fatalf("reset seq: %v", err)
	}

	t.Cleanup(func() { s.Close() })
	return s
}

// Task 1.5: Schema creation + all tables exist
func TestIntegration_SchemaCreation(t *testing.T) {
	s := newTestCloudStore(t)
	ctx := context.Background()

	tables := []string{
		"users", "projects", "project_members", "observations",
		"observation_revisions", "sessions", "prompts",
		"server_seq_counter", "sync_cursors", "idempotency_keys", "rate_limits",
	}
	for _, table := range tables {
		var exists bool
		err := s.pool.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = $1)", table,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s does not exist", table)
		}
	}

	// seq counter should have value=0
	var seqVal int64
	if err := s.pool.QueryRow(ctx, "SELECT value FROM server_seq_counter").Scan(&seqVal); err != nil {
		t.Fatalf("read seq: %v", err)
	}
	if seqVal != 0 {
		t.Errorf("expected seq=0, got %d", seqVal)
	}
}

// Task 1.6: Idempotent schema creation
func TestIntegration_SchemaIdempotent(t *testing.T) {
	s := newTestCloudStore(t)
	ctx := context.Background()

	// Run migrations again — should not error
	if err := s.RunMigrations(ctx); err != nil {
		t.Fatalf("second migration run failed: %v", err)
	}
}

// Task 2.6: User lifecycle — create, validate, rotate
func TestIntegration_UserLifecycle(t *testing.T) {
	s := newTestCloudStore(t)
	ctx := context.Background()

	// Create user
	userID, rawKey, err := s.CreateUser(ctx, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if userID == "" || rawKey == "" {
		t.Fatal("expected non-empty userID and rawKey")
	}

	// Validate key
	gotID, err := s.ValidateAPIKey(ctx, rawKey)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if gotID != userID {
		t.Fatalf("expected userID=%s, got %s", userID, gotID)
	}

	// Invalid key
	gotID, err = s.ValidateAPIKey(ctx, "engram_sk_invalid")
	if err != nil {
		t.Fatalf("validate invalid: %v", err)
	}
	if gotID != "" {
		t.Fatalf("expected empty for invalid key, got %s", gotID)
	}

	// Rotate
	newKey, err := s.RotateKey(ctx, userID)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// Old key should fail
	gotID, _ = s.ValidateAPIKey(ctx, rawKey)
	if gotID != "" {
		t.Fatal("old key should be invalid after rotation")
	}

	// New key should work
	gotID, _ = s.ValidateAPIKey(ctx, newKey)
	if gotID != userID {
		t.Fatalf("new key should validate, got userID=%s", gotID)
	}
}

// Task 2.8: Membership blocks non-members
func TestIntegration_ProjectMembership(t *testing.T) {
	s := newTestCloudStore(t)
	ctx := context.Background()

	userID, _, _ := s.CreateUser(ctx, "Alice", "alice@test.com")
	_, _ = s.CreateProject(ctx, "test-project")
	_ = s.AddMember(ctx, "test-project", "alice@test.com", "member")

	// Alice is a member
	isMember, _ := s.IsMember(ctx, "test-project", userID)
	if !isMember {
		t.Fatal("Alice should be a member")
	}

	// Bob is not a member
	bobID, _, _ := s.CreateUser(ctx, "Bob", "bob@test.com")
	isMember, _ = s.IsMember(ctx, "test-project", bobID)
	if isMember {
		t.Fatal("Bob should NOT be a member")
	}

	// List projects for Alice
	projects, _ := s.ListUserProjects(ctx, userID)
	if len(projects) != 1 || projects[0] != "test-project" {
		t.Fatalf("expected [test-project], got %v", projects)
	}
}

// Task 3.5: Push new observation → inserted with server_seq
func TestIntegration_PushNewObservation(t *testing.T) {
	s := newTestCloudStore(t)
	ctx := context.Background()

	userID, _, _ := s.CreateUser(ctx, "Alice", "alice@test.com")
	_, _ = s.CreateProject(ctx, "proj")
	_ = s.AddMember(ctx, "proj", "alice@test.com", "member")

	mutations := []Mutation{{
		Seq:       1,
		Entity:    "observation",
		EntityKey: "obs-new-1",
		Op:        "upsert",
		Payload: map[string]any{
			"sync_id":    "obs-new-1",
			"session_id": "sess-1",
			"type":       "decision",
			"title":      "Use JWT",
			"content":    "Decided to use JWT for auth",
			"scope":      "project",
		},
	}}

	result, err := s.ProcessPush(ctx, mutations, userID, "proj")
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.AckedSeq != 1 {
		t.Fatalf("expected acked_seq=1, got %d", result.AckedSeq)
	}
	if result.ServerSeq < 1 {
		t.Fatalf("expected server_seq >= 1, got %d", result.ServerSeq)
	}

	// Verify observation exists
	var title string
	err = s.pool.QueryRow(ctx,
		"SELECT title FROM observations WHERE sync_id = 'obs-new-1' AND project = 'proj'",
	).Scan(&title)
	if err != nil {
		t.Fatalf("get obs: %v", err)
	}
	if title != "Use JWT" {
		t.Fatalf("expected title 'Use JWT', got '%s'", title)
	}
}

// Task 3.6: Push topic_key conflict → revision created
func TestIntegration_PushTopicKeyConflict(t *testing.T) {
	s := newTestCloudStore(t)
	ctx := context.Background()

	userA, _, _ := s.CreateUser(ctx, "Alice", "alice@test.com")
	userB, _, _ := s.CreateUser(ctx, "Bob", "bob@test.com")
	_, _ = s.CreateProject(ctx, "proj")
	_ = s.AddMember(ctx, "proj", "alice@test.com", "member")
	_ = s.AddMember(ctx, "proj", "bob@test.com", "member")

	// Alice pushes first
	_, err := s.ProcessPush(ctx, []Mutation{{
		Seq: 1, Entity: "observation", EntityKey: "obs-a", Op: "upsert",
		Payload: map[string]any{
			"sync_id": "obs-a", "session_id": "s1", "type": "decision",
			"title": "Alice version", "content": "JWT approach", "scope": "project",
			"topic_key": "architecture/auth",
		},
	}}, userA, "proj")
	if err != nil {
		t.Fatalf("push A: %v", err)
	}

	// Bob pushes with same topic_key, different sync_id
	result, err := s.ProcessPush(ctx, []Mutation{{
		Seq: 1, Entity: "observation", EntityKey: "obs-b", Op: "upsert",
		Payload: map[string]any{
			"sync_id": "obs-b", "session_id": "s2", "type": "decision",
			"title": "Bob version", "content": "Session approach", "scope": "project",
			"topic_key": "architecture/auth",
		},
	}}, userB, "proj")
	if err != nil {
		t.Fatalf("push B: %v", err)
	}

	// Should have a conflict
	if len(result.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(result.Conflicts))
	}
	if result.Conflicts[0].TopicKey != "architecture/auth" {
		t.Fatalf("expected conflict on architecture/auth, got %s", result.Conflicts[0].TopicKey)
	}

	// Alice's version should be in revisions
	var revContent string
	err = s.pool.QueryRow(ctx,
		"SELECT content FROM observation_revisions WHERE topic_key = 'architecture/auth' AND project = 'proj'",
	).Scan(&revContent)
	if err != nil {
		t.Fatalf("get revision: %v", err)
	}
	if revContent != "JWT approach" {
		t.Fatalf("expected revision content 'JWT approach', got '%s'", revContent)
	}

	// Current observation should be Bob's
	var currentTitle string
	err = s.pool.QueryRow(ctx,
		"SELECT title FROM observations WHERE topic_key = 'architecture/auth' AND project = 'proj' AND deleted_at IS NULL",
	).Scan(&currentTitle)
	if err != nil {
		t.Fatalf("get current: %v", err)
	}
	if currentTitle != "Bob version" {
		t.Fatalf("expected 'Bob version', got '%s'", currentTitle)
	}
}

// Task 3.7: Push idempotency
func TestIntegration_PushIdempotency(t *testing.T) {
	s := newTestCloudStore(t)
	ctx := context.Background()

	userID, _, _ := s.CreateUser(ctx, "Alice", "alice@test.com")

	// Save a response
	testResult := &PushResult{AckedSeq: 42, ServerSeq: 100}
	if err := s.SaveIdempotencyKey(ctx, "dev1:uuid-abc", testResult); err != nil {
		t.Fatalf("save key: %v", err)
	}

	// Check should return cached
	cached, err := s.CheckIdempotencyKey(ctx, "dev1:uuid-abc")
	if err != nil {
		t.Fatalf("check key: %v", err)
	}
	if cached == nil {
		t.Fatal("expected cached response, got nil")
	}

	// Unknown key returns nil
	cached, _ = s.CheckIdempotencyKey(ctx, "unknown-key")
	if cached != nil {
		t.Fatal("expected nil for unknown key")
	}

	_ = userID // used for setup
}

// Task 4.3-4.6: Pull protocol tests
func TestIntegration_PullProtocol(t *testing.T) {
	s := newTestCloudStore(t)
	ctx := context.Background()

	userA, _, _ := s.CreateUser(ctx, "Alice", "alice@test.com")
	userB, _, _ := s.CreateUser(ctx, "Bob", "bob@test.com")
	_, _ = s.CreateProject(ctx, "proj")
	_ = s.AddMember(ctx, "proj", "alice@test.com", "member")
	_ = s.AddMember(ctx, "proj", "bob@test.com", "member")

	// Push various entities as Alice
	_, _ = s.ProcessPush(ctx, []Mutation{
		{Seq: 1, Entity: "observation", EntityKey: "obs-pub", Op: "upsert",
			Payload: map[string]any{"sync_id": "obs-pub", "session_id": "s1", "type": "decision", "title": "Public obs", "content": "Visible to all", "scope": "project"}},
		{Seq: 2, Entity: "observation", EntityKey: "obs-priv-a", Op: "upsert",
			Payload: map[string]any{"sync_id": "obs-priv-a", "session_id": "s1", "type": "preference", "title": "Alice personal", "content": "Only Alice sees", "scope": "personal"}},
		{Seq: 3, Entity: "session", EntityKey: "s1", Op: "upsert",
			Payload: map[string]any{"id": "s1", "project": "proj", "directory": "/tmp", "started_at": "2026-04-13T10:00:00Z"}},
		{Seq: 4, Entity: "prompt", EntityKey: "p1", Op: "upsert",
			Payload: map[string]any{"sync_id": "p1", "session_id": "s1", "content": "Alice's prompt", "project": "proj"}},
	}, userA, "proj")

	// Push personal obs as Bob
	_, _ = s.ProcessPush(ctx, []Mutation{
		{Seq: 1, Entity: "observation", EntityKey: "obs-priv-b", Op: "upsert",
			Payload: map[string]any{"sync_id": "obs-priv-b", "session_id": "s2", "type": "preference", "title": "Bob personal", "content": "Only Bob sees", "scope": "personal"}},
		{Seq: 2, Entity: "prompt", EntityKey: "p2", Op: "upsert",
			Payload: map[string]any{"sync_id": "p2", "session_id": "s2", "content": "Bob's prompt", "project": "proj"}},
	}, userB, "proj")

	// Alice pulls — should see: public obs + her personal + session + her prompt
	// Should NOT see: Bob's personal obs or Bob's prompt
	result, err := s.Pull(ctx, "proj", 0, userA, 500)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}

	var foundPub, foundPrivA, foundPrivB, foundSession, foundPromptA, foundPromptB bool
	for _, e := range result.Entities {
		syncID, _ := e.Data["sync_id"].(string)
		switch {
		case syncID == "obs-pub":
			foundPub = true
		case syncID == "obs-priv-a":
			foundPrivA = true
		case syncID == "obs-priv-b":
			foundPrivB = true
		case syncID == "s1" && e.EntityType == "session":
			foundSession = true
		case syncID == "p1":
			foundPromptA = true
		case syncID == "p2":
			foundPromptB = true
		}
	}

	if !foundPub {
		t.Error("Alice should see public observation")
	}
	if !foundPrivA {
		t.Error("Alice should see her personal observation")
	}
	if foundPrivB {
		t.Error("Alice should NOT see Bob's personal observation")
	}
	if !foundSession {
		t.Error("Alice should see the session")
	}
	if !foundPromptA {
		t.Error("Alice should see her own prompt")
	}
	if foundPromptB {
		t.Error("Alice should NOT see Bob's prompt")
	}

	// Test tombstone propagation
	_, _ = s.ProcessPush(ctx, []Mutation{{
		Seq: 5, Entity: "observation", EntityKey: "obs-pub", Op: "delete",
		Payload: map[string]any{"sync_id": "obs-pub"},
	}}, userA, "proj")

	// Bob pulls from seq 0 — should see deleted obs with deleted_at set
	resultB, _ := s.Pull(ctx, "proj", 0, userB, 500)
	var foundDeleted bool
	for _, e := range resultB.Entities {
		syncID, _ := e.Data["sync_id"].(string)
		if syncID == "obs-pub" {
			deletedAt := e.Data["deleted_at"]
			if deletedAt != nil && deletedAt != "" {
				foundDeleted = true
			}
		}
	}
	if !foundDeleted {
		t.Error("Bob should see obs-pub with deleted_at set (tombstone)")
	}
}

// Task 6.8: Full push → pull cycle between two users
func TestIntegration_FullPushPullCycle(t *testing.T) {
	s := newTestCloudStore(t)
	ctx := context.Background()

	// Setup two users
	userA, _, _ := s.CreateUser(ctx, "Alice", "alice@test.com")
	userB, _, _ := s.CreateUser(ctx, "Bob", "bob@test.com")
	_, _ = s.CreateProject(ctx, "engram")
	_ = s.AddMember(ctx, "engram", "alice@test.com", "owner")
	_ = s.AddMember(ctx, "engram", "bob@test.com", "member")

	// Alice pushes 3 observations
	_, err := s.ProcessPush(ctx, []Mutation{
		{Seq: 1, Entity: "observation", EntityKey: "obs-1", Op: "upsert",
			Payload: map[string]any{"sync_id": "obs-1", "session_id": "sa", "type": "decision", "title": "Architecture", "content": "Clean arch", "scope": "project", "topic_key": "arch/pattern"}},
		{Seq: 2, Entity: "observation", EntityKey: "obs-2", Op: "upsert",
			Payload: map[string]any{"sync_id": "obs-2", "session_id": "sa", "type": "bugfix", "title": "Fixed N+1", "content": "Query fix", "scope": "project"}},
		{Seq: 3, Entity: "session", EntityKey: "sa", Op: "upsert",
			Payload: map[string]any{"id": "sa", "project": "engram", "directory": "/home/alice", "started_at": "2026-04-13T08:00:00Z"}},
	}, userA, "engram")
	if err != nil {
		t.Fatalf("Alice push: %v", err)
	}

	// Bob pulls — should see all 3
	result, err := s.Pull(ctx, "engram", 0, userB, 500)
	if err != nil {
		t.Fatalf("Bob pull: %v", err)
	}
	if len(result.Entities) != 3 {
		t.Fatalf("Bob expected 3 entities, got %d", len(result.Entities))
	}

	// Bob pushes an update to the same topic_key
	_, err = s.ProcessPush(ctx, []Mutation{{
		Seq: 1, Entity: "observation", EntityKey: "obs-3", Op: "upsert",
		Payload: map[string]any{"sync_id": "obs-3", "session_id": "sb", "type": "decision", "title": "New Architecture", "content": "Hexagonal", "scope": "project", "topic_key": "arch/pattern"},
	}}, userB, "engram")
	if err != nil {
		t.Fatalf("Bob push: %v", err)
	}

	// Alice pulls from her last cursor — should see Bob's update
	aliceResult, err := s.Pull(ctx, "engram", result.MaxSeq, userA, 500)
	if err != nil {
		t.Fatalf("Alice pull: %v", err)
	}
	if len(aliceResult.Entities) == 0 {
		t.Fatal("Alice should see Bob's new observation")
	}

	// Verify revision was created
	var revCount int
	s.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM observation_revisions WHERE topic_key = 'arch/pattern' AND project = 'engram'",
	).Scan(&revCount)
	if revCount != 1 {
		t.Fatalf("expected 1 revision for arch/pattern, got %d", revCount)
	}
}
