package claudecode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// ExportConfig holds configuration for the Claude Code exporter.
type ExportConfig struct {
	// ClaudeProjectsDir is the path to the Claude projects directory
	// (e.g., ~/.claude/projects on Unix, %USERPROFILE%\.claude\projects on Windows)
	ClaudeProjectsDir string

	// Project is an optional filter to export only observations for a specific project
	Project string

	// DryRun if true, does not write any files
	DryRun bool
}

// Exporter reads from the store and writes markdown files to Claude Code's
// memory folder.
type Exporter struct {
	store  StoreReader
	config ExportConfig
}

// StoreReader is the read-only interface the exporter needs.
type StoreReader interface {
	Export() (*store.ExportData, error)
	Stats() (*store.Stats, error)
}

// NewExporter constructs an Exporter.
func NewExporter(s StoreReader, cfg ExportConfig) *Exporter {
	return &Exporter{store: s, config: cfg}
}

// Export exports Engram observations to Claude Code's memory folder.
// It returns an ExportResult summarizing what happened.
func (e *Exporter) Export() (*ExportResult, error) {
	if e.config.ClaudeProjectsDir == "" {
		return nil, fmt.Errorf("claudecode: --claude-projects-dir is required")
	}

	result := &ExportResult{}

	// Get all data from store
	data, err := e.store.Export()
	if err != nil {
		return nil, fmt.Errorf("claudecode: store export: %w", err)
	}

	// Group observations by project
	projectObs := make(map[string][]store.Observation)
	for _, obs := range data.Observations {
		if obs.DeletedAt != nil {
			continue
		}

		proj := ""
		if obs.Project != nil {
			proj = *obs.Project
		}

		// Filter by project if specified
		if e.config.Project != "" && proj != e.config.Project {
			continue
		}

		projectObs[proj] = append(projectObs[proj], obs)
	}

	// Export each project's observations
	for projectName, observations := range projectObs {
		if err := e.exportProject(projectName, observations, result); err != nil {
			result.Errors = append(result.Errors, err)
		}
	}

	return result, nil
}

// exportProject exports observations for a single project to Claude Code's memory folder.
func (e *Exporter) exportProject(projectName string, observations []store.Observation, result *ExportResult) error {
	// Convert project name to Claude Code slug format
	// e.g., "My Project" -> "C--Users-JorgeHaraDevs-Desktop-My-Project"
	projectSlug := slugifyProjectName(projectName)

	// Build the memory directory path
	memoryDir := filepath.Join(e.config.ClaudeProjectsDir, projectSlug, "memory")

	if !e.config.DryRun {
		if err := os.MkdirAll(memoryDir, 0755); err != nil {
			return fmt.Errorf("create memory dir %s: %w", memoryDir, err)
		}
	}

	// Track index entries
	var indexEntries []MemoryIndexEntry

	// Export each observation
	for _, obs := range observations {
		filename := generateFilename(obs)
		content := MemoryFileFormat(obs)

		if !e.config.DryRun {
			filePath := filepath.Join(memoryDir, filename)
			// Check idempotency
			if existing, err := os.ReadFile(filePath); err == nil {
				if string(existing) == content {
					result.Skipped++
					indexEntries = append(indexEntries, MemoryIndexEntry{
						Title:       obs.Title,
						Filename:     filename,
						Description: obs.Content,
					})
					continue // Already up to date
				}
			}

			if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("write %s: %w", filePath, err))
				continue
			}
		}

		result.Created++
		indexEntries = append(indexEntries, MemoryIndexEntry{
			Title:       obs.Title,
			Filename:     filename,
			Description: obs.Content,
		})
	}

	// Update MEMORY.md index
	if len(indexEntries) > 0 && !e.config.DryRun {
		indexContent := MemoryIndexFormat(projectName, indexEntries)
		indexPath := filepath.Join(memoryDir, "MEMORY.md")
		if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("write MEMORY.md: %w", err))
		}
	}

	return nil
}

// generateFilename creates a Claude Code-style filename from an observation.
func generateFilename(obs store.Observation) string {
	// Format: project_{slugified_title}.md
	title := obs.Title
	// Replace spaces with underscores and remove special chars
	slug := strings.ToLower(title)
	slug = strings.ReplaceAll(slug, " ", "_")
	slug = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return -1
	}, slug)
	// Truncate if too long
	if len(slug) > 50 {
		slug = slug[:50]
	}
	return fmt.Sprintf("project_%s.md", slug)
}

// slugifyProjectName converts a project name to Claude Code's project slug format.
// Claude Code uses a URL-encoded-like format: "C--Users-Developer-Desktop-Project-Name"
// This replaces special characters and spaces with hyphens, then prefixes with "C--".
func slugifyProjectName(name string) string {
	if name == "" {
		return "C--unknown"
	}

	// Replace spaces and special chars with hyphens
	slug := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		if r == ' ' || r == '-' || r == '_' || r == '.' {
			return '-'
		}
		return '-'
	}, name)

	// Remove leading/trailing hyphens
	slug = strings.Trim(slug, "-")

	// Collapse multiple hyphens
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}

	return "C--" + slug
}

// ExportResult holds the result of an export operation.
type ExportResult struct {
	Created int
	Updated int
	Skipped int
	Deleted int
	Errors  []error
}
