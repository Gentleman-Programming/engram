package cloudstore

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// blockingDashboardLoader returns a loader hook that reports each entry and
// blocks until released, so tests can hold a build in flight deterministically.
func blockingDashboardLoader(calls *atomic.Int32, entered chan<- int32, release <-chan struct{}, result func(call int32) (dashboardReadModel, error)) func(context.Context) (dashboardReadModel, error) {
	return func(ctx context.Context) (dashboardReadModel, error) {
		call := calls.Add(1)
		entered <- call
		select {
		case <-release:
			return result(call)
		case <-ctx.Done():
			return dashboardReadModel{}, ctx.Err()
		case <-time.After(5 * time.Second):
			return dashboardReadModel{}, errors.New("loader barrier timeout")
		}
	}
}

// waitForDashboardBuildWaiters blocks until want callers are attached to the in-flight build.
func waitForDashboardBuildWaiters(tb testing.TB, cs *CloudStore, want int32) {
	tb.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cs.dashboardReadModelMu.RLock()
		build := cs.dashboardReadModelBuild
		cs.dashboardReadModelMu.RUnlock()
		if build != nil && build.waiters.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	tb.Fatalf("in-flight build did not reach %d waiters", want)
}

func TestDashboardReadModelConcurrentColdReadersShareOneBuild(t *testing.T) {
	const readers = 8
	model := dashboardPrincipalReadModel()
	var calls atomic.Int32
	entered := make(chan int32, readers)
	release := make(chan struct{})
	cs := &CloudStore{dashboardReadModelLoad: blockingDashboardLoader(&calls, entered, release, func(int32) (dashboardReadModel, error) {
		return model, nil
	})}

	results := make(chan characterizationResult, readers)
	for i := 0; i < readers; i++ {
		go characterizationRead(cs, results)
	}
	characterizationWait(t, entered)
	waitForDashboardBuildWaiters(t, cs, readers-1)
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent cold readers started %d builds, want 1", got)
	}
	close(release)
	for i := 0; i < readers; i++ {
		result := characterizationWait(t, results)
		characterizationEqual(t, result.rows, model.projects, result.err)
	}
	rows, err := cs.ListProjects("")
	characterizationEqual(t, rows, model.projects, err)
	if got := calls.Load(); got != 1 {
		t.Fatalf("builds after warm follow-up=%d, want 1", got)
	}
}

func TestDashboardReadModelInvalidationDuringBuildIsNotPublished(t *testing.T) {
	old := dashboardPrincipalReadModel()
	next := dashboardPrincipalReadModel()
	next.projects[0].Chunks = 9
	next.projectDetails["project-a"] = DashboardProjectDetail{Project: "project-a", Stats: next.projects[0]}

	var calls atomic.Int32
	entered := make(chan int32, 4)
	release := make(chan struct{}, 4)
	cs := &CloudStore{dashboardReadModelLoad: blockingDashboardLoader(&calls, entered, release, func(call int32) (dashboardReadModel, error) {
		if call == 1 {
			return old, nil
		}
		return next, nil
	})}

	staleResults := make(chan characterizationResult, 2)
	go characterizationRead(cs, staleResults)
	characterizationWait(t, entered)
	go characterizationRead(cs, staleResults)
	waitForDashboardBuildWaiters(t, cs, 1)

	cs.invalidateDashboardReadModel()

	// A caller arriving after invalidation must not join the stale build.
	freshResults := make(chan characterizationResult, 1)
	go characterizationRead(cs, freshResults)
	if call := characterizationWait(t, entered); call != 2 {
		t.Fatalf("post-invalidation caller entered build %d, want 2", call)
	}

	release <- struct{}{}
	release <- struct{}{}
	for i := 0; i < 2; i++ {
		result := characterizationWait(t, staleResults)
		characterizationEqual(t, result.rows, old.projects, result.err)
	}
	fresh := characterizationWait(t, freshResults)
	characterizationEqual(t, fresh.rows, next.projects, fresh.err)

	detail, err := cs.ProjectDetail("project-a")
	characterizationEqual(t, detail, next.projectDetails["project-a"], err)
	if got := calls.Load(); got != 2 {
		t.Fatalf("builds=%d, want 2 (stale build must not be published)", got)
	}
}

