package obsidian

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// ExportConfig holds all CLI flags for the obsidian-export command.
type ExportConfig struct {
	VaultPath   string          // --vault (required): path to the Obsidian vault root
	Project     string          // --project (optional): filter export to a single project
	Limit       int             // --limit (0 = no limit)
	Since       time.Time       // --since (zero = use state file)
	Force       bool            // --force: ignore state, full re-export
	GraphConfig GraphConfigMode // --graph-config: preserve|force|skip (empty string = skip for backward compat)
}

// StoreReader is the read-only interface the exporter needs.
// Keeps the dependency narrow — easy to mock in tests.
type StoreReader interface {
	Export() (*store.ExportData, error)
	Stats() *store.Stats
}

// Exporter reads from the store and writes markdown files to a vault.
type Exporter struct {
	store  StoreReader
	config ExportConfig
}

// NewExporter constructs an Exporter. Validation happens in Export().
func NewExporter(s StoreReader, cfg ExportConfig) *Exporter {
	return &Exporter{store: s, config: cfg}
}

// sanitizePathComponent strips path separators and dot-dot sequences from a
// single path component (project name or observation type), preventing path
// traversal attacks when the value is used inside filepath.Join.
// Any slash, backslash, or ".." element is replaced with "_".
func sanitizePathComponent(s string) string {
	// Replace OS separators with underscore.
	s = strings.ReplaceAll(s, string(filepath.Separator), "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")

	// Replace ".." with "__" to neutralise traversal.
	s = strings.ReplaceAll(s, "..", "__")

	// filepath.Clean on a single component that contains no separators is a
	// no-op, but run it anyway to normalise any remaining oddities (e.g. trailing
	// dots on Windows). We re-apply the separator guard afterwards because Clean
	// can introduce a leading "/" on absolute-looking inputs on Unix.
	clean := filepath.Clean(s)
	clean = strings.ReplaceAll(clean, string(filepath.Separator), "_")
	clean = strings.ReplaceAll(clean, "/", "_")
	if clean == "" || clean == "." {
		clean = "_"
	}
	return clean
}

// SetGraphConfig sets the GraphConfig mode on this exporter's config.
// This is used by Watcher to force GraphConfigSkip on subsequent cycles (REQ-WATCH-06).
func (e *Exporter) SetGraphConfig(mode GraphConfigMode) {
	e.config.GraphConfig = mode
}

// GraphConfig returns the current GraphConfig mode.
func (e *Exporter) GraphConfig() GraphConfigMode {
	return e.config.GraphConfig
}

// engramRoot returns the {vault}/engram/ root path.
func (e *Exporter) engramRoot() string {
	return filepath.Join(e.config.VaultPath, "engram")
}

// stateFilePath returns the path to the sync state JSON file.
func (e *Exporter) stateFilePath() string {
	return filepath.Join(e.engramRoot(), ".engram-sync-state.json")
}

