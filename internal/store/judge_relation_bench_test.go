package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const judgeRelationBenchmarkProject = "judge-benchmark"

type judgeRelationBenchmarkFixture struct {
	store *Store
	ids   []string
}

func newJudgeRelationBenchmarkFixture(b *testing.B, enrolled bool) *judgeRelationBenchmarkFixture {
	b.Helper()
	cfg, err := DefaultConfig()
	if err != nil {
		b.Fatalf("default store config: %v", err)
	}
	cfg.DataDir = b.TempDir()
	s, err := New(cfg)
	if err != nil {
		b.Fatalf("create benchmark store: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	if err := s.CreateSession("judge-benchmark-session", judgeRelationBenchmarkProject, cfg.DataDir); err != nil {
		b.Fatalf("create benchmark session: %v", err)
	}
	add := func(label string) string {
		id, err := s.AddObservation(AddObservationParams{SessionID: "judge-benchmark-session", Type: "benchmark", Title: "judge relation benchmark " + label, Content: "benchmark fixture", Project: judgeRelationBenchmarkProject, Scope: "project"})
		if err != nil {
			b.Fatalf("add benchmark %s observation: %v", label, err)
		}
		observation, err := s.GetObservation(id)
		if err != nil {
			b.Fatalf("get benchmark %s observation: %v", label, err)
		}
		return observation.SyncID
	}
	sourceID, targetID := add("source"), add("target")
	ids := make([]string, 3)
	for i := range ids {
		ids[i] = fmt.Sprintf("rel-judge-benchmark-%d", i+1)
		if _, err := s.SaveRelation(SaveRelationParams{SyncID: ids[i], SourceID: sourceID, TargetID: targetID}); err != nil {
			b.Fatalf("seed benchmark relation %d: %v", i+1, err)
		}
	}
	if enrolled {
		if err := s.EnrollProject(judgeRelationBenchmarkProject); err != nil {
			b.Fatalf("enroll benchmark project: %v", err)
		}
	}
	return &judgeRelationBenchmarkFixture{store: s, ids: ids}
}

func judgeRelationBenchmarkParams(id string) JudgeRelationParams {
	confidence := 0.9
	return JudgeRelationParams{JudgmentID: id, Relation: "not_conflict", Confidence: &confidence, MarkedByActor: "benchmark", MarkedByKind: "agent"}
}

func benchmarkJudgeRelationCalls(b *testing.B, enrolled bool, calls int, concurrent bool) {
	fixture := newJudgeRelationBenchmarkFixture(b, enrolled)
	judge := func(id string) error {
		_, err := fixture.store.JudgeRelation(judgeRelationBenchmarkParams(id))
		return err
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !concurrent {
			for _, id := range fixture.ids[:calls] {
				if err := judge(id); err != nil {
					b.Fatal(err)
				}
			}
			continue
		}
		b.StopTimer()
		start, errs := make(chan struct{}), make(chan error, calls)
		var ready sync.WaitGroup
		ready.Add(calls)
		for _, id := range fixture.ids[:calls] {
			go func(id string) { ready.Done(); <-start; errs <- judge(id) }(id)
		}
		ready.Wait()
		b.StartTimer()
		close(start)
		for range calls {
			if err := <-errs; err != nil {
				b.StopTimer()
				b.Fatal(err)
			}
		}
		b.StopTimer()
	}
}

func benchmarkJudgeRelationExternalSQLiteLock(b *testing.B) {
	fixture := newJudgeRelationBenchmarkFixture(b, true)
	if _, err := fixture.store.DB().Exec("PRAGMA busy_timeout = 0"); err != nil {
		b.Fatalf("disable Store SQLite busy timeout: %v", err)
	}
	originalBackoffs := sqliteWriteRetryBackoffs
	sqliteWriteRetryBackoffs = make([]time.Duration, len(originalBackoffs))
	b.Cleanup(func() { sqliteWriteRetryBackoffs = originalBackoffs })
	external, err := sql.Open("sqlite", filepath.Join(fixture.store.DataDir(), "engram.db"))
	if err != nil {
		b.Fatalf("open external SQLite handle: %v", err)
	}
	external.SetMaxOpenConns(1)
	b.Cleanup(func() { _ = external.Close() })
	const hold = 25 * time.Millisecond
	originalExec := fixture.store.hooks.exec
	var blocked chan<- struct{}
	var retry <-chan struct{}
	fixture.store.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		result, err := originalExec(db, query, args...)
		if strings.Contains(query, "UPDATE memory_relations") && isRetryableSQLiteLockError(err) {
			blocked <- struct{}{}
			<-retry
		}
		return result, err
	}
	b.Cleanup(func() { fixture.store.hooks.exec = originalExec })
	b.ReportAllocs()
	b.ReportMetric(float64(hold/time.Millisecond), "lock-hold-ms")
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		if _, err := external.Exec("BEGIN IMMEDIATE"); err != nil {
			b.Fatalf("acquire external SQLite lock: %v", err)
		}
		start, done := make(chan struct{}), make(chan error, 1)
		updateBlocked, retryUpdate := make(chan struct{}), make(chan struct{})
		blocked, retry = updateBlocked, retryUpdate
		go func() {
			<-start
			_, err := fixture.store.JudgeRelation(judgeRelationBenchmarkParams(fixture.ids[0]))
			done <- err
		}()
		b.StartTimer()
		close(start)
		select {
		case <-updateBlocked:
		case <-time.After(time.Second):
			b.StopTimer()
			_, _ = external.Exec("ROLLBACK")
			close(retryUpdate)
			b.Fatal("Store UPDATE did not report a SQLite lock")
		}
		time.Sleep(hold)
		if _, err := external.Exec("COMMIT"); err != nil {
			b.StopTimer()
			_, _ = external.Exec("ROLLBACK")
			close(retryUpdate)
			b.Fatal(err)
		}
		close(retryUpdate)
		if err := <-done; err != nil {
			b.StopTimer()
			b.Fatal(err)
		}
		b.StopTimer()
	}
}

// BenchmarkJudgeRelation attributes Store-only work with seeded relations.
func BenchmarkJudgeRelation(b *testing.B) {
	for _, scenario := range []struct {
		name                 string
		enrolled, concurrent bool
		calls                int
	}{
		{"One/Unenrolled", false, false, 1}, {"One/Enrolled", true, false, 1},
		{"SequentialThree/Unenrolled", false, false, 3}, {"SequentialThree/Enrolled", true, false, 3},
		{"ConcurrentThree/Unenrolled", false, true, 3}, {"ConcurrentThree/Enrolled", true, true, 3},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			benchmarkJudgeRelationCalls(b, scenario.enrolled, scenario.calls, scenario.concurrent)
		})
	}
	b.Run("ExternalSQLiteLock/Enrolled", benchmarkJudgeRelationExternalSQLiteLock)
}
