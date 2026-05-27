package obsidian

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// ─── Mock StoreReader ─────────────────────────────────────────────────────────

// mockStore implements StoreReader for testing.
type mockStore struct {
	exportData *store.ExportData
	exportErr  error
	stats      *store.Stats
}

func (m *mockStore) Export() (*store.ExportData, error) {
	return m.exportData, m.exportErr
}

func (m *mockStore) Stats() *store.Stats {
	return m.stats
}

// ─── Task 2.1: TestNewExporter / TestExportConfig ─────────────────────────────

func TestNewExporter(t *testing.T) {
	t.Run("missing vault path returns error on Export", func(t *testing.T) {
		ms := &mockStore{
			exportData: &store.ExportData{
				Sessions:     []store.Session{},
				Observations: []store.Observation{},
				Prompts:      []store.Prompt{},
			},
		}
		cfg := ExportConfig{VaultPath: ""} // missing vault
		exp := NewExporter(ms, cfg)
		_, err := exp.Export()
		if err == nil {
			t.Fatal("expected error for missing vault path, got nil")
		}
	})

	t.Run("valid vault path constructs exporter without error", func(t *testing.T) {
		ms := &mockStore{}
		cfg := ExportConfig{VaultPath: "/tmp/test-vault"}
		exp := NewExporter(ms, cfg)
		if exp == nil {
			t.Fatal("expected non-nil Exporter, got nil")
		}
	})

	t.Run("StoreReader mock satisfies interface", func(t *testing.T) {
		var _ StoreReader = &mockStore{}
	})
}

func TestExportConfig(t *testing.T) {
	t.Run("zero value config has empty VaultPath", func(t *testing.T) {
		cfg := ExportConfig{}
		if cfg.VaultPath != "" {
			t.Errorf("expected empty VaultPath, got %q", cfg.VaultPath)
		}
	})

	t.Run("config fields are set correctly", func(t *testing.T) {
		cfg := ExportConfig{
			VaultPath: "/my/vault",
			Project:   "engram",
			Limit:     50,
		}
		if cfg.VaultPath != "/my/vault" {
			t.Errorf("VaultPath: got %q, want %q", cfg.VaultPath, "/my/vault")
		}
		if cfg.Project != "engram" {
			t.Errorf("Project: got %q, want %q", cfg.Project, "engram")
		}
		if cfg.Limit != 50 {
			t.Errorf("Limit: got %d, want 50", cfg.Limit)
		}
	})
}

// ─── Task 2.3: TestIncrementalExport ─────────────────────────────────────────

func TestIncrementalExport(t *testing.T) {
	t.Run("first export with no state file exports all observations", func(t *testing.T) {
		dir := t.TempDir()
		ms := &mockStore{
			exportData: &store.ExportData{
				Sessions: []store.Session{
					{ID: "sess-1", Project: "eng"},
				},
				Observations: []store.Observation{
					{
						ID:        1,
						SessionID: "sess-1",
						Type:      "bugfix",
						Title:     "Fixed auth",
						Content:   "Fixed auth bug",
						Scope:     "project",
						CreatedAt: "2026-01-01T10:00:00Z",
						UpdatedAt: "2026-01-01T10:00:00Z",
						Project:   strPtr("eng"),
					},
					{
						ID:        2,
						SessionID: "sess-1",
						Type:      "decision",
						Title:     "Use JWT",
						Content:   "Decided to use JWT",
						Scope:     "project",
						CreatedAt: "2026-01-02T10:00:00Z",
						UpdatedAt: "2026-01-02T10:00:00Z",
						Project:   strPtr("eng"),
					},
				},
				Prompts: []store.Prompt{},
			},
		}
		cfg := ExportConfig{VaultPath: dir}
		exp := NewExporter(ms, cfg)
		result, err := exp.Export()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Created != 2 {
			t.Errorf("Created: got %d, want 2", result.Created)
		}
	})

	t.Run("incremental export with state file exports only new observations", func(t *testing.T) {
		dir := t.TempDir()

		// Seed state with obs ID=1 already exported
		existingState := SyncState{
			LastExportAt: "2026-01-01T12:00:00Z",
			Files:        map[int64]string{1: "eng/bugfix/fixed-auth-1.md"},
			SessionHubs:  map[string]string{},
			TopicHubs:    map[string]string{},
			Version:      1,
		}
		stateFile := dir + "/engram/.engram-sync-state.json"
		if err := mkdirAll(dir + "/engram"); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := WriteState(stateFile, existingState); err != nil {
			t.Fatalf("setup WriteState: %v", err)
		}

		ms := &mockStore{
			exportData: &store.ExportData{
				Sessions: []store.Session{
					{ID: "sess-1", Project: "eng"},
				},
				Observations: []store.Observation{
					{
						// Old obs — updated_at before LastExportAt → should be skipped
						ID:        1,
						SessionID: "sess-1",
						Type:      "bugfix",
						Title:     "Fixed auth",
						Content:   "Fixed auth bug",
						Scope:     "project",
						CreatedAt: "2026-01-01T10:00:00Z",
						UpdatedAt: "2026-01-01T10:00:00Z",
						Project:   strPtr("eng"),
					},
					{
						// New obs — updated_at after LastExportAt → should be exported
						ID:        2,
						SessionID: "sess-1",
						Type:      "decision",
						Title:     "Use JWT",
						Content:   "Decided to use JWT",
						Scope:     "project",
						CreatedAt: "2026-02-01T10:00:00Z",
						UpdatedAt: "2026-02-01T10:00:00Z",
						Project:   strPtr("eng"),
					},
				},
				Prompts: []store.Prompt{},
			},
		}
		cfg := ExportConfig{VaultPath: dir}
		exp := NewExporter(ms, cfg)
		result, err := exp.Export()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Created != 1 {
			t.Errorf("Created: got %d, want 1 (only new obs)", result.Created)
		}
		if result.Skipped != 1 {
			t.Errorf("Skipped: got %d, want 1 (old obs)", result.Skipped)
		}
	})
}

