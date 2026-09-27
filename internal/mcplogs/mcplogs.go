// Package mcplogs classifies Claude Code MCP client logs for the connect /
// close / never-reconnect failure class tracked in gentle-ai#1019.
//
// The classifier implements the surviving-witness standard proposed by
// Denver2828 (2026-08-23) and validated against jjeg1979's negative control
// (2026-08-24): a 4-clause candidate (declared close of 2-5s, cache-clear,
// zero completed tool calls, no later reconnection) only becomes a
// defect-suspect when another MCP server in the same session demonstrably
// outlived the close. A candidate whose peers all closed within ~10s is
// ordinary session teardown; a candidate with no witness either way is
// indeterminate.
//
// No confirmed defect-positive log exists in the wild yet: the
// defect-suspect path is exercised only by the labeled synthetic fixture in
// testdata/synthetic-defect.
package mcplogs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Verdict is the classification of one (server, session) log group.
type Verdict string

// Verdict values, from Denver2828's 2026-08-23 analysis and jjeg1979's
// 2026-08-24 negative control.
const (
	// VerdictHealthy: no 4-clause candidate match (includes ordinary SIGINT
	// teardown and sessions that reconnected after a close).
	VerdictHealthy Verdict = "healthy"
	// VerdictTeardown: candidate match, but peer servers closed within ~10s —
	// the whole session ended; not this defect.
	VerdictTeardown Verdict = "teardown"
	// VerdictIndeterminate: candidate match, but no peer can witness either way.
	VerdictIndeterminate Verdict = "indeterminate"
	// VerdictDefectSuspect: candidate match plus a surviving witness.
	VerdictDefectSuspect Verdict = "defect-suspect"
)

// Candidate lifetime bounds, in seconds, from the issue's reported pattern.
const (
	candidateMinLifetimeSec = 2
	candidateMaxLifetimeSec = 5
	// peerCloseWindowSec is Denver2828's session-ended bound: peers closing
	// within this window of our close indicate the whole session ended.
	peerCloseWindowSec = 10 * time.Second
)

var (
	closePattern   = regexp.MustCompile(`connection closed after (\d+)s`)
	toolPattern    = regexp.MustCompile(`Tool '([^']+)' completed successfully`)
	startPattern   = regexp.MustCompile(`Starting connection with timeout`)
	connectPattern = regexp.MustCompile(`Successfully connected`)
	clearPattern   = regexp.MustCompile(`Cleared connection cache for reconnection`)
	sigintPattern  = regexp.MustCompile(`Sending SIGINT to MCP server process`)
)

// SessionVerdict classifies one server's connection lifecycle in one session.
type SessionVerdict struct {
	Server         string
	SessionID      string
	Candidate      bool
	Verdict        Verdict
	Reason         string
	ClosedAfterSec int
	ClearedCache   bool
	SIGINTSent     bool
	ToolCalls      int
}

type logLine struct {
	Debug     string    `json:"debug"`
	Timestamp time.Time `json:"timestamp"`
	SessionID string    `json:"sessionId"`
}

type sessionLog struct {
	Server        string
	SessionID     string
	starts        []time.Time
	everConnected bool
	closedAt      time.Time
	closedSec     int
	hasClose      bool
	cleared       bool
	sigint        bool
	toolCalls     []time.Time
}

// logDirPrefix is the Claude Code cache directory prefix for per-server logs.
const logDirPrefix = "mcp-logs-"

// ScanDir walks a Claude Code cache fragment (directories named
// mcp-logs-<server>, each containing per-session *.jsonl files) and
// classifies every (server, session) group. Malformed lines are skipped.
func ScanDir(root string) ([]SessionVerdict, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("mcplogs: %s is not a directory", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	groups := make(map[string]*sessionLog)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), logDirPrefix) {
			continue
		}
		server := strings.TrimPrefix(entry.Name(), logDirPrefix)
		files, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") {
				continue
			}
			if err := scanFile(filepath.Join(root, entry.Name(), file.Name()), server, groups); err != nil {
				return nil, err
			}
		}
	}
	return classify(groups), nil
}

func scanFile(path, server string, groups map[string]*sessionLog) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var line logLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if line.SessionID == "" || line.Debug == "" {
			continue
		}
		key := server + "\x00" + line.SessionID
		group := groups[key]
		if group == nil {
			group = &sessionLog{Server: server, SessionID: line.SessionID}
			groups[key] = group
		}
		applyLine(group, line)
	}
	return scanner.Err()
}

