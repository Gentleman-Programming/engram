// Package mcplogs parses Claude Code MCP client logs for the connect /
// close / never-reconnect failure class tracked in gentle-ai#1019.
//
// Claude Code writes per-server cache fragments as directories named
// mcp-logs-<server>, each containing per-session *.jsonl files where every
// line is a JSON object with a debug message and a timestamp. ScanLifecycles
// groups those lines into per-(server, session) SessionLifecycle records in
// deterministic server-then-session order. Classification of the lifecycles
// lives in a follow-up slice.
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

var (
	closePattern   = regexp.MustCompile(`connection closed after (\d+)s`)
	toolPattern    = regexp.MustCompile(`Tool '([^']+)' completed successfully`)
	startPattern   = regexp.MustCompile(`Starting connection with timeout`)
	connectPattern = regexp.MustCompile(`Successfully connected`)
	clearPattern   = regexp.MustCompile(`Cleared connection cache for reconnection`)
	sigintPattern  = regexp.MustCompile(`Sending SIGINT to MCP server process`)
)

type logLine struct {
	Debug     string    `json:"debug"`
	Timestamp time.Time `json:"timestamp"`
	SessionID string    `json:"sessionId"`
}

// SessionLifecycle is the parsed connection history of one (server, session)
// log group.
type SessionLifecycle struct {
	Server         string
	SessionID      string
	Starts         []time.Time
	EverConnected  bool
	ClosedAt       time.Time
	ClosedAfterSec int
	HasClose       bool
	ClearedCache   bool
	SIGINTSent     bool
	ToolCalls      []time.Time
}

// logDirPrefix is the Claude Code cache directory prefix for per-server logs.
const logDirPrefix = "mcp-logs-"

// ScanLifecycles walks a Claude Code cache fragment (directories named
// mcp-logs-<server>, each containing per-session *.jsonl files) and returns
// the parsed SessionLifecycle for every (server, session) group, sorted by
// server then session id. Malformed lines are skipped.
func ScanLifecycles(root string) ([]SessionLifecycle, error) {
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
	groups := make(map[string]*SessionLifecycle)
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
	lifecycles := make([]SessionLifecycle, 0, len(groups))
	for _, group := range groups {
		lifecycles = append(lifecycles, *group)
	}
	sort.Slice(lifecycles, func(i, j int) bool {
		if lifecycles[i].Server != lifecycles[j].Server {
			return lifecycles[i].Server < lifecycles[j].Server
		}
		return lifecycles[i].SessionID < lifecycles[j].SessionID
	})
	return lifecycles, nil
}

func scanFile(path, server string, groups map[string]*SessionLifecycle) error {
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
			group = &SessionLifecycle{Server: server, SessionID: line.SessionID}
			groups[key] = group
		}
		applyLine(group, line)
	}
	return scanner.Err()
}

func applyLine(group *SessionLifecycle, line logLine) {
	debug, at := line.Debug, line.Timestamp
	switch {
	case startPattern.MatchString(debug):
		group.Starts = append(group.Starts, at)
	case connectPattern.MatchString(debug):
		group.EverConnected = true
	case closePattern.MatchString(debug):
		if m := closePattern.FindStringSubmatch(debug); len(m) == 2 {
			if secs, err := strconv.Atoi(m[1]); err == nil {
				group.HasClose = true
				group.ClosedAfterSec = secs
				group.ClosedAt = at
			}
		}
	case clearPattern.MatchString(debug):
		group.ClearedCache = true
	case sigintPattern.MatchString(debug):
		group.SIGINTSent = true
	case toolPattern.MatchString(debug):
		group.ToolCalls = append(group.ToolCalls, at)
	}
}
