package mcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// Real SQLite and the complete MCP save handler. A stable topic keeps the
// observation corpus bounded; neither candidate discovery nor fencing is stubbed.
func BenchmarkSaveExistingProjectWarning(b *testing.B) {
	for _, projects := range []int{10, 500} {
		b.Run(fmt.Sprintf("rows5000_projects%d", projects), func(b *testing.B) {
			dir := b.TempDir()
			b.Chdir(dir)
			cfg, err := store.DefaultConfig()
			if err != nil {
				b.Fatal(err)
			}
			cfg.DataDir = b.TempDir()
			s, err := store.New(cfg)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = s.Close() })
			_, err = s.DB().Exec(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x+1<?)
			INSERT INTO sessions(id,project,directory) SELECT 'bench-'||x,printf('p%03d',x),? FROM n`, projects, dir)
			if err != nil {
				b.Fatal(err)
			}
			_, err = s.DB().Exec(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x+1<5000)
			INSERT INTO observations(sync_id,session_id,type,title,content,project,scope,normalized_hash)
			SELECT 'bench-observation-'||x,'bench-'||(x%?),'decision','unrelated corpus '||x,'synthetic corpus content '||x,printf('p%03d',x%?),'project','hash-'||x FROM n`, projects, projects)
			if err != nil {
				b.Fatal(err)
			}
			h := handleSave(s, MCPConfig{DefaultProject: "p000"}, NewSessionActivity(0))
			req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"session_id": "bench-0", "title": "benchmark target", "content": "synthetic benchmark target", "topic_key": "benchmark/target", "capture_prompt": false}}}
			invoke := func() {
				result, err := h(context.Background(), req)
				if err != nil || result == nil || result.IsError {
					b.Fatalf("save err=%v result=%+v", err, result)
				}
			}
			invoke()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				invoke()
			}
		})
	}
}