// ─── Task 2.5: TestDeletedObsRemoved ─────────────────────────────────────────

func TestDeletedObsRemoved(t *testing.T) {
	t.Run("obs deleted after first export is removed from vault", func(t *testing.T) {
		dir := t.TempDir()

		// Create the vault engram dir
		engDir := dir + "/engram"
		if err := mkdirAll(engDir); err != nil {
			t.Fatalf("setup: %v", err)
		}

		// Create a file for obs ID=3 (simulating it was previously exported)
		obsDir := engDir + "/eng/bugfix"
		if err := mkdirAll(obsDir); err != nil {
			t.Fatalf("setup: %v", err)
		}
		obsFile := obsDir + "/some-fix-3.md"
		if err := writeFile(obsFile, []byte("old content")); err != nil {
			t.Fatalf("setup writeFile: %v", err)
		}

		// Seed state with obs ID=3 tracked
		existingState := SyncState{
			LastExportAt: "2026-01-01T12:00:00Z",
			Files:        map[int64]string{3: "eng/bugfix/some-fix-3.md"},
			SessionHubs:  map[string]string{},
			TopicHubs:    map[string]string{},
			Version:      1,
		}
		stateFile := engDir + "/.engram-sync-state.json"
		if err := WriteState(stateFile, existingState); err != nil {
			t.Fatalf("setup WriteState: %v", err)
		}

		deletedAt := "2026-02-01T00:00:00Z"
		ms := &mockStore{
			exportData: &store.ExportData{
				Sessions: []store.Session{},
				Observations: []store.Observation{
					{
						// Soft-deleted obs — DeletedAt != nil → remove from vault
						ID:        3,
						SessionID: "sess-1",
						Type:      "bugfix",
						Title:     "Some fix",
						Content:   "some fix",
						Scope:     "project",
						CreatedAt: "2026-01-01T10:00:00Z",
						UpdatedAt: "2026-02-01T00:00:00Z",
						DeletedAt: &deletedAt,
						Project:   strPtr("eng"),
					},
				},
				Prompts: []store.Prompt{},
			},
		}

		cfg := ExportConfig{VaultPath: dir}
		exp := NewExporter(ms, cfg)
		result, err := exp.Export()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Deleted != 1 {
			t.Errorf("Deleted: got %d, want 1", result.Deleted)
		}

		// File must no longer exist
		if fileExists(obsFile) {
			t.Errorf("expected %s to be deleted, but it still exists", obsFile)
		}
	})
}

// ─── Task 2.7: TestProjectFilter ─────────────────────────────────────────────

