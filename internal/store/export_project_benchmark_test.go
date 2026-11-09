package store

import (
	"database/sql"
	"fmt"
	"testing"
)

// BenchmarkExportProjectDuplicateHeavy keeps one target project's 1 session,
// 10 observations, and 1 prompt fixed while unrelated rows grow across projects.
// The sub-benchmark series records the export scaling slope in standard output.
func BenchmarkExportProjectDuplicateHeavy(b *testing.B) {
	for _, unrelated := range []int{1_000, 25_000} {
		b.Run(fmt.Sprintf("%d-unrelated", unrelated), func(b *testing.B) {
			s := benchmarkExportProjectStore(b, unrelated)
			b.Cleanup(func() { _ = s.Close() })
			b.ReportMetric(float64(unrelated), "unrelated_rows")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.ExportProject("target"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchmarkExportProjectStore(b *testing.B, unrelated int) *Store {
	b.Helper()
	cfg, err := DefaultConfig()
	if err != nil {
		b.Fatal(err)
	}
	cfg.DataDir = b.TempDir()
	s, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	if err := s.CreateSession("target-session", "target", "/tmp/target"); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := s.AddObservation(AddObservationParams{SessionID: "target-session", Type: "note", Title: fmt.Sprintf("target-%d", i), Content: "target", Project: "target", Scope: "project"}); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "target-session", Content: "target", Project: "target"}); err != nil {
		b.Fatal(err)
	}
	if err := s.withTx(func(tx *sql.Tx) error {
		for i := 0; i < unrelated; i++ {
			project, sessionID := fmt.Sprintf("noise-%02d", i%25), fmt.Sprintf("noise-session-%06d", i)
			if _, err := tx.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, '/tmp/noise')`, sessionID, project); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope) VALUES (?, ?, 'note', 'noise', 'noise', ?, 'project')`, fmt.Sprintf("noise-observation-%06d", i), sessionID, project); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, 'noise', ?)`, fmt.Sprintf("noise-prompt-%06d", i), sessionID, project); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	return s
}
