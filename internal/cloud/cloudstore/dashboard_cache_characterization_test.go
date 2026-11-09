package cloudstore

import (
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// These are loader-interleaving characterizations, not PostgreSQL incidence or
// performance evidence. The existing seam supplies fixtures; assertions use public reads.
func characterizationEqual(t *testing.T, got, want any, err error) {
	t.Helper()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v; error=%v", got, want, err)
	}
}

func characterizationWait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("controlled loader/read did not reach barrier")
		var zero T
		return zero
	}
}

type characterizationResult struct {
	rows []DashboardProjectRow
	err  error
}

func characterizationRead(cs *CloudStore, results chan<- characterizationResult) {
	rows, err := cs.ListProjects("")
	results <- characterizationResult{rows, err}
}

func TestDashboardCacheCharacterizationWarmPublicSurfaces(t *testing.T) {
	model := dashboardPrincipalReadModel()
	calls := 0
	cs := &CloudStore{dashboardReadModelLoad: func() (dashboardReadModel, error) { calls++; return model, nil }}
	rows, err := cs.ListProjects("")
	characterizationEqual(t, rows, model.projects, err)
	contributors, err := cs.ListContributors("alice")
	characterizationEqual(t, contributors, model.projectDetails["project-a"].Contributors, err)
	detail, err := cs.ProjectDetail("project-a")
	characterizationEqual(t, detail, model.projectDetails["project-a"], err)
	sessions, err := cs.ListRecentSessions("project-a", "", 10)
	characterizationEqual(t, sessions, detail.Sessions, err)
	observations, err := cs.ListRecentObservations("project-a", "", 10)
	characterizationEqual(t, observations, detail.Observations, err)
	prompts, err := cs.ListRecentPrompts("project-a", "", 10)
	characterizationEqual(t, prompts, detail.Prompts, err)
	overview, err := cs.AdminOverview()
	characterizationEqual(t, overview, model.admin, err)
	if calls != 1 {
		t.Fatalf("loads=%d, want 1", calls)
	}
	cs.invalidateDashboardReadModel()
	rows, err = cs.ListProjects("")
	characterizationEqual(t, rows, model.projects, err)
	if calls != 2 {
		t.Fatalf("loads after invalidation=%d, want 2", calls)
	}
}

func TestDashboardCacheCharacterizationConcurrentColdCallers(t *testing.T) {
	model := dashboardPrincipalReadModel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	defer close(release)
	var calls atomic.Int32
	cs := &CloudStore{dashboardReadModelLoad: func() (dashboardReadModel, error) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			return model, nil
		case <-time.After(2 * time.Second):
			return dashboardReadModel{}, errors.New("loader barrier timeout")
		}
	}}
	results := make(chan characterizationResult, 2)
	go characterizationRead(cs, results)
	characterizationWait(t, entered)
	go characterizationRead(cs, results)
	waitForDashboardBuildWaiters(t, cs, 1)
	// The second cold read joins the in-flight build instead of entering the loader.
	if calls.Load() != 1 {
		t.Fatalf("concurrent cold reads must share one loader call, loads=%d", calls.Load())
	}
	// Send a release rather than closing: deferred close also unblocks failure paths.
	release <- struct{}{}
	for i := 0; i < 2; i++ {
		result := characterizationWait(t, results)
		characterizationEqual(t, result.rows, model.projects, result.err)
	}
	rows, err := cs.ListProjects("")
	characterizationEqual(t, rows, model.projects, err)
	if calls.Load() != 1 {
		t.Fatalf("warm follow-up rebuilt: %d", calls.Load())
	}
}

