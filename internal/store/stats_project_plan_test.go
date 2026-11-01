package store

import (
	"strings"
	"testing"
)

// Project-scoped statistics must seek by project instead of scanning unrelated
// sessions or prompts, including legacy mixed-case project values.
func TestStatsProjectCountQueriesSeekByProject(t *testing.T) {
	s := newTestStore(t)
	for _, table := range []string{"sessions", "user_prompts"} {
		t.Run(table, func(t *testing.T) {
			rows, err := s.DB().Query("EXPLAIN QUERY PLAN SELECT COUNT(*) FROM "+table+" WHERE LOWER(project) = ?", "engram")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := rows.Close(); err != nil {
					t.Error(err)
				}
			}()
			seek := false
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(detail, "SEARCH "+table) {
					seek = true
				}
				if strings.Contains(detail, "SCAN "+table) {
					t.Errorf("scoped count scans unrelated rows: %s", detail)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !seek {
				t.Error("scoped count has no project seek")
			}
		})
	}
}