func TestDashboardReadModelInvalidationDuringSoleBuildForcesRebuild(t *testing.T) {
	old := dashboardPrincipalReadModel()
	next := dashboardPrincipalReadModel()
	next.projects[0].Chunks = 9
	next.projectDetails["project-a"] = DashboardProjectDetail{Project: "project-a", Stats: next.projects[0]}

	var calls atomic.Int32
	entered := make(chan int32, 1)
	release := make(chan struct{}, 1)
	defer close(release)
	cs := &CloudStore{dashboardReadModelLoad: blockingDashboardLoader(&calls, entered, release, func(call int32) (dashboardReadModel, error) {
		return old, nil
	})}

	results := make(chan characterizationResult, 1)
	go characterizationRead(cs, results)
	characterizationWait(t, entered)
	cs.invalidateDashboardReadModel()
	release <- struct{}{}
	result := characterizationWait(t, results)
	characterizationEqual(t, result.rows, old.projects, result.err)

	cs.dashboardReadModelLoad = func(context.Context) (dashboardReadModel, error) {
		calls.Add(1)
		return next, nil
	}
	rows, err := cs.ListProjects("")
	characterizationEqual(t, rows, next.projects, err)
	if got := calls.Load(); got != 2 {
		t.Fatalf("builds=%d, want 2 (next read must rebuild)", got)
	}
}

func TestDashboardReadModelBuildErrorReachesAllWaitersAndIsNotCached(t *testing.T) {
	const readers = 4
	model := dashboardPrincipalReadModel()
	failure := errors.New("controlled build failure")
	var calls atomic.Int32
	entered := make(chan int32, readers)
	release := make(chan struct{})
	cs := &CloudStore{dashboardReadModelLoad: blockingDashboardLoader(&calls, entered, release, func(call int32) (dashboardReadModel, error) {
		if call == 1 {
			return dashboardReadModel{}, failure
		}
		return model, nil
	})}

	results := make(chan characterizationResult, readers)
	for i := 0; i < readers; i++ {
		go characterizationRead(cs, results)
	}
	characterizationWait(t, entered)
	waitForDashboardBuildWaiters(t, cs, readers-1)
	close(release)
	for i := 0; i < readers; i++ {
		result := characterizationWait(t, results)
		if !errors.Is(result.err, failure) || len(result.rows) != 0 {
			t.Fatalf("waiter %d rows=%+v err=%v, want shared failure", i, result.rows, result.err)
		}
	}
	rows, err := cs.ListProjects("")
	characterizationEqual(t, rows, model.projects, err)
	if got := calls.Load(); got != 2 {
		t.Fatalf("builds=%d, want 2 (error must not be cached)", got)
	}
}

func TestDashboardReadModelPanickingBuildReleasesWaiters(t *testing.T) {
	model := dashboardPrincipalReadModel()
	var calls atomic.Int32
	entered := make(chan int32, 2)
	release := make(chan struct{})
	cs := &CloudStore{dashboardReadModelLoad: func(context.Context) (dashboardReadModel, error) {
		call := calls.Add(1)
		if call == 1 {
			entered <- call
			<-release
			panic("controlled build panic")
		}
		return model, nil
	}}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { _ = recover() }()
		_, _ = cs.ListProjects("")
	}()
	characterizationWait(t, entered)
	waiter := make(chan characterizationResult, 1)
	go characterizationRead(cs, waiter)
	waitForDashboardBuildWaiters(t, cs, 1)
	close(release)
	wg.Wait()
	result := characterizationWait(t, waiter)
	if result.err == nil {
		t.Fatalf("waiter of a panicking build must receive an error, got rows=%+v", result.rows)
	}
	rows, err := cs.ListProjects("")
	characterizationEqual(t, rows, model.projects, err)
}

