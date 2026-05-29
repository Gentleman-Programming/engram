package cloudstore

import (
	"errors"
	"testing"
	"time"
)

// TestScopedWildcardPassesAllProjects verifies that a wildcard allowlist does not
// filter the dashboard read model — all projects must survive scoped().
func TestScopedWildcardPassesAllProjects(t *testing.T) {
	t1 := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	chunks := []dashboardChunkRow{
		{chunkID: "c1", project: "team-alpha", createdBy: "alice", createdAt: t1,
			parsed: parseMustChunk(t, []byte(`{"sessions":[{"id":"s1","project":"team-alpha","started_at":"2026-04-23T08:00:00Z"}],"observations":[],"prompts":[]}`))},
		{chunkID: "c2", project: "team-beta", createdBy: "bob", createdAt: t1,
			parsed: parseMustChunk(t, []byte(`{"sessions":[{"id":"s2","project":"team-beta","started_at":"2026-04-23T09:00:00Z"}],"observations":[],"prompts":[]}`))},
		{chunkID: "c3", project: "other-project", createdBy: "charlie", createdAt: t1,
			parsed: parseMustChunk(t, []byte(`{"sessions":[{"id":"s3","project":"other-project","started_at":"2026-04-23T10:00:00Z"}],"observations":[],"prompts":[]}`))},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	// Wildcard "*" map — represents the wildcard sentinel.
	wildcard := map[string]struct{}{"*": {}}
	scoped := model.scoped(wildcard)
	if len(scoped.projects) != 3 {
		t.Fatalf("wildcard allowlist must pass all 3 projects through scoped(), got %d: %v", len(scoped.projects), scoped.projects)
	}
}

// TestScopedWithExactAllowlist ensures that scoped() with an explicit list
// (no wildcard) filters the dashboard correctly — backward compatibility guard.
func TestScopedWithExactAllowlist(t *testing.T) {
	t1 := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	chunks := []dashboardChunkRow{
		{chunkID: "c1", project: "team-alpha", createdBy: "alice", createdAt: t1,
			parsed: parseMustChunk(t, []byte(`{"sessions":[{"id":"s1","project":"team-alpha","started_at":"2026-04-23T08:00:00Z"}],"observations":[],"prompts":[]}`))},
		{chunkID: "c2", project: "team-beta", createdBy: "bob", createdAt: t1,
			parsed: parseMustChunk(t, []byte(`{"sessions":[{"id":"s2","project":"team-beta","started_at":"2026-04-23T09:00:00Z"}],"observations":[],"prompts":[]}`))},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	// Explicit list: only "team-alpha" must survive scoped().
	scoped := model.scoped(map[string]struct{}{"team-alpha": {}})
	if len(scoped.projects) != 1 || scoped.projects[0].Project != "team-alpha" {
		t.Fatalf("exact allowlist must keep only team-alpha, got %v", scoped.projects)
	}
}

// TestNormalizeDashboardProjectWildcardAllowsAnyProject verifies that with wildcard
// set, any project passes normalizeDashboardProject (no ErrDashboardProjectForbidden).
func TestNormalizeDashboardProjectWildcardAllowsAnyProject(t *testing.T) {
	cs := &CloudStore{}
	cs.SetDashboardAllowedProjects([]string{"*"})

	_, err := cs.normalizeDashboardProject("any-project")
	if err != nil && !errors.Is(err, ErrDashboardProjectNotFound) {
		// ErrDashboardProjectNotFound is fine (no DB) — ErrDashboardProjectForbidden is not.
		if errors.Is(err, ErrDashboardProjectForbidden) {
			t.Fatalf("wildcard allowlist must not forbid any project, got %v", err)
		}
	}
}
