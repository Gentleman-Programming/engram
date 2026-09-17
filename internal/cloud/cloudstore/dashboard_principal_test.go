package cloudstore

import (
	"errors"
	"testing"
)

func dashboardPrincipalReadModel() dashboardReadModel {
	projectA := DashboardProjectDetail{
		Project:      "project-a",
		Stats:        DashboardProjectRow{Project: "project-a", Chunks: 2, Sessions: 1, Observations: 1, Prompts: 1},
		Contributors: []DashboardContributorRow{{CreatedBy: "alice", Chunks: 2, Projects: 1}},
		Sessions:     []DashboardSessionRow{{Project: "project-a", SessionID: "session-a"}},
		Observations: []DashboardObservationRow{{Project: "project-a", SessionID: "session-a", SyncID: "observation-a", Title: "allowed observation"}},
		Prompts:      []DashboardPromptRow{{Project: "project-a", SessionID: "session-a", SyncID: "prompt-a", Content: "allowed prompt"}},
	}
	projectB := DashboardProjectDetail{
		Project:      "project-b",
		Stats:        DashboardProjectRow{Project: "project-b", Chunks: 3, Sessions: 1, Observations: 1, Prompts: 1},
		Contributors: []DashboardContributorRow{{CreatedBy: "bob", Chunks: 3, Projects: 1}},
		Sessions:     []DashboardSessionRow{{Project: "project-b", SessionID: "session-b"}},
		Observations: []DashboardObservationRow{{Project: "project-b", SessionID: "session-b", SyncID: "observation-b", Title: "private observation"}},
		Prompts:      []DashboardPromptRow{{Project: "project-b", SessionID: "session-b", SyncID: "prompt-b", Content: "private prompt"}},
	}
	return dashboardReadModel{
		projects:       []DashboardProjectRow{projectA.Stats, projectB.Stats},
		contributors:   []DashboardContributorRow{{CreatedBy: "bob", Chunks: 3, Projects: 1}, {CreatedBy: "alice", Chunks: 2, Projects: 1}},
		projectDetails: map[string]DashboardProjectDetail{"project-a": projectA, "project-b": projectB},
		admin:          DashboardAdminOverview{Projects: 2, Contributors: 2, Chunks: 5},
	}
}

func TestDashboardPrincipalScopeIntersectsAllowlistAndDeniesExplicitReads(t *testing.T) {
	store := &CloudStore{dashboardReadModel: dashboardPrincipalReadModel(), dashboardReadModelOK: true}

	view, err := store.DashboardStoreForProjects([]string{"project-a", "outside-deployment"})
	if err != nil {
		t.Fatalf("DashboardStoreForProjects: %v", err)
	}

	projects, err := view.ListProjects("")
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].Project != "project-a" {
		t.Fatalf("expected only deployment-allowed granted project, got %+v", projects)
	}
	for name, rows := range map[string]int{
		"observations": len(mustDashboardPrincipalObservations(t, view)),
		"sessions":     len(mustDashboardPrincipalSessions(t, view)),
		"prompts":      len(mustDashboardPrincipalPrompts(t, view)),
		"contributors": len(mustDashboardPrincipalContributors(t, view)),
	} {
		if rows != 1 {
			t.Fatalf("expected one scoped %s row, got %d", name, rows)
		}
	}
	overview, err := view.AdminOverview()
	if err != nil {
		t.Fatalf("AdminOverview: %v", err)
	}
	if overview != (DashboardAdminOverview{Projects: 1, Contributors: 1, Chunks: 2}) {
		t.Fatalf("expected scoped overview, got %+v", overview)
	}
	if _, err := view.ProjectDetail("project-b"); !errors.Is(err, ErrDashboardProjectForbidden) {
		t.Fatalf("expected explicit ungranted project read to be forbidden, got %v", err)
	}
	if _, _, _, err := view.GetObservationDetail("project-b", "session-b", "observation-b"); !errors.Is(err, ErrDashboardProjectForbidden) {
		t.Fatalf("expected explicit ungranted entity read to be forbidden, got %v", err)
	}
}