func TestDashboardCacheCharacterizationInvalidationDuringLoad(t *testing.T) {
	old := dashboardPrincipalReadModel()
	next := dashboardPrincipalReadModel()
	next.projects[0].Chunks = 9
	next.projectDetails["project-a"] = DashboardProjectDetail{Project: "project-a", Stats: next.projects[0]}
	entered := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	defer close(release)
	var calls atomic.Int32
	cs := &CloudStore{dashboardReadModelLoad: func() (dashboardReadModel, error) {
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			select {
			case <-release:
				return old, nil
			case <-time.After(2 * time.Second):
				return dashboardReadModel{}, errors.New("loader barrier timeout")
			}
		}
		return next, nil
	}}
	results := make(chan characterizationResult, 1)
	go characterizationRead(cs, results)
	characterizationWait(t, entered)
	cs.invalidateDashboardReadModel()
	release <- struct{}{}
	result := characterizationWait(t, results)
	characterizationEqual(t, result.rows, old.projects, result.err)
	// A build that started before an invalidation is returned to its callers but
	// never published, so the next public read rebuilds.
	detail, err := cs.ProjectDetail("project-a")
	characterizationEqual(t, detail, next.projectDetails["project-a"], err)
	if calls.Load() != 2 {
		t.Fatalf("stale build was published: loads=%d, want 2", calls.Load())
	}
	cs.invalidateDashboardReadModel()
	rows, err := cs.ListProjects("")
	characterizationEqual(t, rows, next.projects, err)
	rows, err = cs.ListProjects("")
	characterizationEqual(t, rows, next.projects, err)
	if calls.Load() != 3 {
		t.Fatalf("next invalidation/reuse loads=%d, want 3", calls.Load())
	}
}

func TestDashboardCacheCharacterizationBuilderErrorNotCached(t *testing.T) {
	model := dashboardPrincipalReadModel()
	failure := errors.New("controlled build failure")
	calls := 0
	cs := &CloudStore{dashboardReadModelLoad: func() (dashboardReadModel, error) {
		calls++
		if calls == 1 {
			return dashboardReadModel{}, failure
		}
		return model, nil
	}}
	rows, err := cs.ListProjects("")
	if !errors.Is(err, failure) || len(rows) != 0 {
		t.Fatalf("failed read rows=%+v error=%v", rows, err)
	}
	rows, err = cs.ListProjects("")
	characterizationEqual(t, rows, model.projects, err)
	overview, err := cs.AdminOverview()
	characterizationEqual(t, overview, model.admin, err)
	if calls != 2 {
		t.Fatalf("error retry and warm reuse loads=%d, want 2", calls)
	}
}

func TestDashboardCacheCharacterizationLegacyManagedNeutralReuse(t *testing.T) {
	model := dashboardPrincipalReadModel()
	calls := 0
	cs := &CloudStore{dashboardReadModelLoad: func() (dashboardReadModel, error) { calls++; return model, nil }}
	cs.SetDashboardAllowedProjects([]string{"project-a"})
	managed, err := cs.DashboardStoreForProjects([]string{"project-b"})
	if err != nil {
		t.Fatal(err)
	}
	legacyRows, err := cs.ListProjects("")
	characterizationEqual(t, legacyRows, []DashboardProjectRow{model.projects[0]}, err)
	managedRows, err := managed.ListProjects("")
	characterizationEqual(t, managedRows, []DashboardProjectRow{model.projects[1]}, err)
	if _, err := cs.ProjectDetail("project-b"); !errors.Is(err, ErrDashboardProjectForbidden) {
		t.Fatalf("legacy exclusion: %v", err)
	}
	if _, err := managed.ProjectDetail("project-a"); !errors.Is(err, ErrDashboardProjectForbidden) {
		t.Fatalf("managed exclusion: %v", err)
	}
	second, err := cs.DashboardStoreForProjects([]string{"project-a"})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := second.ProjectDetail("project-a")
	characterizationEqual(t, detail, model.projectDetails["project-a"], err)
	managedRows, err = managed.ListProjects("")
	characterizationEqual(t, managedRows, []DashboardProjectRow{model.projects[1]}, err)
	legacyRows, err = cs.ListProjects("")
	characterizationEqual(t, legacyRows, []DashboardProjectRow{model.projects[0]}, err)
	if calls != 1 {
		t.Fatalf("credential views should reuse neutral cache, loads=%d", calls)
	}
}
