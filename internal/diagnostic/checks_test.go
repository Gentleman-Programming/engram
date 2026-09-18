package diagnostic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// seedSessionStartedAt creates an open session and backdates its started_at so
// Scope.Now time-travel can drive the staleness window deterministically.
func seedSessionStartedAt(t *testing.T, s *store.Store, id, project, startedAt string) {
	t.Helper()
	if err := s.CreateSession(id, project, "/work/"+id); err != nil {
		t.Fatalf("create session %s: %v", id, err)
	}
	if _, err := s.DB().Exec(`UPDATE sessions SET started_at = ? WHERE id = ?`, startedAt, id); err != nil {
		t.Fatalf("backdate session %s: %v", id, err)
	}
}

func TestStaleOpenSessionsCheckFlagsOnlyStaleSessions(t *testing.T) {
	s := newDiagnosticTestStore(t)
	// Scope.Now is fixed in the future so seeded timestamps are stable facts,
	// not races against the wall clock.
	now := time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)
	seedSessionStartedAt(t, s, "stale-a", "proj-a", "2026-06-01 00:00:00")
	seedSessionStartedAt(t, s, "stale-b", "proj-b", "2026-06-15 08:30:00")
	seedSessionStartedAt(t, s, "fresh-a", "proj-a", "2026-12-31 00:00:00")

	check := StaleOpenSessionsCheck{}
	if got := check.Code(); got != CheckStaleOpenSessions {
		t.Fatalf("code = %q, want %q", got, CheckStaleOpenSessions)
	}

	result, err := check.Run(context.Background(), Scope{Store: s, Now: now})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Result != StatusWarning || result.Severity != SeverityWarning {
		t.Fatalf("result=%s severity=%s, want warning", result.Result, result.Severity)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %d, want 1 aggregate finding", len(result.Findings))
	}
	finding := result.Findings[0]
	if finding.CheckID != CheckStaleOpenSessions || finding.ReasonCode != CheckStaleOpenSessions {
		t.Fatalf("finding identity = %s/%s, want %s", finding.CheckID, finding.ReasonCode, CheckStaleOpenSessions)
	}
	if finding.Severity != SeverityWarning {
		t.Fatalf("finding severity = %s, want warning", finding.Severity)
	}
	if !strings.Contains(finding.SafeNextStep, "engram session end --by-age 720h --project <name>") || !strings.Contains(finding.SafeNextStep, "dry-run") {
		t.Fatalf("safe next step = %q, want the session end dry-run pointer", finding.SafeNextStep)
	}

	var evidence struct {
		StaleCount   int            `json:"stale_count"`
		ByProject    map[string]int `json:"by_project"`
		Oldest       string         `json:"oldest"`
		NewestStale  string         `json:"newest_stale"`
		ThresholdDay int            `json:"threshold_days"`
	}
	if err := json.Unmarshal(finding.Evidence, &evidence); err != nil {
		t.Fatalf("invalid evidence JSON %s: %v", finding.Evidence, err)
	}
	if evidence.StaleCount != 2 {
		t.Fatalf("stale_count = %d, want 2", evidence.StaleCount)
	}
	if evidence.ByProject["proj-a"] != 1 || evidence.ByProject["proj-b"] != 1 {
		t.Fatalf("by_project = %v, want one stale session per project", evidence.ByProject)
	}
	if evidence.Oldest != "2026-06-01 00:00:00" {
		t.Fatalf("oldest = %q, want the oldest stale last activity", evidence.Oldest)
	}
	if evidence.NewestStale != "2026-06-15 08:30:00" {
		t.Fatalf("newest_stale = %q, want the newest stale last activity", evidence.NewestStale)
	}
	if evidence.ThresholdDay != 30 {
		t.Fatalf("threshold_days = %d, want 30", evidence.ThresholdDay)
	}
}

func TestStaleOpenSessionsCheckAllFreshIsOK(t *testing.T) {
	s := newDiagnosticTestStore(t)
	now := time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)
	seedSessionStartedAt(t, s, "fresh-only", "proj", "2026-12-31 00:00:00")

	result, err := StaleOpenSessionsCheck{}.Run(context.Background(), Scope{Store: s, Now: now})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Result != StatusOK {
		t.Fatalf("result = %s, want ok for a fully fresh store", result.Result)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("findings = %d, want none", len(result.Findings))
	}
}

func TestStaleOpenSessionsCheckIgnoresEndedSessions(t *testing.T) {
	s := newDiagnosticTestStore(t)
	now := time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)
	seedSessionStartedAt(t, s, "old-but-ended", "proj", "2026-06-01 00:00:00")
	if err := s.EndSession("old-but-ended", "done"); err != nil {
		t.Fatalf("end session: %v", err)
	}

	result, err := StaleOpenSessionsCheck{}.Run(context.Background(), Scope{Store: s, Now: now})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Result != StatusOK || len(result.Findings) != 0 {
		t.Fatalf("result=%s findings=%d, want ok with no findings for ended sessions", result.Result, len(result.Findings))
	}
}

func TestStaleOpenSessionsCheckRegisteredAndRunnable(t *testing.T) {
	found := false
	for _, code := range RegisteredCodes() {
		if code == CheckStaleOpenSessions {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("RegisteredCodes() = %v, want %q registered", RegisteredCodes(), CheckStaleOpenSessions)
	}
	if _, err := DefaultRegistry().Lookup(CheckStaleOpenSessions); err != nil {
		t.Fatalf("DefaultRegistry lookup: %v", err)
	}

	s := newDiagnosticTestStore(t)
	report, err := NewRunner().RunOne(context.Background(), Scope{
		Store: s,
		Now:   time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC),
	}, CheckStaleOpenSessions)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].CheckID != CheckStaleOpenSessions {
		t.Fatalf("report checks = %+v, want exactly the stale_open_sessions check", report.Checks)
	}
}