func TestProjectFilter(t *testing.T) {
	t.Run("--project flag limits exported observations to matching project", func(t *testing.T) {
		dir := t.TempDir()
		ms := &mockStore{
			exportData: &store.ExportData{
				Sessions: []store.Session{
					{ID: "sess-1", Project: "eng"},
					{ID: "sess-2", Project: "gentle-ai"},
				},
				Observations: []store.Observation{
					{
						ID:        1,
						SessionID: "sess-1",
						Type:      "bugfix",
						Title:     "Eng fix",
						Content:   "eng fix content",
						Scope:     "project",
						CreatedAt: "2026-01-01T10:00:00Z",
						UpdatedAt: "2026-01-01T10:00:00Z",
						Project:   strPtr("eng"),
					},
					{
						ID:        2,
						SessionID: "sess-2",
						Type:      "decision",
						Title:     "AI decision",
						Content:   "gentle-ai content",
						Scope:     "project",
						CreatedAt: "2026-01-02T10:00:00Z",
						UpdatedAt: "2026-01-02T10:00:00Z",
						Project:   strPtr("gentle-ai"),
					},
				},
				Prompts: []store.Prompt{},
			},
		}
		cfg := ExportConfig{VaultPath: dir, Project: "eng"}
		exp := NewExporter(ms, cfg)
		result, err := exp.Export()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Created != 1 {
			t.Errorf("Created: got %d, want 1 (only eng project)", result.Created)
		}
		// The gentle-ai obs must NOT have a file
		aiFile := dir + "/engram/gentle-ai/decision/ai-decision-2.md"
		if fileExists(aiFile) {
			t.Errorf("unexpected file for filtered-out project: %s", aiFile)
		}
	})

	t.Run("no project filter exports all projects", func(t *testing.T) {
		dir := t.TempDir()
		ms := &mockStore{
			exportData: &store.ExportData{
				Sessions: []store.Session{
					{ID: "sess-1", Project: "eng"},
					{ID: "sess-2", Project: "gentle-ai"},
				},
				Observations: []store.Observation{
					{
						ID:        1,
						SessionID: "sess-1",
						Type:      "bugfix",
						Title:     "Eng fix",
						Content:   "eng fix",
						Scope:     "project",
						CreatedAt: "2026-01-01T10:00:00Z",
						UpdatedAt: "2026-01-01T10:00:00Z",
						Project:   strPtr("eng"),
					},
					{
						ID:        2,
						SessionID: "sess-2",
						Type:      "decision",
						Title:     "AI decision",
						Content:   "ai decision",
						Scope:     "project",
						CreatedAt: "2026-01-02T10:00:00Z",
						UpdatedAt: "2026-01-02T10:00:00Z",
						Project:   strPtr("gentle-ai"),
					},
				},
				Prompts: []store.Prompt{},
			},
		}
		cfg := ExportConfig{VaultPath: dir, Project: ""} // no filter
		exp := NewExporter(ms, cfg)
		result, err := exp.Export()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Created != 2 {
			t.Errorf("Created: got %d, want 2 (all projects)", result.Created)
		}
	})
}

// ─── Task 2.9: TestFullExportPipeline ────────────────────────────────────────

