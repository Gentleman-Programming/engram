package store

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkEnrolledProjectRepairDetector(b *testing.B) {
	for _, dimensions := range []struct {
		name         string
		enrolled     int
		observations int
	}{
		{name: "E8/O128", enrolled: 8, observations: 128},
	} {
		b.Run(dimensions.name+"/missing-mutations", func(b *testing.B) {
			s := benchmarkRepairDetectorStore(b, dimensions.enrolled, dimensions.observations)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				projects, err := s.enrolledProjectsNeedingBackfill()
				if err != nil {
					b.Fatal(err)
				}
				if len(projects) != dimensions.enrolled {
					b.Fatalf("set-based detector found %d projects, want %d", len(projects), dimensions.enrolled)
				}
			}
		})

		b.Run(dimensions.name+"/fully-repaired", func(b *testing.B) {
			s := benchmarkRepairDetectorStore(b, dimensions.enrolled, dimensions.observations)
			if err := s.repairEnrolledProjectSyncMutations(); err != nil {
				b.Fatal(err)
			}
			projects, err := s.enrolledProjectsNeedingBackfill()
			if err != nil {
				b.Fatal(err)
			}
			if len(projects) != 0 {
				b.Fatalf("fully repaired detector found %d projects, want 0", len(projects))
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				projects, err := s.enrolledProjectsNeedingBackfill()
				if err != nil {
					b.Fatal(err)
				}
				if len(projects) != 0 {
					b.Fatalf("fully repaired detector found %d projects, want 0", len(projects))
				}
			}
		})

		b.Run(dimensions.name+"/historical-per-project", func(b *testing.B) {
			s := benchmarkRepairDetectorStore(b, dimensions.enrolled, dimensions.observations)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				found := 0
				for projectIndex := 0; projectIndex < dimensions.enrolled; projectIndex++ {
					needs, err := s.projectNeedsBackfill(fmt.Sprintf("repair-858-%d", projectIndex))
					if err != nil {
						b.Fatal(err)
					}
					if needs {
						found++
					}
				}
				if found != dimensions.enrolled {
					b.Fatalf("historical detector found %d projects, want %d", found, dimensions.enrolled)
				}
			}
		})
	}
}

func benchmarkRepairDetectorStore(b *testing.B, enrolled, observations int) *Store {
	b.Helper()
	cfg, err := DefaultConfig()
	if err != nil {
		b.Fatal(err)
	}
	cfg.DataDir = b.TempDir()
	cfg.DedupeWindow = time.Hour
	s, err := newWithoutRepair(cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	for projectIndex := 0; projectIndex < enrolled; projectIndex++ {
		project := fmt.Sprintf("repair-858-%d", projectIndex)
		sessionID := fmt.Sprintf("repair-858-session-%d", projectIndex)
		if _, err := s.db.Exec(`INSERT INTO sync_enrolled_projects (project) VALUES (?)`, project); err != nil {
			b.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory, started_at) VALUES (?, ?, ?, datetime('now'))`, sessionID, project, "/tmp/repair-858"); err != nil {
			b.Fatal(err)
		}
	}
	for observationIndex := 0; observationIndex < observations; observationIndex++ {
		projectIndex := observationIndex % enrolled
		project := fmt.Sprintf("repair-858-%d", projectIndex)
		sessionID := fmt.Sprintf("repair-858-session-%d", projectIndex)
		if _, err := s.db.Exec(`
			INSERT INTO observations (session_id, type, title, content, project, scope, normalized_hash,
				revision_count, duplicate_count, last_seen_at, updated_at, sync_id)
			VALUES (?, 'decision', ?, 'content', ?, 'project', ?, 1, 1, datetime('now'), datetime('now'), ?)`,
			sessionID, fmt.Sprintf("observation-%d", observationIndex), project, hashNormalized(fmt.Sprintf("content-%d", observationIndex)), fmt.Sprintf("repair-858-observation-%d", observationIndex)); err != nil {
			b.Fatal(err)
		}
	}
	return s
}