func TestDashboardReadModelCanceledReaderLeavesSharedBuildRunning(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		startCanceledReaderFirst bool
	}{
		{name: "leader", startCanceledReaderFirst: true},
		{name: "follower"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				entered := make(chan int32, 2)
				release := make(chan struct{})
				model := dashboardPrincipalReadModel()
				cs := &CloudStore{dashboardReadModelLoad: blockingDashboardLoader(&calls, entered, release, func(int32) (dashboardReadModel, error) {
					return model, nil
				})}

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				canceled := make(chan characterizationResult, 1)
				survivor := make(chan characterizationResult, 1)
				read := func(ctx context.Context, project string, result chan<- characterizationResult) {
					view, err := cs.DashboardStoreForProjectsContext(ctx, []string{project})
					if err != nil {
						result <- characterizationResult{err: err}
						return
					}
					rows, err := view.ListProjects("")
					result <- characterizationResult{rows: rows, err: err}
				}
				if tc.startCanceledReaderFirst {
					go read(ctx, "project-a", canceled)
					characterizationWait(t, entered)
					go read(context.Background(), "project-b", survivor)
				} else {
					go read(context.Background(), "project-b", survivor)
					characterizationWait(t, entered)
					go read(ctx, "project-a", canceled)
				}
				synctest.Wait()
				cancel()
				synctest.Wait()
				select {
				case result := <-canceled:
					if !errors.Is(result.err, context.Canceled) {
						t.Errorf("canceled reader returned %v", result.err)
					}
				default:
					t.Error("canceled reader remained attached to the shared build")
				}
				select {
				case result := <-survivor:
					t.Errorf("surviving reader returned before release: %+v", result)
					close(release)
					return
				default:
				}

				close(release)
				result := characterizationWait(t, survivor)
				characterizationEqual(t, result.rows, model.projects[1:], result.err)
				if got := calls.Load(); got != 1 {
					t.Fatalf("loader ran %d times, want 1", got)
				}
			})
		})
	}
}

func TestDashboardReadModelTimedOutBuildCanBeRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		model := dashboardPrincipalReadModel()
		cs := &CloudStore{dashboardReadModelLoad: func(ctx context.Context) (dashboardReadModel, error) {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				return dashboardReadModel{}, ctx.Err()
			}
			return model, nil
		}}

		start := time.Now()
		if _, err := cs.ListProjects(""); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("first read error = %v, want build deadline", err)
		}
		if elapsed := time.Since(start); elapsed != dashboardReadModelBuildTimeout {
			t.Fatalf("shared build waited %s, want %s", elapsed, dashboardReadModelBuildTimeout)
		}
		synctest.Wait()
		for range 2 {
			rows, err := cs.ListProjects("")
			characterizationEqual(t, rows, model.projects, err)
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("loader ran %d times, want one failed load and one successful rebuild", got)
		}
	})
}

func TestDashboardReadModelLateSuccessAfterDeadlineIsNotCached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		model := dashboardPrincipalReadModel()
		cs := &CloudStore{dashboardReadModelLoad: func(context.Context) (dashboardReadModel, error) {
			if calls.Add(1) == 1 {
				<-release
			}
			return model, nil
		}}

		read := make(chan error, 1)
		go func() {
			_, err := cs.ListProjects("")
			read <- err
		}()
		select {
		case err := <-read:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("stalled read error = %v, want build deadline", err)
			}
		case <-time.After(dashboardReadModelBuildTimeout + time.Second):
			t.Error("deadline did not release reader while the loader was stalled")
		}
		close(release)
		synctest.Wait()

		rows, err := cs.ListProjects("")
		characterizationEqual(t, rows, model.projects, err)
		if got := calls.Load(); got != 2 {
			t.Fatalf("loader ran %d times, want rebuild after a late success", got)
		}
	})
}