func TestFullExportPipeline(t *testing.T) {
	t.Run("full pipeline: 3 projects, 5 sessions, 20 obs, 1 deleted — dir structure + state file", func(t *testing.T) {
		dir := t.TempDir()

		deletedAt := "2026-03-15T00:00:00Z"
		ms := &mockStore{
			exportData: buildPipelineFixtures(deletedAt),
		}

		cfg := ExportConfig{VaultPath: dir}
		exp := NewExporter(ms, cfg)
		result, err := exp.Export()
		if err != nil {
			t.Fatalf("Export() error: %v", err)
		}

		// 19 live obs created (1 deleted — ignored on first run since not in state)
		if result.Created != 19 {
			t.Errorf("Created: got %d, want 19", result.Created)
		}

		// State file must exist
		stateFile := dir + "/engram/.engram-sync-state.json"
		if !fileExists(stateFile) {
			t.Errorf("state file not found at %s", stateFile)
		}

		// Read back state and verify 19 entries
		state, err := ReadState(stateFile)
		if err != nil {
			t.Fatalf("ReadState: %v", err)
		}
		if len(state.Files) != 19 {
			t.Errorf("state.Files: got %d entries, want 19", len(state.Files))
		}
		if state.LastExportAt == "" {
			t.Error("state.LastExportAt must not be empty after export")
		}

		// Session hub notes: 5 sessions with obs → 5 hub files
		sessionHubsDir := dir + "/engram/_sessions"
		if !dirExists(sessionHubsDir) {
			t.Errorf("_sessions dir not found: %s", sessionHubsDir)
		}
		hubCount := countFilesInDir(t, sessionHubsDir)
		if hubCount != 5 {
			t.Errorf("session hubs: got %d, want 5", hubCount)
		}

		// Topic hubs: only prefixes with ≥2 obs
		topicHubsDir := dir + "/engram/_topics"
		if !dirExists(topicHubsDir) {
			t.Errorf("_topics dir not found: %s", topicHubsDir)
		}
	})

	t.Run("second export is incremental — only new obs written", func(t *testing.T) {
		dir := t.TempDir()

		deletedAt := "2026-03-15T00:00:00Z"
		ms := &mockStore{
			exportData: buildPipelineFixtures(deletedAt),
		}

		// First export
		cfg := ExportConfig{VaultPath: dir}
		exp := NewExporter(ms, cfg)
		first, err := exp.Export()
		if err != nil {
			t.Fatalf("first Export() error: %v", err)
		}
		if first.Created != 19 {
			t.Fatalf("first export Created: got %d, want 19", first.Created)
		}

		// Second export with same data → nothing new
		exp2 := NewExporter(ms, cfg)
		second, err := exp2.Export()
		if err != nil {
			t.Fatalf("second Export() error: %v", err)
		}
		if second.Created != 0 {
			t.Errorf("second export Created: got %d, want 0 (incremental)", second.Created)
		}
		if second.Skipped != 19 {
			t.Errorf("second export Skipped: got %d, want 19", second.Skipped)
		}
	})
}

// ─── Task 2.1: TestExporterCallsGraphConfig ───────────────────────────────────

// TestExporterCallsGraphConfig verifies REQ-WATCH-06:
// - ExportConfig{GraphConfig: GraphConfigForce} causes Export() to create .obsidian/graph.json
// - ExportConfig{GraphConfig: GraphConfigSkip} causes Export() to NOT create .obsidian/graph.json
func TestExporterCallsGraphConfig(t *testing.T) {
	t.Run("GraphConfigForce creates .obsidian/graph.json before returning", func(t *testing.T) {
		dir := t.TempDir()
		ms := &mockStore{
			exportData: &store.ExportData{
				Sessions:     []store.Session{},
				Observations: []store.Observation{},
				Prompts:      []store.Prompt{},
			},
		}
		cfg := ExportConfig{
			VaultPath:   dir,
			GraphConfig: GraphConfigForce,
		}
		exp := NewExporter(ms, cfg)
		_, err := exp.Export()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		graphPath := dir + "/.obsidian/graph.json"
		if !fileExists(graphPath) {
			t.Errorf("expected .obsidian/graph.json to be created with GraphConfigForce, but it does not exist")
		}
	})

	t.Run("GraphConfigSkip does NOT create .obsidian/graph.json", func(t *testing.T) {
		dir := t.TempDir()
		ms := &mockStore{
			exportData: &store.ExportData{
				Sessions:     []store.Session{},
				Observations: []store.Observation{},
				Prompts:      []store.Prompt{},
			},
		}
		cfg := ExportConfig{
			VaultPath:   dir,
			GraphConfig: GraphConfigSkip,
		}
		exp := NewExporter(ms, cfg)
		_, err := exp.Export()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		graphPath := dir + "/.obsidian/graph.json"
		if fileExists(graphPath) {
			t.Errorf("expected .obsidian/graph.json to NOT be created with GraphConfigSkip, but it exists")
		}
	})

	t.Run("zero value GraphConfig (empty string) defaults to skip — backward compat", func(t *testing.T) {
		dir := t.TempDir()
		ms := &mockStore{
			exportData: &store.ExportData{
				Sessions:     []store.Session{},
				Observations: []store.Observation{},
				Prompts:      []store.Prompt{},
			},
		}
		cfg := ExportConfig{
			VaultPath: dir,
			// GraphConfig intentionally zero value (empty string)
		}
		exp := NewExporter(ms, cfg)
		_, err := exp.Export()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		graphPath := dir + "/.obsidian/graph.json"
		// Empty string defaults to skip — no graph.json created
		if fileExists(graphPath) {
			t.Errorf("expected .obsidian/graph.json to NOT be created with zero-value GraphConfig, but it exists")
		}
	})
}

