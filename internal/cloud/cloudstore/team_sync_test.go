package cloudstore

import (
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud"
)

// teamTestSetup creates a CloudStore with two users enrolled in the same project.
// Returns (store, userA_ID, userB_ID).
func teamTestSetup(t *testing.T) (*CloudStore, string, string) {
	t.Helper()
	dsn := testDSN(t)
	cs, err := New(cloud.Config{DSN: dsn, MaxPool: 5})
	if err != nil {
		t.Fatalf("cloudstore.New: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	userA, err := cs.CreateUser("alice", "alice@test.com", "password123")
	if err != nil {
		t.Fatalf("CreateUser A: %v", err)
	}

	userB, err := cs.CreateUser("bob", "bob@test.com", "password123")
	if err != nil {
		t.Fatalf("CreateUser B: %v", err)
	}

	return cs, userA.ID, userB.ID
}

// ── Enrollment CRUD ──────────────────────────────────────────────────────────

func TestEnrollProject(t *testing.T) {
	cs, userA, _ := teamTestSetup(t)

	if err := cs.EnrollProject(userA, "my-project"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	projects, err := cs.ListEnrolledProjects(userA)
	if err != nil {
		t.Fatalf("ListEnrolledProjects: %v", err)
	}
	if len(projects) != 1 || projects[0] != "my-project" {
		t.Fatalf("expected [my-project], got %v", projects)
	}
}

func TestEnrollProjectIdempotent(t *testing.T) {
	cs, userA, _ := teamTestSetup(t)

	if err := cs.EnrollProject(userA, "proj"); err != nil {
		t.Fatalf("first EnrollProject: %v", err)
	}
	if err := cs.EnrollProject(userA, "proj"); err != nil {
		t.Fatalf("second EnrollProject: %v", err)
	}

	projects, err := cs.ListEnrolledProjects(userA)
	if err != nil {
		t.Fatalf("ListEnrolledProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
}

func TestUnenrollProject(t *testing.T) {
	cs, userA, _ := teamTestSetup(t)

	if err := cs.EnrollProject(userA, "proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	if err := cs.UnenrollProject(userA, "proj"); err != nil {
		t.Fatalf("UnenrollProject: %v", err)
	}

	projects, err := cs.ListEnrolledProjects(userA)
	if err != nil {
		t.Fatalf("ListEnrolledProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("expected 0 projects, got %d", len(projects))
	}
}

func TestSyncEnrollments(t *testing.T) {
	cs, userA, _ := teamTestSetup(t)

	// Enroll initially.
	if err := cs.EnrollProject(userA, "old-project"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	// Sync replaces the entire list.
	if err := cs.SyncEnrollments(userA, []string{"proj-1", "proj-2"}); err != nil {
		t.Fatalf("SyncEnrollments: %v", err)
	}

	projects, err := cs.ListEnrolledProjects(userA)
	if err != nil {
		t.Fatalf("ListEnrolledProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d: %v", len(projects), projects)
	}
	if projects[0] != "proj-1" || projects[1] != "proj-2" {
		t.Fatalf("expected [proj-1, proj-2], got %v", projects)
	}
}

func TestEnrollProjectEmptyNameFails(t *testing.T) {
	cs, userA, _ := teamTestSetup(t)

	if err := cs.EnrollProject(userA, ""); err == nil {
		t.Fatal("expected error for empty project name")
	}
}

func TestEnrollmentIsolation(t *testing.T) {
	cs, userA, userB := teamTestSetup(t)

	if err := cs.EnrollProject(userA, "alice-only"); err != nil {
		t.Fatalf("EnrollProject A: %v", err)
	}

	// User B should have no enrollments.
	projects, err := cs.ListEnrolledProjects(userB)
	if err != nil {
		t.Fatalf("ListEnrolledProjects B: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("expected 0 projects for user B, got %d", len(projects))
	}
}

// ── Cross-User Pull (Team Sync) ─────────────────────────────────────────────

func TestTeamSyncPullCrossUserObservations(t *testing.T) {
	cs, userA, userB := teamTestSetup(t)

	// Both users enroll in the same project.
	if err := cs.EnrollProject(userA, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject A: %v", err)
	}
	if err := cs.EnrollProject(userB, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject B: %v", err)
	}

	// User A pushes an observation mutation.
	obsPayload := json.RawMessage(`{"sync_id":"obs-1","session_id":"s1","type":"decision","title":"Team decision","content":"We chose X","project":"shared-proj","scope":"project"}`)
	_, err := cs.AppendMutation(userA, "observation", "obs-1", "upsert", obsPayload)
	if err != nil {
		t.Fatalf("AppendMutation A obs: %v", err)
	}

	// User B pulls with team sync — should see user A's observation.
	result, err := cs.PullMutationsWithTeamSync(userB, 0, 100)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync B: %v", err)
	}
	if len(result.Mutations) != 1 {
		t.Fatalf("expected 1 cross-user mutation, got %d", len(result.Mutations))
	}
	if result.Mutations[0].Entity != "observation" {
		t.Fatalf("expected observation entity, got %s", result.Mutations[0].Entity)
	}
	if result.Mutations[0].UserID != userA {
		t.Fatalf("expected mutation from user A (%s), got %s", userA, result.Mutations[0].UserID)
	}
}

func TestTeamSyncExcludesPersonalScope(t *testing.T) {
	cs, userA, userB := teamTestSetup(t)

	// Both enroll in the same project.
	if err := cs.EnrollProject(userA, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject A: %v", err)
	}
	if err := cs.EnrollProject(userB, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject B: %v", err)
	}

	// User A pushes a personal-scoped observation.
	personalPayload := json.RawMessage(`{"sync_id":"obs-personal","session_id":"s1","type":"note","title":"Private note","content":"My personal thought","project":"shared-proj","scope":"personal"}`)
	_, err := cs.AppendMutation(userA, "observation", "obs-personal", "upsert", personalPayload)
	if err != nil {
		t.Fatalf("AppendMutation A personal: %v", err)
	}

	// User A also pushes a project-scoped observation.
	projectPayload := json.RawMessage(`{"sync_id":"obs-public","session_id":"s1","type":"decision","title":"Public decision","content":"Team knows this","project":"shared-proj","scope":"project"}`)
	_, err = cs.AppendMutation(userA, "observation", "obs-public", "upsert", projectPayload)
	if err != nil {
		t.Fatalf("AppendMutation A project: %v", err)
	}

	// User B pulls — should only see the project-scoped observation, not the personal one.
	result, err := cs.PullMutationsWithTeamSync(userB, 0, 100)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync B: %v", err)
	}
	if len(result.Mutations) != 1 {
		t.Fatalf("expected 1 mutation (only project-scoped), got %d", len(result.Mutations))
	}
	if result.Mutations[0].EntityKey != "obs-public" {
		t.Fatalf("expected obs-public, got %s", result.Mutations[0].EntityKey)
	}
}

func TestTeamSyncExcludesSessionsAndPrompts(t *testing.T) {
	cs, userA, userB := teamTestSetup(t)

	// Both enroll.
	if err := cs.EnrollProject(userA, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject A: %v", err)
	}
	if err := cs.EnrollProject(userB, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject B: %v", err)
	}

	// User A pushes a session mutation.
	_, err := cs.AppendMutation(userA, "session", "s-a", "upsert",
		json.RawMessage(`{"id":"s-a","project":"shared-proj","directory":"/work"}`))
	if err != nil {
		t.Fatalf("AppendMutation A session: %v", err)
	}

	// User A pushes a prompt mutation.
	_, err = cs.AppendMutation(userA, "prompt", "p-a", "upsert",
		json.RawMessage(`{"session_id":"s-a","content":"test prompt","project":"shared-proj"}`))
	if err != nil {
		t.Fatalf("AppendMutation A prompt: %v", err)
	}

	// User A pushes an observation mutation.
	_, err = cs.AppendMutation(userA, "observation", "obs-a", "upsert",
		json.RawMessage(`{"sync_id":"obs-a","session_id":"s-a","type":"decision","title":"Shared","content":"Team info","project":"shared-proj","scope":"project"}`))
	if err != nil {
		t.Fatalf("AppendMutation A obs: %v", err)
	}

	// User B pulls — should only see the observation, not the session or prompt.
	result, err := cs.PullMutationsWithTeamSync(userB, 0, 100)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync B: %v", err)
	}
	if len(result.Mutations) != 1 {
		t.Fatalf("expected 1 mutation (only observation), got %d", len(result.Mutations))
	}
	if result.Mutations[0].Entity != "observation" {
		t.Fatalf("expected observation entity, got %s", result.Mutations[0].Entity)
	}
}

func TestTeamSyncOwnMutationsStillIncluded(t *testing.T) {
	cs, userA, userB := teamTestSetup(t)

	// Both enroll.
	if err := cs.EnrollProject(userA, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject A: %v", err)
	}
	if err := cs.EnrollProject(userB, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject B: %v", err)
	}

	// User A pushes a session + observation.
	_, err := cs.AppendMutation(userA, "session", "s-a", "upsert",
		json.RawMessage(`{"id":"s-a","project":"shared-proj","directory":"/work"}`))
	if err != nil {
		t.Fatalf("AppendMutation A session: %v", err)
	}
	_, err = cs.AppendMutation(userA, "observation", "obs-a", "upsert",
		json.RawMessage(`{"sync_id":"obs-a","session_id":"s-a","type":"decision","title":"Test","content":"Content","project":"shared-proj","scope":"project"}`))
	if err != nil {
		t.Fatalf("AppendMutation A obs: %v", err)
	}

	// User A pulls — should see both (own session + own observation).
	result, err := cs.PullMutationsWithTeamSync(userA, 0, 100)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync A: %v", err)
	}
	if len(result.Mutations) != 2 {
		t.Fatalf("expected 2 own mutations, got %d", len(result.Mutations))
	}
}

func TestTeamSyncNoEnrollmentsFallsBackToUserOnly(t *testing.T) {
	cs, userA, userB := teamTestSetup(t)

	// User A pushes an observation (no enrollments at all).
	_, err := cs.AppendMutation(userA, "observation", "obs-a", "upsert",
		json.RawMessage(`{"sync_id":"obs-a","session_id":"s-a","type":"decision","title":"Test","content":"Content","project":"some-proj","scope":"project"}`))
	if err != nil {
		t.Fatalf("AppendMutation A: %v", err)
	}

	// User B pulls with team sync but has no enrollments — should get nothing
	// (falls back to user-only PullMutations).
	result, err := cs.PullMutationsWithTeamSync(userB, 0, 100)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync B: %v", err)
	}
	if len(result.Mutations) != 0 {
		t.Fatalf("expected 0 mutations (no enrollments = user-only), got %d", len(result.Mutations))
	}
}

func TestTeamSyncDifferentProjectsNoLeakage(t *testing.T) {
	cs, userA, userB := teamTestSetup(t)

	// User A enrolls in project-X, user B enrolls in project-Y (different projects).
	if err := cs.EnrollProject(userA, "project-X"); err != nil {
		t.Fatalf("EnrollProject A: %v", err)
	}
	if err := cs.EnrollProject(userB, "project-Y"); err != nil {
		t.Fatalf("EnrollProject B: %v", err)
	}

	// User A pushes observation for project-X.
	_, err := cs.AppendMutation(userA, "observation", "obs-x", "upsert",
		json.RawMessage(`{"sync_id":"obs-x","session_id":"s-a","type":"decision","title":"X-only","content":"Private to X","project":"project-X","scope":"project"}`))
	if err != nil {
		t.Fatalf("AppendMutation A: %v", err)
	}

	// User B pulls — should see nothing because they are enrolled in a different project.
	result, err := cs.PullMutationsWithTeamSync(userB, 0, 100)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync B: %v", err)
	}
	if len(result.Mutations) != 0 {
		t.Fatalf("expected 0 mutations (different projects), got %d", len(result.Mutations))
	}
}

func TestTeamSyncMultipleSharedProjects(t *testing.T) {
	cs, userA, userB := teamTestSetup(t)

	// Both enroll in two projects.
	for _, proj := range []string{"proj-1", "proj-2"} {
		if err := cs.EnrollProject(userA, proj); err != nil {
			t.Fatalf("EnrollProject A %s: %v", proj, err)
		}
		if err := cs.EnrollProject(userB, proj); err != nil {
			t.Fatalf("EnrollProject B %s: %v", proj, err)
		}
	}

	// User A pushes observations for both projects.
	_, err := cs.AppendMutation(userA, "observation", "obs-p1", "upsert",
		json.RawMessage(`{"sync_id":"obs-p1","session_id":"s1","type":"decision","title":"P1","content":"Project 1","project":"proj-1","scope":"project"}`))
	if err != nil {
		t.Fatalf("AppendMutation A proj-1: %v", err)
	}
	_, err = cs.AppendMutation(userA, "observation", "obs-p2", "upsert",
		json.RawMessage(`{"sync_id":"obs-p2","session_id":"s1","type":"decision","title":"P2","content":"Project 2","project":"proj-2","scope":"project"}`))
	if err != nil {
		t.Fatalf("AppendMutation A proj-2: %v", err)
	}

	// User B pulls — should see both.
	result, err := cs.PullMutationsWithTeamSync(userB, 0, 100)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync B: %v", err)
	}
	if len(result.Mutations) != 2 {
		t.Fatalf("expected 2 cross-user mutations, got %d", len(result.Mutations))
	}
}

func TestTeamSyncPaginationWorks(t *testing.T) {
	cs, userA, userB := teamTestSetup(t)

	// Both enroll.
	if err := cs.EnrollProject(userA, "proj"); err != nil {
		t.Fatalf("EnrollProject A: %v", err)
	}
	if err := cs.EnrollProject(userB, "proj"); err != nil {
		t.Fatalf("EnrollProject B: %v", err)
	}

	// User A pushes 5 observation mutations.
	for i := 0; i < 5; i++ {
		payload := json.RawMessage(`{"sync_id":"obs-` + string(rune('a'+i)) + `","session_id":"s1","type":"decision","title":"Test","content":"Content","project":"proj","scope":"project"}`)
		_, err := cs.AppendMutation(userA, "observation", "obs-"+string(rune('a'+i)), "upsert", payload)
		if err != nil {
			t.Fatalf("AppendMutation %d: %v", i, err)
		}
	}

	// User B pulls with limit=2 — should see 2 mutations with has_more=true.
	result, err := cs.PullMutationsWithTeamSync(userB, 0, 2)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync page 1: %v", err)
	}
	if len(result.Mutations) != 2 {
		t.Fatalf("expected 2 mutations on page 1, got %d", len(result.Mutations))
	}
	if !result.HasMore {
		t.Fatal("expected has_more=true on page 1")
	}

	// Pull next page.
	lastSeq := result.Mutations[1].Seq
	result2, err := cs.PullMutationsWithTeamSync(userB, lastSeq, 10)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync page 2: %v", err)
	}
	if len(result2.Mutations) != 3 {
		t.Fatalf("expected 3 mutations on page 2, got %d", len(result2.Mutations))
	}
	if result2.HasMore {
		t.Fatal("expected has_more=false on last page")
	}
}

