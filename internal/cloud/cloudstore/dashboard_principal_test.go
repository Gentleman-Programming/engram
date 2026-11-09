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

func TestDashboardPrincipalScopeSyncControlsUseGrantsOnly(t *testing.T) {
	store := &CloudStore{
		dashboardAllowedScopes: map[string]struct{}{"project-a": {}},
		dashboardReadModel:     dashboardPrincipalReadModel(),
		dashboardReadModelOK:   true,
	}

	wildcard, err := store.DashboardStoreForProjects([]string{"*"})
	if err != nil {
		t.Fatalf("DashboardStoreForProjects wildcard: %v", err)
	}
	if rows, err := wildcard.ListProjects(""); err != nil || len(rows) != 2 {
		t.Fatalf("explicit managed wildcard must include both projects: %+v %v", rows, err)
	}
	if _, err := wildcard.scopedProject("project-b"); err != nil {
		t.Fatalf("managed wildcard must allow project-b controls: %v", err)
	}

	stale, err := store.DashboardStoreForProjects([]string{"project-b"})
	if err != nil {
		t.Fatalf("DashboardStoreForProjects stale grant: %v", err)
	}
	assertDashboardPrincipalProjects(t, stale, "project-b")
	if _, err := stale.scopedProject("project-b"); err != nil {
		t.Fatalf("grant outside legacy scope must allow controls: %v", err)
	}
}

func TestDashboardPrincipalSyncControlsRejectInvalidRecords(t *testing.T) {
	store := &CloudStore{
		dashboardAllowedScopes: map[string]struct{}{"project-a": {}},
		dashboardReadModel:     dashboardPrincipalReadModel(),
		dashboardReadModelOK:   true,
	}
	view, err := store.DashboardStoreForProjects([]string{"project-a"})
	if err != nil {
		t.Fatalf("DashboardStoreForProjects: %v", err)
	}

	controls, err := view.filterProjectSyncControls([]ProjectSyncControl{{Project: "project-a"}, {Project: "project-b"}})
	if err != nil {
		t.Fatalf("filterProjectSyncControls: %v", err)
	}
	if len(controls) != 1 || controls[0].Project != "project-a" {
		t.Fatalf("expected only effective-scope control, got %+v", controls)
	}
	if _, err := view.filterProjectSyncControls([]ProjectSyncControl{{Project: " "}}); !errors.Is(err, ErrDashboardProjectInvalid) {
		t.Fatalf("expected invalid control project error, got %v", err)
	}
}

func TestDashboardPrincipalScopeKeepsSharedCacheIsolatedBetweenSequentialPrincipals(t *testing.T) {
	store := &CloudStore{dashboardReadModel: dashboardPrincipalReadModel(), dashboardReadModelOK: true, dashboardAllowedScopes: map[string]struct{}{"project-a": {}}}

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

func TestDashboardPrincipalScopeOutsideLegacyAndEmptyRegistration(t *testing.T) {
	model := dashboardPrincipalReadModel().withRegisteredProjects([]ProjectSyncControl{{Project: "empty"}})
	store := &CloudStore{dashboardReadModel: model, dashboardReadModelOK: true, dashboardAllowedScopes: map[string]struct{}{"project-a": {}}}
	view, err := store.DashboardStoreForProjects([]string{"project-b", "empty"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := view.ListProjects("")
	if err != nil || len(rows) != 2 {
		t.Fatalf("managed inventory: %+v %v", rows, err)
	}
	empty, err := view.ProjectDetail("empty")
	if err != nil || empty.Stats != (DashboardProjectRow{Project: "empty"}) {
		t.Fatalf("empty registration: %+v %v", empty, err)
	}
	if _, err := view.ProjectDetail("project-a"); !errors.Is(err, ErrDashboardProjectForbidden) {
		t.Fatalf("ungranted detail: %v", err)
	}
	if obs := mustDashboardPrincipalObservations(t, view); len(obs) != 1 || obs[0].Project != "project-b" {
		t.Fatalf("observations: %+v", obs)
	}
	if sessions := mustDashboardPrincipalSessions(t, view); len(sessions) != 1 || sessions[0].Project != "project-b" {
		t.Fatalf("sessions: %+v", sessions)
	}
	if prompts := mustDashboardPrincipalPrompts(t, view); len(prompts) != 1 || prompts[0].Project != "project-b" {
		t.Fatalf("prompts: %+v", prompts)
	}
	if overview, err := view.AdminOverview(); err != nil || overview != (DashboardAdminOverview{Projects: 2, Contributors: 1, Chunks: 3}) {
		t.Fatalf("managed stats: %+v %v", overview, err)
	}
	legacy, err := store.ListProjects("")
	if err != nil || len(legacy) != 1 || legacy[0].Project != "project-a" {
		t.Fatalf("legacy inventory: %+v %v", legacy, err)
	}
	if _, err := store.ProjectDetail("project-b"); !errors.Is(err, ErrDashboardProjectForbidden) {
		t.Fatalf("legacy exclusion: %v", err)
	}
	store.dashboardAllowedScopes = map[string]struct{}{"*": {}}
	store.dashboardAllowedAll = true
	if rows, err := store.ListProjects(""); err != nil || len(rows) != 3 {
		t.Fatalf("legacy explicit wildcard: %+v %v", rows, err)
	}
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
