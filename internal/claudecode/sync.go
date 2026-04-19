package claudecode

import (
	"fmt"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// Syncer orchestrates bidirectional sync between Engram and Claude Code memory.
type Syncer struct {
	store            StoreReader
	observationStore ObservationWriter
	exportConfig     ExportConfig
	importConfig     ImportConfig
}

// NewSyncer creates a new bidirectional syncer.
func NewSyncer(s *store.Store, cfg SyncConfig) *Syncer {
	return &Syncer{
		store:            s,
		observationStore: s,
		exportConfig: ExportConfig{
			ClaudeProjectsDir: cfg.ClaudeProjectsDir,
			Project:          cfg.Project,
			DryRun:           cfg.DryRun,
		},
		importConfig: ImportConfig{
			ClaudeProjectsDir: cfg.ClaudeProjectsDir,
			Project:          cfg.Project,
			DryRun:           cfg.DryRun,
		},
	}
}

// FullSync performs a complete bidirectional sync:
// 1. Export Engram observations to Claude Code memory folder
// 2. Import new Claude Code memory files into Engram
func (s *Syncer) FullSync() (*FullSyncResult, error) {
	result := &FullSyncResult{}

	// Phase 1: Export Engram → Claude Code
	exporter := NewExporter(s.store, s.exportConfig)
	exportResult, err := exporter.Export()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("export: %w", err))
	}
	result.ExportResult = exportResult

	// Phase 2: Import Claude Code → Engram
	importer := NewImporter(s.observationStore, s.importConfig)
	importResult, err := importer.Import()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("import: %w", err))
	}
	result.ImportResult = importResult

	return result, nil
}

// ExportOnly exports Engram observations to Claude Code memory folder.
func (s *Syncer) ExportOnly() (*ExportResult, error) {
	exporter := NewExporter(s.store, s.exportConfig)
	return exporter.Export()
}

// ImportOnly imports Claude Code memory files into Engram.
func (s *Syncer) ImportOnly() (*ImportResult, error) {
	importer := NewImporter(s.observationStore, s.importConfig)
	return importer.Import()
}
