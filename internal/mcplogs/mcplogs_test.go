package mcplogs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanRejectsMissingRoot(t *testing.T) {
	if _, err := ScanLifecycles(filepath.Join("testdata", "does-not-exist")); err == nil {
		t.Fatal("ScanLifecycles on missing root must fail")
	}
}

func TestParseSurvivesMalformedLines(t *testing.T) {
	dir := t.TempDir()
	serverDir := filepath.Join(dir, "mcp-logs-engram")
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "not json at all\n" +
		"{\"debug\":\"Starting connection with timeout of 30000ms\",\"timestamp\":\"2026-09-27T09:00:00.000Z\",\"sessionId\":\"s1\",\"cwd\":\"/tmp\"}\n" +
		"\n" +
		"{\"debug\":\"UNKNOWN connection closed after 3s (cleanly)\",\"timestamp\":\"2026-09-27T09:00:03.000Z\",\"sessionId\":\"s1\",\"cwd\":\"/tmp\"}\n" +
		"{\"debug\":\"Cleared connection cache for reconnection\",\"timestamp\":\"2026-09-27T09:00:03.004Z\",\"sessionId\":\"s1\",\"cwd\":\"/tmp\"}\n"
	if err := os.WriteFile(filepath.Join(serverDir, "s1.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	lifecycles, err := ScanLifecycles(dir)
	if err != nil {
		t.Fatalf("ScanLifecycles with malformed lines: %v", err)
	}
	if len(lifecycles) != 1 {
		t.Fatalf("got %d lifecycles, want 1: %v", len(lifecycles), lifecycles)
	}
	lc := lifecycles[0]
	if lc.Server != "engram" || lc.SessionID != "s1" {
		t.Fatalf("grouping = %s/%s, want engram/s1", lc.Server, lc.SessionID)
	}
	if !lc.HasClose || lc.ClosedAfterSec != 3 || lc.ClosedAt.IsZero() {
		t.Errorf("close = %v/%d/%v, want true/3/non-zero", lc.HasClose, lc.ClosedAfterSec, lc.ClosedAt)
	}
	if !lc.ClearedCache || lc.EverConnected {
		t.Errorf("cleared=%v everConnected=%v, want true/false", lc.ClearedCache, lc.EverConnected)
	}
}

// TestExtractsSessionLifecycles covers the parse contract on inline-written
// temp files: grouping by (server, session), close/clear/SIGINT/tool-call
// extraction, everConnected, and deterministic server-then-session order.
func TestExtractsSessionLifecycles(t *testing.T) {
	dir := t.TempDir()
	write := func(server, name, lines string) {
		t.Helper()
		serverDir := filepath.Join(dir, "mcp-logs-"+server)
		if err := os.MkdirAll(serverDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(serverDir, name+".jsonl"), []byte(lines), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	line := func(debug, ts, session string) string {
		return `{"debug":"` + debug + `","timestamp":"` + ts + `","sessionId":"` + session + `","cwd":"/tmp"}` + "\n"
	}
	write("zeta", "a",
		line("Starting connection with timeout of 30000ms", "2026-09-27T09:00:00.000Z", "s1")+
			line("Successfully connected", "2026-09-27T09:00:01.000Z", "s1")+
			line("Tool 'mcp' completed successfully", "2026-09-27T09:00:02.000Z", "s1"))
	write("alpha", "b",
		line("connection closed after 4s (cleanly)", "2026-09-27T09:00:05.000Z", "s2")+
			line("Cleared connection cache for reconnection", "2026-09-27T09:00:05.004Z", "s2")+
			line("Sending SIGINT to MCP server process", "2026-09-27T09:00:06.000Z", "s2"))
	write("alpha", "a", line("Successfully connected", "2026-09-27T09:00:03.000Z", "s1"))
	lifecycles, err := ScanLifecycles(dir)
	if err != nil {
		t.Fatalf("ScanLifecycles: %v", err)
	}
	if len(lifecycles) != 3 {
		t.Fatalf("got %d lifecycles, want 3: %v", len(lifecycles), lifecycles)
	}
	want := []SessionLifecycle{
		{Server: "alpha", SessionID: "s1", EverConnected: true},
		{Server: "alpha", SessionID: "s2", HasClose: true,
			ClosedAt: time.Date(2026, 9, 27, 9, 0, 5, 0, time.UTC), ClosedAfterSec: 4,
			ClearedCache: true, SIGINTSent: true},
		{Server: "zeta", SessionID: "s1", EverConnected: true,
			Starts:    []time.Time{time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)},
			ToolCalls: []time.Time{time.Date(2026, 9, 27, 9, 0, 2, 0, time.UTC)}},
	}
	for i, w := range want {
		got := lifecycles[i]
		if got.Server != w.Server || got.SessionID != w.SessionID {
			t.Errorf("row %d = %s/%s, want %s/%s (server-then-session order)", i, got.Server, got.SessionID, w.Server, w.SessionID)
			continue
		}
		if got.EverConnected != w.EverConnected || got.HasClose != w.HasClose ||
			got.ClosedAfterSec != w.ClosedAfterSec || got.ClearedCache != w.ClearedCache || got.SIGINTSent != w.SIGINTSent {
			t.Errorf("row %d (%s/%s) flags = connected:%v close:%v/%d cleared:%v sigint:%v",
				i, got.Server, got.SessionID, got.EverConnected, got.HasClose, got.ClosedAfterSec, got.ClearedCache, got.SIGINTSent)
		}
		if !got.ClosedAt.Equal(w.ClosedAt) {
			t.Errorf("row %d (%s/%s) ClosedAt = %v, want %v", i, got.Server, got.SessionID, got.ClosedAt, w.ClosedAt)
		}
		if len(got.Starts) != len(w.Starts) || len(got.ToolCalls) != len(w.ToolCalls) {
			t.Errorf("row %d (%s/%s) starts=%d toolCalls=%d, want %d/%d", i, got.Server, got.SessionID,
				len(got.Starts), len(got.ToolCalls), len(w.Starts), len(w.ToolCalls))
			continue
		}
		for k, at := range w.Starts {
			if !got.Starts[k].Equal(at) {
				t.Errorf("row %d (%s/%s) Starts[%d] = %v, want %v", i, got.Server, got.SessionID, k, got.Starts[k], at)
			}
		}
		for k, at := range w.ToolCalls {
			if !got.ToolCalls[k].Equal(at) {
				t.Errorf("row %d (%s/%s) ToolCalls[%d] = %v, want %v", i, got.Server, got.SessionID, k, got.ToolCalls[k], at)
			}
		}
	}
}