// ─── Security: TestPathTraversalPrevention (Issue #180) ──────────────────────

// TestPathTraversalPrevention asserts that malicious Project or Type values
// containing path traversal sequences (e.g. "../../etc") or absolute paths
// cannot produce output files that escape the {vault}/engram/ export root.
func TestPathTraversalPrevention(t *testing.T) {
	traversalCases := []struct {
		name    string
		project string
		obsType string
	}{
		{
			name:    "dotdot in project",
			project: "../../etc",
			obsType: "bugfix",
		},
		{
			name:    "dotdot in type",
			project: "myproject",
			obsType: "../../etc",
		},
		{
			name:    "dotdot in both",
			project: "../../tmp",
			obsType: "../../../var",
		},
		{
			name:    "absolute path in project",
			project: "/etc",
			obsType: "bugfix",
		},
		{
			name:    "absolute path in type",
			project: "myproject",
			obsType: "/etc",
		},
		{
			name:    "nested traversal",
			project: "foo/../../../secret",
			obsType: "bugfix",
		},
	}

	for _, tc := range traversalCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			ms := &mockStore{
				exportData: &store.ExportData{
					Sessions: []store.Session{
						{ID: "sess-1", Project: tc.project},
					},
					Observations: []store.Observation{
						{
							ID:        1,
							SessionID: "sess-1",
							Type:      tc.obsType,
							Title:     "Traversal Test",
							Content:   "malicious content",
							Scope:     "project",
							CreatedAt: "2026-01-01T10:00:00Z",
							UpdatedAt: "2026-01-01T10:00:00Z",
							Project:   strPtr(tc.project),
						},
					},
					Prompts: []store.Prompt{},
				},
			}

			cfg := ExportConfig{VaultPath: dir}
			exp := NewExporter(ms, cfg)
			result, err := exp.Export()
			if err != nil {
				t.Fatalf("Export() returned unexpected error: %v", err)
			}

			// The observation must either be sanitized (created inside engRoot)
			// or skipped due to invalid path — it must NOT escape the export root.
			engRoot := dir + "/engram"

			// Walk everything that was written and assert containment.
			err = walkDir(dir, func(path string) {
				// engRoot itself and state/hub files are fine — only check obs files.
				if !isContainedIn(path, engRoot) {
					t.Errorf("file written OUTSIDE export root: %s (engRoot=%s)", path, engRoot)
				}
			})
			if err != nil {
				t.Fatalf("walkDir: %v", err)
			}

			// If a file was created, verify its absolute clean path is under engRoot.
			if result != nil && result.Created > 0 {
				// Retrieve the state to find which relative path was recorded.
				stateFile := engRoot + "/.engram-sync-state.json"
				state, readErr := ReadState(stateFile)
				if readErr != nil {
					t.Fatalf("ReadState: %v", readErr)
				}
				for id, relPath := range state.Files {
					absPath := filepath.Join(engRoot, relPath)
					cleanAbs := filepath.Clean(absPath)
					cleanRoot := filepath.Clean(engRoot)
					if !strings.HasPrefix(cleanAbs, cleanRoot+string(filepath.Separator)) &&
						cleanAbs != cleanRoot {
						t.Errorf("obs %d: state path escapes export root: %s (engRoot=%s)", id, cleanAbs, cleanRoot)
					}
				}
			}
		})
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// buildPipelineFixtures creates a fixture ExportData with 3 projects, 5 sessions,
// 20 observations (1 deleted), and topic_key clusters for hub testing.
func buildPipelineFixtures(deletedAt string) *store.ExportData {
	sessions := []store.Session{
		{ID: "sess-alpha", Project: "project-a"},
		{ID: "sess-beta", Project: "project-a"},
		{ID: "sess-gamma", Project: "project-b"},
		{ID: "sess-delta", Project: "project-b"},
		{ID: "sess-epsilon", Project: "project-c"},
	}

	// Build 20 observations:
	//   - obs 1..4: project-a, sess-alpha, topic "sdd/plugin" (cluster ≥2)
	//   - obs 5..8: project-a, sess-beta, topic "sdd/design" (same cluster "sdd")
	//   - obs 9..12: project-b, sess-gamma, topic "auth/jwt" (cluster ≥2)
	//   - obs 13..16: project-b, sess-delta, topic "auth/sessions"
	//   - obs 17..19: project-c, sess-epsilon, no topic (singleton project)
	//   - obs 20: project-a, deleted (should not be exported / creates delete on 2nd run)
	observations := []store.Observation{
		makeObs(1, "sess-alpha", "project-a", "architecture", "SDD Plugin Arch", "sdd/plugin", "2026-01-01T01:00:00Z"),
		makeObs(2, "sess-alpha", "project-a", "decision", "SDD Plugin Decision", "sdd/plugin", "2026-01-01T02:00:00Z"),
		makeObs(3, "sess-alpha", "project-a", "bugfix", "SDD Plugin Fix", "sdd/plugin", "2026-01-01T03:00:00Z"),
		makeObs(4, "sess-alpha", "project-a", "learning", "SDD Plugin Learning", "sdd/plugin", "2026-01-01T04:00:00Z"),
		makeObs(5, "sess-beta", "project-a", "architecture", "SDD Design Arch", "sdd/design", "2026-01-02T01:00:00Z"),
		makeObs(6, "sess-beta", "project-a", "decision", "SDD Design Decision", "sdd/design", "2026-01-02T02:00:00Z"),
		makeObs(7, "sess-beta", "project-a", "bugfix", "SDD Design Fix", "sdd/design", "2026-01-02T03:00:00Z"),
		makeObs(8, "sess-beta", "project-a", "pattern", "SDD Design Pattern", "sdd/design", "2026-01-02T04:00:00Z"),
		makeObs(9, "sess-gamma", "project-b", "architecture", "Auth JWT Arch", "auth/jwt", "2026-01-03T01:00:00Z"),
		makeObs(10, "sess-gamma", "project-b", "decision", "Auth JWT Decision", "auth/jwt", "2026-01-03T02:00:00Z"),
		makeObs(11, "sess-gamma", "project-b", "bugfix", "Auth JWT Fix", "auth/jwt", "2026-01-03T03:00:00Z"),
		makeObs(12, "sess-gamma", "project-b", "learning", "Auth JWT Learning", "auth/jwt", "2026-01-03T04:00:00Z"),
		makeObs(13, "sess-delta", "project-b", "architecture", "Auth Sessions Arch", "auth/sessions", "2026-01-04T01:00:00Z"),
		makeObs(14, "sess-delta", "project-b", "decision", "Auth Sessions Decision", "auth/sessions", "2026-01-04T02:00:00Z"),
		makeObs(15, "sess-delta", "project-b", "bugfix", "Auth Sessions Fix", "auth/sessions", "2026-01-04T03:00:00Z"),
		makeObs(16, "sess-delta", "project-b", "pattern", "Auth Sessions Pattern", "auth/sessions", "2026-01-04T04:00:00Z"),
		makeObs(17, "sess-epsilon", "project-c", "architecture", "PC Arch", "", "2026-01-05T01:00:00Z"),
		makeObs(18, "sess-epsilon", "project-c", "decision", "PC Decision", "", "2026-01-05T02:00:00Z"),
		makeObs(19, "sess-epsilon", "project-c", "bugfix", "PC Fix", "", "2026-01-05T03:00:00Z"),
		// obs 20: deleted
		{
			ID:        20,
			SessionID: "sess-alpha",
			Type:      "bugfix",
			Title:     "Deleted Obs",
			Content:   "this was deleted",
			Scope:     "project",
			CreatedAt: "2026-01-01T00:00:00Z",
			UpdatedAt: deletedAt,
			DeletedAt: &deletedAt,
			Project:   strPtr("project-a"),
		},
	}

	return &store.ExportData{
		Sessions:     sessions,
		Observations: observations,
		Prompts:      []store.Prompt{},
	}
}

// makeObs is a test helper to build store.Observation values concisely.
func makeObs(id int64, sessionID, project, obsType, title, topicKey, ts string) store.Observation {
	obs := store.Observation{
		ID:        id,
		SessionID: sessionID,
		Type:      obsType,
		Title:     title,
		Content:   title + " content",
		Scope:     "project",
		CreatedAt: ts,
		UpdatedAt: ts,
		Project:   strPtr(project),
	}
	if topicKey != "" {
		obs.TopicKey = strPtr(topicKey)
	}
	return obs
}
