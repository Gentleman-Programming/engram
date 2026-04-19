package claudecode

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// ImportConfig holds configuration for the Claude Code importer.
type ImportConfig struct {
	// ClaudeProjectsDir is the path to the Claude projects directory
	ClaudeProjectsDir string

	// Project is an optional filter to import only from a specific project
	Project string

	// DryRun if true, does not write anything to the store
	DryRun bool
}

// Importer reads markdown files from Claude Code's memory folder and
// imports them as observations into the store.
type Importer struct {
	store  ObservationWriter
	config ImportConfig
}

// ObservationWriter is the write interface the importer needs.
type ObservationWriter interface {
	AddObservation(p store.AddObservationParams) (int64, error)
	CreateSession(id, project, directory string) error
}

// NewImporter constructs an Importer.
func NewImporter(s ObservationWriter, cfg ImportConfig) *Importer {
	return &Importer{store: s, config: cfg}
}

// Import imports observations from Claude Code's memory folder into the store.
// It returns an ImportResult summarizing what happened.
func (i *Importer) Import() (*ImportResult, error) {
	if i.config.ClaudeProjectsDir == "" {
		return nil, fmt.Errorf("claudecode: --claude-projects-dir is required")
	}

	result := &ImportResult{}

	// Find all project directories
	entries, err := os.ReadDir(i.config.ClaudeProjectsDir)
	if err != nil {
		return nil, fmt.Errorf("read projects dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		projectSlug := entry.Name()
		memoryDir := filepath.Join(i.config.ClaudeProjectsDir, projectSlug, "memory")

		// Filter by project if specified
		if i.config.Project != "" {
			expectedSlug := slugifyProjectName(i.config.Project)
			if projectSlug != expectedSlug {
				continue
			}
		}

		if err := i.importProject(projectSlug, memoryDir, result); err != nil {
			result.Errors = append(result.Errors, err)
		}
	}

	return result, nil
}

// importProject imports all memory files from a single project's memory folder.
func (i *Importer) importProject(slug, memoryDir string, result *ImportResult) error {
	// Check if memory directory exists
	if _, err := os.Stat(memoryDir); os.IsNotExist(err) {
		return nil // No memory folder, skip
	}

	entries, err := os.ReadDir(memoryDir)
	if err != nil {
		return fmt.Errorf("read memory dir %s: %w", memoryDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		if filename == "MEMORY.md" {
			continue // Skip index file
		}

		if !strings.HasSuffix(filename, ".md") {
			continue
		}

		filePath := filepath.Join(memoryDir, filename)
		obs, err := parseMemoryFile(filePath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("parse %s: %w", filePath, err))
			continue
		}

		// Create session if needed
		sessionID := obs.SessionID
		if sessionID == "" {
			sessionID = "claude-code-import"
		}
		project := obs.Project
		if project == "" {
			project = unslugifyProjectName(slug)
		}

		if !i.config.DryRun {
			// Ensure session exists
			if err := i.store.CreateSession(sessionID, project, ""); err != nil {
				// Ignore "session already exists" errors
				if !strings.Contains(err.Error(), "already exists") {
					result.Errors = append(result.Errors, fmt.Errorf("create session: %w", err))
				}
			}

			// Add observation
			id, err := i.store.AddObservation(store.AddObservationParams{
				SessionID: sessionID,
				Type:      obs.Type,
				Title:     obs.Title,
				Content:   obs.Content,
				Project:   project,
				Scope:     "project",
				TopicKey:  obs.TopicKey,
			})
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("add observation: %w", err))
				continue
			}
			result.Imported++
			_ = id // Observation ID is not needed for anything
		} else {
			result.Imported++
		}
	}

	return nil
}

// ParsedMemory represents a memory file parsed from Claude Code's format.
type ParsedMemory struct {
	Title       string
	Description string
	Type        string
	SessionID   string
	Project     string
	TopicKey    string
	Content     string
}

