package agents

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AgentType represents a known AI agent.
type AgentType string

const (
	ClaudeCode AgentType = "claude-code"
	GeminiCLI  AgentType = "gemini-cli"
	OpenCode   AgentType = "opencode"
	Cursor     AgentType = "cursor"
	Codex      AgentType = "codex"
	Unknown    AgentType = "unknown"
)

// AgentStats holds usage statistics for a single agent.
type AgentStats struct {
	Agent       AgentType `json:"agent"`
	Sessions    int       `json:"sessions"`
	Messages    int       `json:"messages"`
	Projects    int       `json:"projects"`
	FirstSeen  string    `json:"first_seen"`
	LastSeen   string    `json:"last_seen"`
	ProjectList []string  `json:"projects_list,omitempty"`
}

// AllAgentsStats holds combined stats for all agents.
type AllAgentsStats struct {
	Agents    []AgentStats       `json:"agents"`
	TotalDays int               `json:"total_days"`
	ByProject map[string][]AgentType `json:"by_project,omitempty"`
}

// DetectAgents scans the user's home directory for known AI agents
// and returns usage statistics for each.
func DetectAgents(homeDir string) (*AllAgentsStats, error) {
	stats := &AllAgentsStats{
		Agents:     make([]AgentStats, 0),
		ByProject:  make(map[string][]AgentType),
	}

	// Detect Claude Code
	if claudeStats := detectClaudeCode(homeDir); claudeStats.Sessions > 0 {
		stats.Agents = append(stats.Agents, *claudeStats)
	}

	// Detect Gemini CLI
	if geminiStats := detectGeminiCLI(homeDir); geminiStats.Sessions > 0 {
		stats.Agents = append(stats.Agents, *geminiStats)
	}

	// Detect OpenCode
	if opencodeStats := detectOpenCode(homeDir); opencodeStats.Sessions > 0 {
		stats.Agents = append(stats.Agents, *opencodeStats)
	}

	// Detect Cursor
	if cursorStats := detectCursor(homeDir); cursorStats.Sessions > 0 {
		stats.Agents = append(stats.Agents, *cursorStats)
	}

	// Sort by sessions (descending)
	sort.Slice(stats.Agents, func(i, j int) bool {
		return stats.Agents[i].Sessions > stats.Agents[j].Sessions
	})

	return stats, nil
}

// detectClaudeCode reads Claude Code's history and stats.
func detectClaudeCode(homeDir string) *AgentStats {
	claudeDir := filepath.Join(homeDir, ".claude")
	stats := &AgentStats{Agent: ClaudeCode}

	// Read stats-cache.json
	statsCache := filepath.Join(claudeDir, "stats-cache.json")
	if data, err := os.ReadFile(statsCache); err == nil {
		var cache struct {
			TotalSessions int `json:"totalSessions"`
			TotalMessages int `json:"totalMessages"`
		}
		if json.Unmarshal(data, &cache) == nil {
			stats.Sessions = cache.TotalSessions
			stats.Messages = cache.TotalMessages
		}
	}

	// Count projects
	projectsDir := filepath.Join(claudeDir, "projects")
	if entries, err := os.ReadDir(projectsDir); err == nil {
		stats.Projects = 0
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				stats.Projects++
				stats.ProjectList = append(stats.ProjectList, e.Name())
			}
		}
	}

	// Get first/last from history
	if history := filepath.Join(claudeDir, "history.jsonl"); exists(history) {
		stats.FirstSeen, stats.LastSeen = getHistoryRange(history)
	}

	return stats
}

// detectGeminiCLI reads Gemini CLI's history.
func detectGeminiCLI(homeDir string) *AgentStats {
	geminiDir := filepath.Join(homeDir, ".gemini")
	stats := &AgentStats{Agent: GeminiCLI}

	// Read projects.json
	projectsFile := filepath.Join(geminiDir, "projects.json")
	if data, err := os.ReadFile(projectsFile); err == nil {
		var projData struct {
			Projects map[string]string `json:"projects"`
		}
		if json.Unmarshal(data, &projData) == nil {
			stats.Projects = len(projData.Projects)
			for _, name := range projData.Projects {
				stats.ProjectList = append(stats.ProjectList, name)
			}
		}
	}

	// Estimate sessions from sessions directory
	sessionsDir := filepath.Join(geminiDir, "sessions")
	if entries, err := os.ReadDir(sessionsDir); err == nil {
		stats.Sessions = len(entries)
	}

	return stats
}

// detectOpenCode reads OpenCode's history.
func detectOpenCode(homeDir string) *AgentStats {
	opencodeDir := filepath.Join(homeDir, ".opencode")
	stats := &AgentStats{Agent: OpenCode}

	// Read config
	configFile := filepath.Join(opencodeDir, "config.json")
	if exists(configFile) {
		stats.Sessions = 1 // OpenCode doesn't track sessions like Claude
	}

	return stats
}

// detectCursor reads Cursor's history.
func detectCursor(homeDir string) *AgentStats {
	cursorDir := filepath.Join(homeDir, ".cursor")
	stats := &AgentStats{Agent: Cursor}

	// Check if Cursor has been used (has history or memories)
	memoriesDir := filepath.Join(cursorDir, "memories")
	if exists(memoriesDir) {
		if entries, err := os.ReadDir(memoriesDir); err == nil {
			stats.Sessions = len(entries)
		}
	}

	return stats
}

// getHistoryRange returns first and last timestamp from a .jsonl history file.
func getHistoryRange(historyFile string) (first, last string) {
	file, err := os.Open(historyFile)
	if err != nil {
		return "", ""
	}
	defer file.Close()

	var earliest, latest time.Time

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Try to extract timestamp from JSON line
		// Claude Code history.jsonl format: {"display":"...","timestamp":1764696488146,...}
		var entry struct {
			Timestamp int64 `json:"timestamp"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Timestamp > 0 {
			// timestamp is Unix milliseconds
			t := time.UnixMilli(entry.Timestamp)
			if earliest.IsZero() || t.Before(earliest) {
				earliest = t
			}
			if latest.IsZero() || t.After(latest) {
				latest = t
			}
		}
	}

	if !earliest.IsZero() {
		first = earliest.Format("2006-01-02")
	}
	if !latest.IsZero() {
		last = latest.Format("2006-01-02")
	}

	return first, last
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
