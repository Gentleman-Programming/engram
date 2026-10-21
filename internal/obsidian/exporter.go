package obsidian

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/identityfile"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// ExportConfig holds all CLI flags for the obsidian-export command.
type ExportConfig struct {
	VaultPath   string          // --vault (required): path to the Obsidian vault root
	Project     string          // --project (optional): filter export to a single project
	Org         string          // --org (optional): filter export to a single org (#776)
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

	// Optional per-exporter seams for owned temporary creation and publication.
	createTopicTemp func(string, string) (*os.File, error)
	publishTopicHub func(string, string) error
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

// matchesFilters reports whether obs is in scope for the current export's
// Project and Org filters (#776). An empty filter matches everything for that
// axis; a nil field on the observation is treated as the empty value.
func (e *Exporter) matchesFilters(obs store.Observation) bool {
	if e.config.Project != "" {
		proj := ""
		if obs.Project != nil {
			proj = *obs.Project
		}
		if proj != e.config.Project {
			return false
		}
	}
	if e.config.Org != "" {
		org := ""
		if obs.Org != nil {
			org = *obs.Org
		}
		if org != e.config.Org {
			return false
		}
	}
	return true
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
			PendingRetirements: make(map[int64][]string),
			Files:              make(map[int64]string),
			SessionHubs:        make(map[string]string),
			TopicHubs:          make(map[string]string),
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

	// present lists every observation the store still returns, soft deleted
	// rows included. A tracked note whose ID is absent from it belonged to an
	// observation that was hard deleted: the row is gone from the store, so the
	// soft-delete loop below can never visit it (issue #1900).
	present := make(map[int64]bool, len(data.Observations))
	for _, obs := range data.Observations {
		if e.config.Project == "" || (obs.Project != nil && *obs.Project == e.config.Project) {
			present[obs.ID] = true
		}
	}

	// A project-scoped export only reads that project's rows, so anything it
	// does not see is still tracked on purpose and must not be pruned.
	fullScope := e.config.Project == ""

	// trackedPaths remembers where each note lived before this run so a rename
	// can retire the old file after state.Files is overwritten.
	trackedPaths := make(map[int64]string, len(state.Files))
	for id, relPath := range state.Files {
		trackedPaths[id] = relPath
	}

	// ── Handle deleted observations: clean up files ───────────────────────────
	for _, obs := range data.Observations {
		if obs.DeletedAt == nil {
			continue
		}
		if e.config.Project != "" && (obs.Project == nil || *obs.Project != e.config.Project) {
			continue
		}
		if relPath, tracked := state.Files[obs.ID]; tracked {
			state.PendingRetirements[obs.ID] = append(state.PendingRetirements[obs.ID], relPath)
			delete(state.Files, obs.ID)
		}
	}

	// ── Reconcile notes whose observation was hard deleted ───────────────────
	if fullScope {
		for id, relPath := range state.Files {
			if present[id] {
				continue
			}
			state.PendingRetirements[id] = append(state.PendingRetirements[id], relPath)
			delete(state.Files, id)
		}
	}

	// ── Handle filter exclusions: retire notes that no longer match (#776) ────
	// The exporter mirrors the selected Project/Org filters, so a previously
	// tracked observation that drops out of scope (e.g. a narrower --org on a
	// later run) must lose its note too. Route the drop through the same
	// retirement reconciliation as deletes so the removal is ownership-checked
	// and confined to the export root.
	for _, obs := range data.Observations {
		if obs.DeletedAt != nil {
			continue // handled by the deleted-observation loop above
		}
		relPath, tracked := state.Files[obs.ID]
		if !tracked || e.matchesFilters(obs) {
			continue
		}
		state.PendingRetirements[obs.ID] = append(state.PendingRetirements[obs.ID], relPath)
		delete(state.Files, obs.ID)
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
	// Live counts protect existing hubs even when exporting their notes fails.
	liveSessionCounts := make(map[string]int)
	liveTopicCounts := make(map[string]int)
	addHubRef := func(obs store.Observation) {
		ref := obsToRef(obs)
		if obs.SessionID != "" {
			sessionObsRefs[obs.SessionID] = append(sessionObsRefs[obs.SessionID], ref)
		}
		if prefix := obsTopicPrefix(obs); prefix != "" {
			topicObsRefs[prefix] = append(topicObsRefs[prefix], ref)
		}
	}

	for _, obs := range data.Observations {
		// Skip deleted
		if obs.DeletedAt != nil {
			continue
		}

		// Project and org filters (#776)
		if !e.matchesFilters(obs) {
			continue
		}

		project := "unknown"
		if obs.Project != nil && *obs.Project != "" {
			project = sanitizePathComponent(*obs.Project)
		}
		obsType := sanitizePathComponent(obs.Type)
		relPath := filepath.Join(project, obsType, Slugify(obs.Title, obs.ID)+".md")
		// Desired references survive note/hub write failures.
		if obs.SessionID != "" {
			liveSessionCounts[obs.SessionID]++
		}
		if prefix := obsTopicPrefix(obs); prefix != "" {
			liveTopicCounts[prefix]++
		}

		// Incremental filter: a changed path must retry even below the cutoff.
		if !cutoff.IsZero() {
			updatedAt, err := time.Parse(time.RFC3339, obs.UpdatedAt)
			if err == nil && !updatedAt.After(cutoff) {
				// Only skip if already tracked in state (already exported)
				if old, tracked := state.Files[obs.ID]; tracked && filepath.Clean(old) == relPath {
					result.Skipped++
					addHubRef(obs)
					continue
				}
			}
		}

		// Determine target file path — sanitize project and type to prevent
		// path traversal (issue #180): strip separators and dot-dot sequences,
		// then verify the resolved absolute path stays inside engRoot.
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
				addHubRef(obs)
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
		addHubRef(obs)
	}

	// ── Retire notes left behind by a rename ─────────────────────────────────
	// Keep the latest successful note in Files and retain every superseded
	// path separately until removal succeeds, including across repeated renames.
	// Never retire a normalized path still owned by any tracked live note.
	{
		owned := make(map[string]struct{}, len(state.Files))
		for _, relPath := range state.Files {
			owned[filepath.Clean(relPath)] = struct{}{}
		}
		// Snapshot physical identities once: case-only aliases can name a live
		// file even when their normalized lexical paths differ.
		root, identityErr := os.OpenRoot(engRoot)
		var liveFiles []os.FileInfo
		if identityErr == nil {
			defer func() { _ = root.Close() }()
			for path := range owned {
				info, err := root.Stat(path)
				if err != nil && !os.IsNotExist(err) {
					identityErr = err
					break
				}
				if err == nil {
					liveFiles = append(liveFiles, info)
				}
			}
		}
		if identityErr != nil {
			result.Errors = append(result.Errors, fmt.Errorf("stat live export ownership: %w", identityErr))
		}
		for id, relPath := range state.Files {
			if old, tracked := trackedPaths[id]; tracked && filepath.Clean(old) != filepath.Clean(relPath) {
				state.PendingRetirements[id] = append(state.PendingRetirements[id], old)
			}
		}
		for id, paths := range state.PendingRetirements {
			if !fullScope && !present[id] {
				continue // Never infer project ownership from a stored path.
			}
			remaining := make([]string, 0, len(paths))
			seen := make(map[string]bool)
			for _, path := range paths {
				clean := filepath.Clean(path)
				if seen[clean] {
					continue
				}
				seen[clean] = true
				if _, taken := owned[clean]; taken {
					remaining = append(remaining, path)
					continue
				}
				if identityErr != nil {
					remaining = append(remaining, path)
					continue // Unknown ownership must never permit removal.
				}
				info, err := root.Stat(path)
				if err != nil && !os.IsNotExist(err) {
					result.Errors = append(result.Errors, fmt.Errorf("delete %s: stat retirement ownership: %w", path, err))
					remaining = append(remaining, path)
					continue
				}
				taken := false
				if err == nil {
					for _, live := range liveFiles {
						if os.SameFile(info, live) {
							taken = true
							break
						}
					}
				}
				if taken {
					remaining = append(remaining, path)
					continue
				}
				if err := e.removeExportFile(engRoot, path); err != nil {
					result.Errors = append(result.Errors, err)
					remaining = append(remaining, path)
					continue
				}
				result.Deleted++
			}
			if len(remaining) == 0 {
				delete(state.PendingRetirements, id)
			} else {
				state.PendingRetirements[id] = remaining
			}
		}
	}

	// ── Generate session hub notes ────────────────────────────────────────────
	for sessionID, refs := range sessionObsRefs {
		if len(refs) == 0 {
			continue
		}
		hubPath := filepath.Join(sessionsDir, sessionID+".md")
		cleanSessionsDir := filepath.Clean(sessionsDir)
		cleanHubPath := filepath.Clean(hubPath)
		if !strings.HasPrefix(cleanHubPath, cleanSessionsDir+string(filepath.Separator)) {
			result.Errors = append(result.Errors, fmt.Errorf("unsafe session hub path rejected (would escape sessions dir): %s", cleanHubPath))
			continue
		}
		content := SessionHubMarkdown(sessionID, refs)
		if err := writeSessionHub(sessionsDir, sessionID+".md", content); err != nil {
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
		var writeErr error
		if fullScope {
			writeErr = os.WriteFile(hubPath, []byte(content), 0644)
		} else {
			var created bool
			created, writeErr = e.writeScopedTopicHub(hubPath, []byte(content))
			if writeErr == nil && !created {
				// Shared hubs, including competing creations, must not be replaced
				// with references from only the selected project.
				state.TopicHubs[prefix] = filepath.Join("_topics", safeName+".md")
				continue
			}
		}
		if writeErr != nil {
			result.Errors = append(result.Errors, fmt.Errorf("write topic hub %s: %w", hubPath, writeErr))
			continue
		}
		state.TopicHubs[prefix] = filepath.Join("_topics", safeName+".md")
		result.HubsCreated++
	}

	// ── Retire hubs that no longer reference a live observation ───────────────
	// A session or topic whose observations were all deleted is never rewritten
	// above, so its hub stayed in the vault as an orphan (issue #1900).
	if fullScope {
		for sessionID, relPath := range state.SessionHubs {
			if liveSessionCounts[sessionID] > 0 {
				continue
			}
			if err := e.removeExportFile(engRoot, relPath); err != nil {
				result.Errors = append(result.Errors, err)
				continue
			}
			result.Deleted++
			delete(state.SessionHubs, sessionID)
		}
		for prefix, relPath := range state.TopicHubs {
			if ShouldCreateTopicHub(liveTopicCounts[prefix]) {
				continue
			}
			if err := e.removeExportFile(engRoot, relPath); err != nil {
				result.Errors = append(result.Errors, err)
				continue
			}
			result.Deleted++
			delete(state.TopicHubs, prefix)
		}
	}

	// ── Persist updated state ─────────────────────────────────────────────────
	state.LastExportAt = time.Now().UTC().Format(time.RFC3339)
	state.Version = 1
	if err := WriteState(e.stateFilePath(), state); err != nil {
		return nil, fmt.Errorf("obsidian: write state: %w", err)
	}

	return result, nil
}

// writeScopedTopicHub publishes only a closed, complete owned temporary file.
// A competing destination is preserved; cleanup never touches the final path.
func (e *Exporter) writeScopedTopicHub(path string, content []byte) (created bool, err error) {
	create := e.createTopicTemp
	if create == nil {
		create = os.CreateTemp
	}
	publish := e.publishTopicHub
	if publish == nil {
		publish = identityfile.Publish
	}
	file, err := create(filepath.Dir(path), ".topic-hub-*")
	if err != nil {
		return false, err
	}
	temporary := file.Name()
	defer func() {
		// Linux no-replace rename consumes the source; hard-link publication
		// leaves it behind. In either case only this attempt's temp is ours.
		if cleanupErr := os.Remove(temporary); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
			err = errors.Join(err, fmt.Errorf("clean topic hub temporary %s: %w", temporary, cleanupErr))
		}
	}()
	_, err = file.Write(content)
	if err == nil {
		err = file.Chmod(0644)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	if err = publish(temporary, path); os.IsExist(err) {
		return false, nil
	}
	return err == nil, err
}

// removeExportFile deletes one note tracked by the sync state from the export
// root. relPath is resolved against engRoot and rejected when it escapes that
// root, so a corrupted or hostile state file cannot delete anything outside
// {vault}/engram. A missing file is not an error: the goal is only that the
// note is gone.
func (e *Exporter) removeExportFile(engRoot, relPath string) error {
	cleanPath := filepath.Clean(filepath.Join(engRoot, relPath))
	cleanRoot := filepath.Clean(engRoot)
	if !strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator)) {
		return fmt.Errorf("unsafe path rejected (would escape export root): %s", cleanPath)
	}
	root, err := os.OpenRoot(engRoot)
	if err != nil {
		return fmt.Errorf("open export root for delete %s: %w", cleanPath, err)
	}
	err = root.Remove(relPath)
	_ = root.Close()
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete %s: %w", cleanPath, err)
	}
	return nil
}

func writeSessionHub(sessionsDir, name, content string) error {
	root, err := os.OpenRoot(sessionsDir)
	if err != nil {
		return err
	}
	defer func() {
		_ = root.Close()
	}()

	if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return fmt.Errorf("mkdir session hub parent: %w", err)
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
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
