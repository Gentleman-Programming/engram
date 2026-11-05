package mcp

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func summaryActivity() *SessionActivity {
	a := NewSessionActivity(10 * time.Minute)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	for i := 0; i < 6; i++ {
		a.RecordToolCall(defaultSessionID("p000"))
	}
	now = now.Add(20 * time.Minute)
	return a
}

func TestMemContextProjectSummaryMatchesFullStats(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("summary", "p000", ""); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for i := 0; i < 100; i++ {
		if _, err = tx.Exec(`INSERT INTO observations(session_id,project,scope,type,title,content,created_at) VALUES('summary',?,'personal','decision','title 界','body 🧠',?)`, fmt.Sprintf("p%03d", i), fmt.Sprintf("2026-01-%02d", 1+i%4)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	original := loadContextStats
	defer func() { loadContextStats = original }()
	full := func(s *store.Store, project string) (*store.Stats, int, error) {
		var stats *store.Stats
		var e error
		if project == "" {
			stats, e = s.Stats()
		} else {
			stats, e = s.StatsProject(project)
		}
		if e != nil {
			return nil, 0, e
		}
		return stats, len(stats.Projects), nil
	}
	for i, args := range []map[string]any{{}, {"project": " P000 "}, {"scope": "personal"}, {"scope": "personal", "compact": true}, {"scope": "personal", "max_bytes": 256}, {"scope": "personal", "max_bytes": 1}, {"scope": "personal", "older_history": true}} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}}
			loadContextStats = full
			want, e1 := handleContext(s, MCPConfig{DefaultProject: "p000"}, summaryActivity())(context.Background(), req)
			loadContextStats = original
			got, e2 := handleContext(s, MCPConfig{DefaultProject: "p000"}, summaryActivity())(context.Background(), req)
			if fmt.Sprint(e1) != fmt.Sprint(e2) || !reflect.DeepEqual(want, got) {
				t.Fatalf("context output changed for %v: %#v/%#v", args, want, got)
			}
		})
	}
}