// Export performs a full or incremental export from the store to the vault.
// It returns an ExportResult summarizing what happened.
func (e *Exporter) Export() (*ExportResult, error) {
	// ── Validate config ───────────────────────────────────────────────────────
	if e.config.VaultPath == "" {
		return nil, fmt.Errorf("obsidian: --vault path is required")
	}

	// ── Write graph config (first cycle only, controlled by caller) ───────────
	// Empty string means skip — preserves backward compatibility with existing
	// code that doesn't set GraphConfig. Watcher sets it to GraphConfigSkip on
	// subsequent cycles to avoid clobbering user edits.
	graphMode := e.config.GraphConfig
	if graphMode == "" {
		graphMode = GraphConfigSkip
	}
	if err := WriteGraphConfig(e.config.VaultPath, graphMode); err != nil {
		return nil, fmt.Errorf("obsidian: write graph config: %w", err)
	}

	// ── Create vault namespace directory ─────────────────────────────────────
	engRoot := e.engramRoot()
	if err := os.MkdirAll(engRoot, 0755); err != nil {
		return nil, fmt.Errorf("obsidian: create vault dir %q: %w", engRoot, err)
	}
	// Create hub directories up front
	sessionsDir := filepath.Join(engRoot, "_sessions")
	topicsDir := filepath.Join(engRoot, "_topics")
	for _, d := range []string{sessionsDir, topicsDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return nil, fmt.Errorf("obsidian: create dir %q: %w", d, err)
		}
	}

	// ── Read incremental state ────────────────────────────────────────────────
	state, err := ReadState(e.stateFilePath())
	if err != nil {
		return nil, fmt.Errorf("obsidian: read state: %w", err)
	}
	if e.config.Force {
		state = SyncState{
			Files:       make(map[int64]string),
			SessionHubs: make(map[string]string),
			TopicHubs:   make(map[string]string),
		}
	}

	// ── Determine cutoff time ─────────────────────────────────────────────────
	var cutoff time.Time
	if !e.config.Since.IsZero() {
		cutoff = e.config.Since
	} else if state.LastExportAt != "" {
		cutoff, _ = time.Parse(time.RFC3339, state.LastExportAt)
	}

	// ── Fetch data from store ─────────────────────────────────────────────────
	data, err := e.store.Export()
	if err != nil {
		return nil, fmt.Errorf("obsidian: store export: %w", err)
	}

	result := &ExportResult{}

	// ── Handle deleted observations: clean up files ───────────────────────────
	for _, obs := range data.Observations {
		if obs.DeletedAt == nil {
			continue
		}
		relPath, tracked := state.Files[obs.ID]
		if !tracked {
			continue
		}
		absPath := filepath.Join(engRoot, relPath)
		if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
			result.Errors = append(result.Errors, fmt.Errorf("delete %s: %w", absPath, err))
		} else {
			result.Deleted++
			delete(state.Files, obs.ID)
		}
	}

	// ── Build session map for hub generation ─────────────────────────────────
	sessionMap := make(map[string]store.Session)
	for _, s := range data.Sessions {
		sessionMap[s.ID] = s
	}

	// ── Filter and export observations ────────────────────────────────────────
	// sessionObsRefs tracks obs per session for hub generation.
	sessionObsRefs := make(map[string][]ObsRef)
	// topicObsRefs tracks obs per topic prefix for hub generation.
	topicObsRefs := make(map[string][]ObsRef)

	for _, obs := range data.Observations {
		// Skip deleted
		if obs.DeletedAt != nil {
			continue
		}

		// Project filter
		if e.config.Project != "" {
			proj := ""
			if obs.Project != nil {
				proj = *obs.Project
			}
			if proj != e.config.Project {
				continue
			}
		}

		// Incremental filter: skip if updated_at <= cutoff AND already in state
		if !cutoff.IsZero() {
			updatedAt, err := time.Parse(time.RFC3339, obs.UpdatedAt)
			if err == nil && !updatedAt.After(cutoff) {
				// Only skip if already tracked in state (already exported)
				if _, tracked := state.Files[obs.ID]; tracked {
					result.Skipped++
					// Still collect for hub building
					ref := obsToRef(obs)
					if obs.SessionID != "" {
						sessionObsRefs[obs.SessionID] = append(sessionObsRefs[obs.SessionID], ref)
					}
					if prefix := obsTopicPrefix(obs); prefix != "" {
						topicObsRefs[prefix] = append(topicObsRefs[prefix], ref)
					}
					continue
				}
			}
		}

		// Determine target file path — sanitize project and type to prevent
		// path traversal (issue #180): strip separators and dot-dot sequences,
		// then verify the resolved absolute path stays inside engRoot.
		project := "unknown"
		if obs.Project != nil && *obs.Project != "" {
			project = sanitizePathComponent(*obs.Project)
		}
		obsType := sanitizePathComponent(obs.Type)
		slug := Slugify(obs.Title, obs.ID)
		relPath := filepath.Join(project, obsType, slug+".md")
		absDir := filepath.Join(engRoot, project, obsType)
		absPath := filepath.Join(engRoot, relPath)

		// Containment check: reject any path that escapes engRoot after cleaning.
		cleanRoot := filepath.Clean(engRoot)
		cleanPath := filepath.Clean(absPath)
		if !strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator)) {
			result.Errors = append(result.Errors, fmt.Errorf("unsafe path rejected (would escape export root): %s", cleanPath))
			continue
		}

		// Create directory
		if err := os.MkdirAll(absDir, 0755); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("mkdir %s: %w", absDir, err))
			continue
		}

		// Generate markdown content
		content := ObservationToMarkdown(obs)

		// Check idempotency: if file exists and content unchanged, skip
		if existing, err := os.ReadFile(absPath); err == nil {
			if string(existing) == content {
				result.Skipped++
				// Track in state in case it wasn't tracked (e.g. after --force)
				state.Files[obs.ID] = relPath
				ref := obsToRef(obs)
				if obs.SessionID != "" {
					sessionObsRefs[obs.SessionID] = append(sessionObsRefs[obs.SessionID], ref)
				}
				if prefix := obsTopicPrefix(obs); prefix != "" {
					topicObsRefs[prefix] = append(topicObsRefs[prefix], ref)
				}
				continue
			}
			// Content changed → update
			if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("write %s: %w", absPath, err))
				continue
			}
			result.Updated++
		} else {
			// New file
			if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("write %s: %w", absPath, err))
				continue
			}
			result.Created++
		}

		state.Files[obs.ID] = relPath

		// Collect for hub generation
		ref := obsToRef(obs)
		if obs.SessionID != "" {
			sessionObsRefs[obs.SessionID] = append(sessionObsRefs[obs.SessionID], ref)
		}
		if prefix := obsTopicPrefix(obs); prefix != "" {
			topicObsRefs[prefix] = append(topicObsRefs[prefix], ref)
		}
	}

	// ── Generate session hub notes ────────────────────────────────────────────
	for sessionID, refs := range sessionObsRefs {
		if len(refs) == 0 {
			continue
		}
		hubPath := filepath.Join(sessionsDir, sessionID+".md")
		content := SessionHubMarkdown(sessionID, refs)
		if err := os.WriteFile(hubPath, []byte(content), 0644); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("write session hub %s: %w", hubPath, err))
			continue
		}
		state.SessionHubs[sessionID] = filepath.Join("_sessions", sessionID+".md")
		result.HubsCreated++
	}

	// ── Generate topic hub notes ──────────────────────────────────────────────
	for prefix, refs := range topicObsRefs {
		if !ShouldCreateTopicHub(len(refs)) {
			continue
		}
		safeName := strings.ReplaceAll(prefix, "/", "--")
		hubPath := filepath.Join(topicsDir, safeName+".md")
		content := TopicHubMarkdown(prefix, refs)
		if err := os.WriteFile(hubPath, []byte(content), 0644); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("write topic hub %s: %w", hubPath, err))
			continue
		}
		state.TopicHubs[prefix] = filepath.Join("_topics", safeName+".md")
		result.HubsCreated++
	}

	// ── Persist updated state ─────────────────────────────────────────────────
	state.LastExportAt = time.Now().UTC().Format(time.RFC3339)
	state.Version = 1
	if err := WriteState(e.stateFilePath(), state); err != nil {
		return nil, fmt.Errorf("obsidian: write state: %w", err)
	}

	return result, nil
}

// obsToRef converts a store.Observation to a lightweight ObsRef for hub building.
func obsToRef(obs store.Observation) ObsRef {
	topicKey := ""
	if obs.TopicKey != nil {
		topicKey = *obs.TopicKey
	}
	return ObsRef{
		Slug:     Slugify(obs.Title, obs.ID),
		Title:    obs.Title,
		TopicKey: topicKey,
		Type:     obs.Type,
	}
}

// obsTopicPrefix returns the topic prefix for an observation (for hub grouping).
// Returns "" if the observation has no topic_key.
func obsTopicPrefix(obs store.Observation) string {
	if obs.TopicKey == nil || *obs.TopicKey == "" {
		return ""
	}
	return topicPrefix(*obs.TopicKey)
}