// parseMemoryFile reads a Claude Code memory .md file and extracts the observation data.
func parseMemoryFile(path string) (*ParsedMemory, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer file.Close()

	var inFrontmatter bool
	var frontmatter strings.Builder
	var content strings.Builder
	var inContent bool

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()

		if !inFrontmatter && !inContent && strings.TrimSpace(line) == "---" {
			inFrontmatter = true
			continue
		}

		if inFrontmatter && strings.TrimSpace(line) == "---" {
			inFrontmatter = false
			inContent = true
			continue
		}

		if inFrontmatter {
			frontmatter.WriteString(line + "\n")
		} else if inContent {
			// Skip the H2 title line (## Title)
			if strings.HasPrefix(strings.TrimSpace(line), "## ") {
				continue
			}
			content.WriteString(line + "\n")
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	// Parse frontmatter
	fm := frontmatter.String()
	parsed := parseFrontmatter(fm)
	parsed.Content = strings.TrimSpace(content.String())

	// If Title is empty, try to get from filename
	if parsed.Title == "" {
		filename := filepath.Base(path)
		filename = strings.TrimSuffix(filename, ".md")
		// Remove "project_" prefix
		if strings.HasPrefix(filename, "project_") {
			filename = strings.TrimPrefix(filename, "project_")
		}
		// Replace underscores with spaces
		filename = strings.ReplaceAll(filename, "_", " ")
		parsed.Title = filename
	}

	if parsed.Type == "" {
		parsed.Type = "imported"
	}

	return parsed, nil
}

// parseFrontmatter parses YAML-like frontmatter from Claude Code memory files.
func parseFrontmatter(fm string) *ParsedMemory {
	result := &ParsedMemory{}

	scanner := bufio.NewScanner(strings.NewReader(fm))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse "key: value" lines
		idx := strings.Index(line, ":")
		if idx == -1 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])

		// Remove quotes if present
		if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
			value = value[1 : len(value)-1]
			value = strings.ReplaceAll(value, "\\\"", "\"")
		}

		switch key {
		case "name":
			result.Title = value
		case "description":
			result.Description = value
		case "type":
			result.Type = value
		case "originSessionId":
			result.SessionID = value
		case "project":
			result.Project = value
		case "topic_key":
			result.TopicKey = value
		}
	}

	return result
}

// unslugifyProjectName converts a Claude Code project slug back to a readable name.
// e.g., "C--Users-JorgeHaraDevs-Desktop-My-Project" -> "my project"
func unslugifyProjectName(slug string) string {
	// Remove "C--" prefix
	if strings.HasPrefix(slug, "C--") {
		slug = slug[3:]
	}

	// Handle Claude Code's path-style slugs:
	// C--Users-JorgeHaraDevs-Desktop-My-Project -> My Project
	// C--home-jorgeharadevs-project -> project
	//
	// Strategy: if we can identify this as a path-style slug (contains Desktop/Users/home etc.),
	// extract just the project name (last meaningful component after those path markers).
	if strings.Contains(slug, "Desktop") || strings.Contains(slug, "Users") || strings.HasPrefix(slug, "home-") {
		// Find the last hyphen-separated component after path markers
		parts := strings.Split(slug, "-")
		// Find "Desktop" or "Users" or "home" and take everything after the last path component
		lastPart := ""
		for i := len(parts) - 1; i >= 0; i-- {
			part := parts[i]
			if part == "Desktop" || part == "Users" || part == "home" {
				break
			}
			if lastPart != "" {
				lastPart = part + "-" + lastPart
			} else {
				lastPart = part
			}
		}
		if lastPart != "" {
			// Also replace remaining hyphens with spaces
			name := strings.ReplaceAll(lastPart, "-", " ")
			return strings.ToLower(name)
		}
	}

	// Simple case: just replace hyphens with spaces and lowercase
	name := strings.ReplaceAll(slug, "-", " ")
	return strings.ToLower(strings.TrimSpace(name))
}

// ImportResult holds the result of an import operation.
type ImportResult struct {
	Imported int
	Skipped  int
	Errors   []error
}

// SyncResult holds the combined result of a bidirectional sync.
type SyncResult struct {
	Export *ExportResult
	Import *ImportResult
}

// SyncConfig holds configuration for bidirectional sync.
type SyncConfig struct {
	ClaudeProjectsDir string
	Project          string
	DryRun           bool
}

// FullSyncResult is the result of a full bidirectional sync.
type FullSyncResult struct {
	ExportResult *ExportResult
	ImportResult *ImportResult
	Errors       []error
}