func applyLine(group *sessionLog, line logLine) {
	debug, at := line.Debug, line.Timestamp
	switch {
	case startPattern.MatchString(debug):
		group.starts = append(group.starts, at)
	case connectPattern.MatchString(debug):
		group.everConnected = true
	case closePattern.MatchString(debug):
		if m := closePattern.FindStringSubmatch(debug); len(m) == 2 {
			if secs, err := strconv.Atoi(m[1]); err == nil {
				group.hasClose = true
				group.closedSec = secs
				group.closedAt = at
			}
		}
	case clearPattern.MatchString(debug):
		group.cleared = true
	case sigintPattern.MatchString(debug):
		group.sigint = true
	case toolPattern.MatchString(debug):
		group.toolCalls = append(group.toolCalls, at)
	}
}

func classify(groups map[string]*sessionLog) []SessionVerdict {
	bySession := make(map[string][]*sessionLog)
	for _, group := range groups {
		bySession[group.SessionID] = append(bySession[group.SessionID], group)
	}
	verdicts := make([]SessionVerdict, 0, len(groups))
	for _, group := range groups {
		verdicts = append(verdicts, verdictFor(group, bySession[group.SessionID]))
	}
	sort.Slice(verdicts, func(i, j int) bool {
		if verdicts[i].Server != verdicts[j].Server {
			return verdicts[i].Server < verdicts[j].Server
		}
		return verdicts[i].SessionID < verdicts[j].SessionID
	})
	return verdicts
}

func verdictFor(group *sessionLog, peers []*sessionLog) SessionVerdict {
	v := SessionVerdict{
		Server:         group.Server,
		SessionID:      group.SessionID,
		ClosedAfterSec: group.closedSec,
		ClearedCache:   group.cleared,
		SIGINTSent:     group.sigint,
		ToolCalls:      len(group.toolCalls),
		Verdict:        VerdictHealthy,
		Reason:         "not-a-candidate",
	}
	v.Candidate = isCandidate(group)
	if !v.Candidate {
		return v
	}
	for _, peer := range peers {
		if peer.Server == group.Server {
			continue
		}
		if witnessByToolCall(peer, group.closedAt) {
			v.Verdict = VerdictDefectSuspect
			v.Reason = "peer-tool-call-after-close"
			return v
		}
	}
	for _, peer := range peers {
		if peer.Server == group.Server {
			continue
		}
		if witnessByOutliving(peer, group.closedAt) {
			v.Verdict = VerdictDefectSuspect
			v.Reason = "peer-outlived"
			return v
		}
	}
	anyPeerConnected := false
	for _, peer := range peers {
		if peer.Server != group.Server && peer.everConnected {
			anyPeerConnected = true
			break
		}
	}
	switch {
	case len(peers) <= 1:
		v.Verdict = VerdictIndeterminate
		v.Reason = "no-peer"
	case anyPeerConnected:
		// Peers connected but nothing survived our close: they closed within
		// the window (or stopped logging without declaring a close), which is
		// ordinary short-session teardown.
		v.Verdict = VerdictTeardown
		v.Reason = "peer-closed-within-10s"
	default:
		v.Verdict = VerdictIndeterminate
		v.Reason = "peer-never-connected"
	}
	return v
}

// isCandidate implements the 4-clause conjunction: declared close of 2-5
// seconds, cache-clear, zero completed tool calls, and no reconnection
// attempt after the close.
func isCandidate(group *sessionLog) bool {
	if !group.hasClose {
		return false
	}
	if group.closedSec < candidateMinLifetimeSec || group.closedSec > candidateMaxLifetimeSec {
		return false
	}
	if !group.cleared {
		return false
	}
	if len(group.toolCalls) > 0 {
		return false
	}
	for _, start := range group.starts {
		if start.After(group.closedAt) {
			return false
		}
	}
	return true
}

// witnessByToolCall: a peer completed a tool call after our close, so the
// session demonstrably continued using MCP without us.
func witnessByToolCall(peer *sessionLog, ourClose time.Time) bool {
	for _, call := range peer.toolCalls {
		if call.After(ourClose) {
			return true
		}
	}
	return false
}

// witnessByOutliving: a peer declared its own close more than the window
// after ours. A peer that never declared a close proves nothing (the
// unauthenticated teardown logs stop without close lines for some servers).
func witnessByOutliving(peer *sessionLog, ourClose time.Time) bool {
	return peer.hasClose && peer.closedAt.Sub(ourClose) > peerCloseWindowSec
}