func TestUserHasEnrollments(t *testing.T) {
	cs, userA, userB := teamTestSetup(t)

	// Initially no enrollments.
	has, err := cs.userHasEnrollments(userA)
	if err != nil {
		t.Fatalf("userHasEnrollments: %v", err)
	}
	if has {
		t.Fatal("expected no enrollments initially")
	}

	// Enroll user A.
	if err := cs.EnrollProject(userA, "proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	has, err = cs.userHasEnrollments(userA)
	if err != nil {
		t.Fatalf("userHasEnrollments after enroll: %v", err)
	}
	if !has {
		t.Fatal("expected enrollments after enroll")
	}

	// User B still has none.
	has, err = cs.userHasEnrollments(userB)
	if err != nil {
		t.Fatalf("userHasEnrollments B: %v", err)
	}
	if has {
		t.Fatal("expected no enrollments for user B")
	}
}

// ── Author Attribution ───────────────────────────────────────────────────────

func TestTeamSyncCrossUserMutationsHaveAuthor(t *testing.T) {
	cs, userA, userB := teamTestSetup(t)

	// Both users enroll in the same project.
	if err := cs.EnrollProject(userA, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject A: %v", err)
	}
	if err := cs.EnrollProject(userB, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject B: %v", err)
	}

	// User A (alice) pushes an observation.
	obsPayload := json.RawMessage(`{"sync_id":"obs-auth-1","session_id":"s1","type":"decision","title":"Alice's decision","content":"We chose X","project":"shared-proj","scope":"project"}`)
	_, err := cs.AppendMutation(userA, "observation", "obs-auth-1", "upsert", obsPayload)
	if err != nil {
		t.Fatalf("AppendMutation A: %v", err)
	}

	// User B pulls with team sync — cross-user mutation should have author="alice".
	result, err := cs.PullMutationsWithTeamSync(userB, 0, 100)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync B: %v", err)
	}
	if len(result.Mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(result.Mutations))
	}
	if result.Mutations[0].Author != "alice" {
		t.Fatalf("expected author='alice', got %q", result.Mutations[0].Author)
	}
}

