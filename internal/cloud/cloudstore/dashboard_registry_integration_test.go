package cloudstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func openDashboardRegistryStore(t *testing.T) *CloudStore {
	t.Helper()
	if testing.Short() {
		t.Skip("Postgres integration test")
	}
	return openIsolatedCloudStore(t)
}

func TestDashboardRegistryPostgresEmptyProject(t *testing.T) {
	cs := openDashboardRegistryStore(t)
	if _, err := cs.db.ExecContext(context.Background(), `INSERT INTO cloud_project_controls (project, sync_enabled) VALUES ('empty', TRUE)`); err != nil {
		t.Fatal(err)
	}
	view, err := cs.DashboardStoreForProjects([]string{"empty"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := view.ListProjects("")
	if err != nil || len(rows) != 1 || rows[0] != (DashboardProjectRow{Project: "empty"}) {
		t.Fatalf("registry-only inventory: %+v %v", rows, err)
	}
	detail, err := view.ProjectDetail("empty")
	if err != nil || detail.Stats != rows[0] || len(detail.Sessions)+len(detail.Observations)+len(detail.Prompts)+len(detail.Contributors) != 0 {
		t.Fatalf("empty detail: %+v %v", detail, err)
	}
	overview, err := view.AdminOverview()
	if err != nil || overview != (DashboardAdminOverview{Projects: 1}) {
		t.Fatalf("empty stats: %+v %v", overview, err)
	}
}

func TestDashboardRegistryPostgresRegistrationRefresh(t *testing.T) {
	cs := openDashboardRegistryStore(t)
	view, err := cs.DashboardStoreForProjects([]string{"new-project"})
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := view.ListProjects(""); err != nil || len(rows) != 0 {
		t.Fatalf("warm empty inventory: %+v %v", rows, err)
	}
	if err := cs.SetProjectSyncEnabled("new-project", true, "tester", "registered"); err != nil {
		t.Fatal(err)
	}
	// Scoped stores are request snapshots; the next request must reload inventory.
	view, err = cs.DashboardStoreForProjects([]string{"new-project"})
	if err != nil {
		t.Fatal(err)
	}
	assertDashboardPrincipalProjects(t, view, "new-project")
	detail, err := view.ProjectDetail("new-project")
	if err != nil || detail.Stats != (DashboardProjectRow{Project: "new-project"}) {
		t.Fatalf("refreshed detail: %+v %v", detail, err)
	}
}

func TestDashboardRegistryPostgresRegistryFailureRecovery(t *testing.T) {
	cs := openDashboardRegistryStore(t)
	if err := cs.SetProjectSyncEnabled("recoverable", true, "tester", ""); err != nil {
		t.Fatal(err)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := cs.db.ExecContext(context.Background(), query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`ALTER TABLE cloud_project_controls RENAME TO registry_unavailable`)
	restored := false
	t.Cleanup(func() {
		if !restored {
			exec(`ALTER TABLE registry_unavailable RENAME TO cloud_project_controls`)
		}
	})
	for i := 0; i < 2; i++ {
		if rows, err := cs.ListProjects(""); err == nil || len(rows) != 0 {
			t.Fatalf("registry failure must not succeed/cache partial rows: %+v %v", rows, err)
		}
	}
	exec(`ALTER TABLE registry_unavailable RENAME TO cloud_project_controls`)
	restored = true
	rows, err := cs.ListProjects("")
	if err != nil || len(rows) != 1 || rows[0].Project != "recoverable" {
		t.Fatalf("recovery must retry SQL rather than cache failure: %+v %v", rows, err)
	}
}

func TestDashboardRegistryPostgresManagedContentScope(t *testing.T) {
	cs := openDashboardRegistryStore(t)
	cs.SetDashboardAllowedProjects([]string{"legacy"})
	for _, project := range []string{"legacy", "managed", "private"} {
		payload := []byte(fmt.Sprintf(`{"sessions":[{"id":"session-%[1]s","project":"%[1]s","started_at":"2026-05-04T01:45:00Z"}],"observations":[{"sync_id":"obs-%[1]s","session_id":"session-%[1]s","project":"%[1]s","type":"decision","title":"%[1]s observation","content":"content","created_at":"2026-05-04T01:49:52Z"}],"prompts":[{"sync_id":"prompt-%[1]s","session_id":"session-%[1]s","project":"%[1]s","content":"prompt","created_at":"2026-05-04T01:50:00Z"}]}`, project))
		if err := cs.WriteChunk(context.Background(), project, chunkIDFromPayload(payload), "author-"+project, "2026-05-04T01:50:00Z", payload); err != nil {
			t.Fatal(err)
		}
	}
	view, err := cs.DashboardStoreForProjects([]string{"managed"})
	if err != nil {
		t.Fatal(err)
	}
	assertDashboardPrincipalProjects(t, view, "managed")
	detail, err := view.ProjectDetail("managed")
	if err != nil || detail.Stats.Chunks != 1 || detail.Stats.Sessions != 1 || detail.Stats.Observations != 1 || detail.Stats.Prompts != 1 {
		t.Fatalf("managed SQL content: %+v %v", detail, err)
	}
	if rows := mustDashboardPrincipalObservations(t, view); len(rows) != 1 || rows[0].Project != "managed" {
		t.Fatalf("observations: %+v", rows)
	}
	if rows := mustDashboardPrincipalSessions(t, view); len(rows) != 1 || rows[0].Project != "managed" {
		t.Fatalf("sessions: %+v", rows)
	}
	if rows := mustDashboardPrincipalPrompts(t, view); len(rows) != 1 || rows[0].Project != "managed" {
		t.Fatalf("prompts: %+v", rows)
	}
	if rows := mustDashboardPrincipalContributors(t, view); len(rows) != 1 || rows[0].CreatedBy != "author-managed" {
		t.Fatalf("contributors: %+v", rows)
	}
	overview, err := view.AdminOverview()
	if err != nil || overview != (DashboardAdminOverview{Projects: 1, Contributors: 1, Chunks: 1}) {
		t.Fatalf("managed stats: %+v %v", overview, err)
	}
	if _, _, _, err := view.GetObservationDetail("managed", "session-managed", "obs-managed"); err != nil {
		t.Fatalf("managed browser detail: %v", err)
	}
	for _, denied := range []string{"legacy", "private"} {
		if _, err := view.ProjectDetail(denied); !errors.Is(err, ErrDashboardProjectForbidden) {
			t.Fatalf("ungranted project %s: %v", denied, err)
		}
		if _, _, _, err := view.GetObservationDetail(denied, "session-"+denied, "obs-"+denied); !errors.Is(err, ErrDashboardProjectForbidden) {
			t.Fatalf("ungranted browser %s: %v", denied, err)
		}
	}
	if rows, err := cs.ListProjects(""); err != nil || len(rows) != 1 || rows[0].Project != "legacy" {
		t.Fatalf("legacy inventory: %+v %v", rows, err)
	}
	if _, err := cs.ProjectDetail("managed"); !errors.Is(err, ErrDashboardProjectForbidden) {
		t.Fatalf("legacy managed exclusion: %v", err)
	}
	if rows, err := cs.ListRecentObservations("", "", 10); err != nil || len(rows) != 1 || rows[0].Project != "legacy" {
		t.Fatalf("legacy browser: %+v %v", rows, err)
	}
	if overview, err := cs.AdminOverview(); err != nil || overview != (DashboardAdminOverview{Projects: 1, Contributors: 1, Chunks: 1}) {
		t.Fatalf("legacy stats: %+v %v", overview, err)
	}
	empty, err := cs.DashboardStoreForProjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := empty.ListProjects(""); err != nil || len(rows) != 0 {
		t.Fatalf("no grants inventory: %+v %v", rows, err)
	}
	if len(mustDashboardPrincipalObservations(t, empty))+len(mustDashboardPrincipalSessions(t, empty))+len(mustDashboardPrincipalPrompts(t, empty))+len(mustDashboardPrincipalContributors(t, empty)) != 0 {
		t.Fatal("no grants leaked browser rows")
	}
	if overview, err := empty.AdminOverview(); err != nil || overview != (DashboardAdminOverview{}) {
		t.Fatalf("no grants stats: %+v %v", overview, err)
	}
	if _, err := empty.ProjectDetail("managed"); !errors.Is(err, ErrDashboardProjectForbidden) {
		t.Fatalf("no grants detail: %v", err)
	}
}
