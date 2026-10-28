package mcp

import (
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOlderHistoryMCPRealRendering(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("real-history", "alpha", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		project := "alpha"
		if i%2 == 0 {
			project = "beta"
		}
		if _, err := s.DB().Exec(`INSERT INTO observations(session_id,type,title,content,project,scope,created_at) VALUES('real-history','decision','past','body',?,'personal','2022-01-01')`, project); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"scope": "personal", "max_bytes": float64(4096)}
	absent := budgetCallContext(t, s, args)
	args["older_history"] = false
	disabled := budgetCallContext(t, s, args)
	if absent != disabled || strings.Contains(absent, "Older History") {
		t.Fatal("disabled output changed")
	}
	args["older_history"] = true
	enabled := budgetCallContext(t, s, args)
	if !strings.Contains(enabled, "2022: 5 observations") || len(enabled) > 4096 || !utf8.ValidString(enabled) {
		t.Fatal(enabled)
	}
	part := budgetContextPart(t, enabled)
	index := strings.Index(part, "### Older History")
	if index < 0 {
		t.Fatal(enabled)
	}
	if len(part[index:]) > min(2048, (4096-(len(enabled)-len(part)))/4) {
		t.Fatal("suffix not reserved")
	}
	after, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	before.ExportedAt = ""
	after.ExportedAt = ""
	if !reflect.DeepEqual(before, after) {
		t.Fatal("context mutated store")
	}
}

func TestOlderHistorySchema(t *testing.T) {
	tool := NewServer(newMCPTestStore(t)).GetTool("mem_context")
	prop, ok := tool.Tool.InputSchema.Properties["older_history"].(map[string]any)
	if !ok || prop["type"] != "boolean" {
		t.Fatalf("older_history must be an optional boolean: %v", prop)
	}
	for _, name := range tool.Tool.InputSchema.Required {
		if name == "older_history" {
			t.Fatal("older_history must remain optional")
		}
	}
}

func TestOlderHistoryForwarding(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("history", "engram", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	old := contextHistoryFormatter
	t.Cleanup(func() { contextHistoryFormatter = old })
	for _, tc := range []struct {
		name  string
		value any
		want  bool
	}{
		{"absent", nil, false}, {"false", false, false}, {"true", true, true}, {"string", "true", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			contextHistoryFormatter = func(_ *store.Store, project, scope string, opts store.ContextOptions) (string, error) {
				called = true
				if project != "engram" || scope != "personal" || opts.OlderHistory != tc.want || !opts.Compact || opts.Pinned != 20 || opts.MaxBytes <= 0 || opts.MaxBytes >= 1024 {
					t.Errorf("forwarded %q %q %+v", project, scope, opts)
				}
				return "fixture", nil
			}
			args := map[string]any{"project": "engram", "scope": "personal", "compact": true, "max_bytes": float64(1024)}
			if tc.value != nil {
				args["older_history"] = tc.value
			}
			result := budgetCallContext(t, s, args)
			if !called || budgetContextPart(t, result) != "fixture" || len(result) > 1024 {
				t.Fatalf("result: %q, called=%v", result, called)
			}
		})
	}
}