func TestTeamSyncOwnMutationsHaveEmptyAuthor(t *testing.T) {
	cs, userA, _ := teamTestSetup(t)

	// User A enrolls.
	if err := cs.EnrollProject(userA, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject A: %v", err)
	}

	// User A pushes an observation.
	obsPayload := json.RawMessage(`{"sync_id":"obs-own-1","session_id":"s1","type":"decision","title":"Own decision","content":"My stuff","project":"shared-proj","scope":"project"}`)
	_, err := cs.AppendMutation(userA, "observation", "obs-own-1", "upsert", obsPayload)
	if err != nil {
		t.Fatalf("AppendMutation A: %v", err)
	}

	// User A pulls their own mutations — author should be empty (own mutations).
	result, err := cs.PullMutationsWithTeamSync(userA, 0, 100)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync A: %v", err)
	}
	if len(result.Mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(result.Mutations))
	}
	if result.Mutations[0].Author != "" {
		t.Fatalf("expected empty author for own mutation, got %q", result.Mutations[0].Author)
	}
}

func TestTeamSyncAuthorPreservedThroughRoundtrip(t *testing.T) {
	cs, userA, userB := teamTestSetup(t)

	// Both enroll.
	if err := cs.EnrollProject(userA, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject A: %v", err)
	}
	if err := cs.EnrollProject(userB, "shared-proj"); err != nil {
		t.Fatalf("EnrollProject B: %v", err)
	}

	// User A (alice) pushes multiple observations.
	for i := 0; i < 3; i++ {
		payload := json.RawMessage(`{"sync_id":"obs-rt-` + string(rune('a'+i)) + `","session_id":"s1","type":"decision","title":"Decision","content":"Content","project":"shared-proj","scope":"project"}`)
		_, err := cs.AppendMutation(userA, "observation", "obs-rt-"+string(rune('a'+i)), "upsert", payload)
		if err != nil {
			t.Fatalf("AppendMutation %d: %v", i, err)
		}
	}

	// User B pulls page 1 (limit=2).
	result1, err := cs.PullMutationsWithTeamSync(userB, 0, 2)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync page 1: %v", err)
	}
	if len(result1.Mutations) != 2 {
		t.Fatalf("expected 2 mutations on page 1, got %d", len(result1.Mutations))
	}
	for i, m := range result1.Mutations {
		if m.Author != "alice" {
			t.Fatalf("page 1 mutation[%d]: expected author='alice', got %q", i, m.Author)
		}
	}

	// User B pulls page 2.
	lastSeq := result1.Mutations[1].Seq
	result2, err := cs.PullMutationsWithTeamSync(userB, lastSeq, 10)
	if err != nil {
		t.Fatalf("PullMutationsWithTeamSync page 2: %v", err)
	}
	if len(result2.Mutations) != 1 {
		t.Fatalf("expected 1 mutation on page 2, got %d", len(result2.Mutations))
	}
	if result2.Mutations[0].Author != "alice" {
		t.Fatalf("page 2 mutation: expected author='alice', got %q", result2.Mutations[0].Author)
	}
}