func TestDashboardPrincipalScopeHonorsDeploymentWildcardAndZeroGrants(t *testing.T) {
	deploymentScoped := dashboardPrincipalReadModel().scoped(map[string]struct{}{"project-a": {}})
	store := &CloudStore{dashboardReadModel: deploymentScoped, dashboardReadModelOK: true}

	intersection, err := store.DashboardStoreForProjects([]string{"project-a", "project-b"})
	if err != nil {
		t.Fatalf("DashboardStoreForProjects intersection: %v", err)
	}
	assertDashboardPrincipalProjects(t, intersection, "project-a")

	wildcardDeployment := &CloudStore{dashboardReadModel: dashboardPrincipalReadModel(), dashboardReadModelOK: true, dashboardAllowedAll: true}
	wildcard, err := wildcardDeployment.DashboardStoreForProjects([]string{"project-a"})
	if err != nil {
		t.Fatalf("DashboardStoreForProjects wildcard deployment: %v", err)
	}
	assertDashboardPrincipalProjects(t, wildcard, "project-a")

	zeroGrants, err := store.DashboardStoreForProjects(nil)
	if err != nil {
		t.Fatalf("DashboardStoreForProjects zero grants: %v", err)
	}
	if projects, err := zeroGrants.ListProjects(""); err != nil || len(projects) != 0 {
		t.Fatalf("expected zero grants to expose no projects, projects=%+v err=%v", projects, err)
	}
	if _, err := zeroGrants.ProjectDetail("project-a"); !errors.Is(err, ErrDashboardProjectForbidden) {
		t.Fatalf("expected zero grants to forbid explicit project read, got %v", err)
	}
}

func TestDashboardPrincipalScopeKeepsSharedCacheIsolatedBetweenSequentialPrincipals(t *testing.T) {
	store := &CloudStore{dashboardReadModel: dashboardPrincipalReadModel(), dashboardReadModelOK: true}

	alice, err := store.DashboardStoreForProjects([]string{"project-a"})
	if err != nil {
		t.Fatalf("alice DashboardStoreForProjects: %v", err)
	}
	bob, err := store.DashboardStoreForProjects([]string{"project-b"})
	if err != nil {
		t.Fatalf("bob DashboardStoreForProjects: %v", err)
	}
	assertDashboardPrincipalProjects(t, alice, "project-a")
	assertDashboardPrincipalProjects(t, bob, "project-b")
	assertDashboardPrincipalProjects(t, alice, "project-a")
}

func mustDashboardPrincipalObservations(t *testing.T, view *DashboardScopedStore) []DashboardObservationRow {
	t.Helper()
	rows, _, err := view.ListRecentObservationsPaginated("", "", "", 10, 0)
	if err != nil {
		t.Fatalf("ListRecentObservationsPaginated: %v", err)
	}
	return rows
}

func mustDashboardPrincipalSessions(t *testing.T, view *DashboardScopedStore) []DashboardSessionRow {
	t.Helper()
	rows, _, err := view.ListRecentSessionsPaginated("", "", 10, 0)
	if err != nil {
		t.Fatalf("ListRecentSessionsPaginated: %v", err)
	}
	return rows
}

func mustDashboardPrincipalPrompts(t *testing.T, view *DashboardScopedStore) []DashboardPromptRow {
	t.Helper()
	rows, _, err := view.ListRecentPromptsPaginated("", "", 10, 0)
	if err != nil {
		t.Fatalf("ListRecentPromptsPaginated: %v", err)
	}
	return rows
}

func mustDashboardPrincipalContributors(t *testing.T, view *DashboardScopedStore) []DashboardContributorRow {
	t.Helper()
	rows, _, err := view.ListContributorsPaginated("", 10, 0)
	if err != nil {
		t.Fatalf("ListContributorsPaginated: %v", err)
	}
	return rows
}

func assertDashboardPrincipalProjects(t *testing.T, view *DashboardScopedStore, want string) {
	t.Helper()
	rows, err := view.ListProjects("")
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(rows) != 1 || rows[0].Project != want {
		t.Fatalf("expected only %q, got %+v", want, rows)
	}
}
