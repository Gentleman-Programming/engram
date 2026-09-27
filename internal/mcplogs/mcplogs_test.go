package mcplogs

import (
	"os"
	"path/filepath"
	"testing"
)

// Fixtures under testdata/ are transcribed from the #1019 thread evidence:
// report-broken (issue body 2026-07-05), healthy-44s (jjeg1979 2026-08-15),
// jjeg-run1/jjeg-run2 (jjeg1979 2026-08-24 positive control), may-indeterminate
// (Denver2828 2026-08-11 shape), synthetic-defect (labeled synthetic: the only
// witness case; no confirmed defect-positive exists in the wild yet),
// reconnect-recovered (clause 4 branch).

func scanFixture(t *testing.T, name string) []SessionVerdict {
	t.Helper()
	verdicts, err := ScanDir(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("ScanDir(%s): %v", name, err)
	}
	return verdicts
}

func findVerdict(verdicts []SessionVerdict, server string) (SessionVerdict, bool) {
	for _, v := range verdicts {
		if v.Server == server {
			return v, true
		}
	}
	return SessionVerdict{}, false
}

func TestScanClassifiesThreadFixtures(t *testing.T) {
	cases := []struct {
		fixture   string
		server    string
		candidate bool
		verdict   Verdict
		reason    string
	}{
		// Original report: candidate, but nothing else logged in the session.
		{"report-broken", "plugin-engram-engram", true, VerdictIndeterminate, "no-peer"},
		// Healthy 44s SIGINT teardown: not even a candidate.
		{"healthy-44s", "engram", false, VerdictHealthy, "not-a-candidate"},
		// jjeg1979 run 1: every server closes within seconds, nobody survives.
		// codegraph is the load-bearing row: strict 4-clause match, still negative.
		{"jjeg-run1", "plugin-engram-engram", true, VerdictTeardown, "peer-closed-within-10s"},
		{"jjeg-run1", "engram", true, VerdictTeardown, "peer-closed-within-10s"},
		{"jjeg-run1", "codegraph", true, VerdictTeardown, "peer-closed-within-10s"},
		{"jjeg-run1", "context7", false, VerdictHealthy, "not-a-candidate"},
		// jjeg1979 run 2: codegraph (1s) and context7 (0s) fall below the 2s bound.
		{"jjeg-run2", "plugin-engram-engram", true, VerdictTeardown, "peer-closed-within-10s"},
		{"jjeg-run2", "engram", true, VerdictTeardown, "peer-closed-within-10s"},
		{"jjeg-run2", "codegraph", false, VerdictHealthy, "not-a-candidate"},
		{"jjeg-run2", "context7", false, VerdictHealthy, "not-a-candidate"},
		// Denver2828 May shape: a peer exists but never connected (needs-auth skip).
		{"may-indeterminate", "plugin-engram-engram", true, VerdictIndeterminate, "peer-never-connected"},
		// SYNTHETIC witness case: peer completes a tool call after our close and
		// outlives us by minutes.
		{"synthetic-defect", "engram", true, VerdictDefectSuspect, "peer-tool-call-after-close"},
		// Clause 4: a later Starting connection after the close disqualifies the
		// candidate outright.
		{"reconnect-recovered", "engram", false, VerdictHealthy, "not-a-candidate"},
	}
	for _, tc := range cases {
		verdicts := scanFixture(t, tc.fixture)
		v, ok := findVerdict(verdicts, tc.server)
		if !ok {
			t.Errorf("%s/%s: no verdict for server (got %v)", tc.fixture, tc.server, verdicts)
			continue
		}
		if v.Candidate != tc.candidate {
			t.Errorf("%s/%s: candidate = %v, want %v", tc.fixture, tc.server, v.Candidate, tc.candidate)
		}
		if v.Verdict != tc.verdict {
			t.Errorf("%s/%s: verdict = %q, want %q", tc.fixture, tc.server, v.Verdict, tc.verdict)
		}
		if v.Reason != tc.reason {
			t.Errorf("%s/%s: reason = %q, want %q", tc.fixture, tc.server, v.Reason, tc.reason)
		}
	}
}

// TestNegativeControlNoDefectSuspects is the load-bearing property from
// jjeg1979's 2026-08-24 comment: the unauthenticated short-session teardown
// produces strict 4-clause matches on multiple servers, and a correct
// detector must return zero defect-suspects on that input.
func TestNegativeControlNoDefectSuspects(t *testing.T) {
	for _, fixture := range []string{"jjeg-run1", "jjeg-run2", "report-broken", "may-indeterminate", "healthy-44s", "reconnect-recovered"} {
		for _, v := range scanFixture(t, fixture) {
			if v.Verdict == VerdictDefectSuspect {
				t.Errorf("%s/%s: defect-suspect on negative-control fixture (reason %q)", fixture, v.Server, v.Reason)
			}
		}
	}
}

func TestScanRejectsMissingRoot(t *testing.T) {
	if _, err := ScanDir(filepath.Join("testdata", "does-not-exist")); err == nil {
		t.Fatal("ScanDir on missing root must fail")
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
	verdicts, err := ScanDir(dir)
	if err != nil {
		t.Fatalf("ScanDir with malformed lines: %v", err)
	}
	v, ok := findVerdict(verdicts, "engram")
	if !ok {
		t.Fatalf("no verdict parsed from malformed-tolerant file: %v", verdicts)
	}
	if !v.Candidate || v.Verdict != VerdictIndeterminate {
		t.Errorf("malformed-tolerant parse: candidate=%v verdict=%q, want candidate=true indeterminate", v.Candidate, v.Verdict)
	}
}
