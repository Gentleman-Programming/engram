package cloudstore

import (
	"errors"
	"testing"
)

func TestDashboardStoreForProjectsCanonicalVisibility(t *testing.T) {
	for _, project := range []string{"alpha project", "alpha%project", "alpha%20project"} {
		t.Run(project, func(t *testing.T) {
			cs := dashboardVisibilityStore(project, "private")
			view, err := cs.DashboardStoreForProjects([]string{NormalizeProjectGrant(project)})
			if err != nil {
				t.Fatal(err)
			}
			rows, err := view.ListProjects("")
			if err != nil || len(rows) != 1 || rows[0].Project != project {
				t.Errorf("unique canonical project must be visible: rows=%+v err=%v", rows, err)
			}
			if detail, err := view.ProjectDetail(project); err != nil || detail.Project != project {
				t.Errorf("canonical detail: %+v err=%v", detail, err)
			}
			if got, err := view.scopedProject(project); err != nil || got != project {
				t.Errorf("canonical control access: %q err=%v", got, err)
			}
			if rows, err := view.ListRecentSessions(project, "", 10); err != nil || len(rows) != 1 || rows[0].Project != project {
				t.Errorf("canonical sessions: %+v err=%v", rows, err)
			}
			assertDashboardVisibilityDenied(t, view, "private")
			// A slug is a grant identity, not an alias for a canonical detail URL.
			assertDashboardVisibilityDenied(t, view, NormalizeProjectGrant(project))
			if rows, err := cs.DashboardStoreForProjects([]string{"*"}); err != nil {
				t.Fatal(err)
			} else if all, err := rows.ListProjects(""); err != nil || len(all) != 2 {
				t.Errorf("shared neutral cache changed: %+v err=%v", all, err)
			}
		})
	}
}

func TestDashboardStoreForProjectsCollisionDenial(t *testing.T) {
	for _, names := range [][]string{
		{"alpha project", "alpha-project"},
		{"alpha%project", "alpha project"},
		{"alpha%20project", "alpha-20project"},
	} {
		t.Run(names[0], func(t *testing.T) {
			cs := dashboardVisibilityStore(append(names, "safe")...)
			view, err := cs.DashboardStoreForProjects([]string{NormalizeProjectGrant(names[0]), "safe"})
			if err != nil {
				t.Fatal(err)
			}
			rows, err := view.ListProjects("")
			if err != nil || len(rows) != 1 || rows[0].Project != "safe" {
				t.Errorf("collision must expose neither canonical name: %+v err=%v", rows, err)
			}
			for _, name := range names {
				assertDashboardVisibilityDenied(t, view, name)
			}
		})
	}
}

func TestDashboardStoreForProjectsEmptyAndWildcard(t *testing.T) {
	cs := dashboardVisibilityStore("alpha project", "alpha-project", "alpha%20project")
	for _, tc := range []struct {
		name   string
		grants []string
		want   int
	}{
		{"empty", nil, 0},
		{"blank", []string{" "}, 0},
		{"unknown", []string{"unknown"}, 0},
		{"wildcard", []string{"*"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view, err := cs.DashboardStoreForProjects(tc.grants)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := view.ListProjects("")
			if err != nil || len(rows) != tc.want {
				t.Fatalf("projects: %+v err=%v want=%d", rows, err, tc.want)
			}
			for _, name := range []string{"alpha project", "alpha-project", "alpha%20project"} {
				if tc.want == 0 {
					assertDashboardVisibilityDenied(t, view, name)
				} else if _, err := view.ProjectDetail(name); err != nil {
					t.Errorf("wildcard detail %q: %v", name, err)
				} else if _, err := view.scopedProject(name); err != nil {
					t.Errorf("wildcard control %q: %v", name, err)
				}
			}
		})
	}
}

func dashboardVisibilityStore(names ...string) *CloudStore {
	model := dashboardReadModel{projectDetails: make(map[string]DashboardProjectDetail)}
	for _, name := range names {
		row := DashboardProjectRow{Project: name, Sessions: 1}
		model.projects = append(model.projects, row)
		model.projectDetails[name] = DashboardProjectDetail{
			Project: name, Stats: row,
			Sessions: []DashboardSessionRow{{Project: name, SessionID: "session"}},
		}
	}
	return &CloudStore{dashboardReadModel: model, dashboardReadModelOK: true}
}

func assertDashboardVisibilityDenied(t *testing.T, view *DashboardScopedStore, project string) {
	t.Helper()
	if _, err := view.ProjectDetail(project); !errors.Is(err, ErrDashboardProjectForbidden) {
		t.Errorf("detail %q must be forbidden: %v", project, err)
	}
	if _, err := view.ListRecentSessions(project, "", 10); !errors.Is(err, ErrDashboardProjectForbidden) {
		t.Errorf("sessions %q must be forbidden: %v", project, err)
	}
	if err := view.SetProjectSyncEnabled(project, false, "tester", "denial regression"); !errors.Is(err, ErrDashboardProjectForbidden) {
		t.Errorf("mutation %q must be forbidden before reaching persistence: %v", project, err)
	}
}
