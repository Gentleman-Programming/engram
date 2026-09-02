package cloudstore

import (
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v2/internal/sync"
)

// captureTransport is a minimal engramsync.Transport that keeps every chunk the
// cloud exporter produces, in manifest order, so the cloud-side materialization
// can be replayed against real client output.
type captureTransport struct {
	manifest *engramsync.Manifest
	chunks   map[string][]byte
}

func newCaptureTransport() *captureTransport {
	return &captureTransport{
		manifest: &engramsync.Manifest{Version: 1},
		chunks:   map[string][]byte{},
	}
}

func (c *captureTransport) ReadManifest() (*engramsync.Manifest, error) { return c.manifest, nil }

func (c *captureTransport) WriteManifest(m *engramsync.Manifest) error {
	c.manifest = m
	return nil
}

func (c *captureTransport) WriteChunk(chunkID string, data []byte, _ engramsync.ChunkEntry) error {
	stored := make([]byte, len(data))
	copy(stored, data)
	c.chunks[chunkID] = stored
	return nil
}

func (c *captureTransport) ReadChunk(chunkID string) ([]byte, error) {
	return c.chunks[chunkID], nil
}

// dashboardRows converts everything uploaded so far into the rows the cloud
// dashboard reads: the chunk payloads themselves plus the cloud_mutations rows
// WriteChunk materializes from them.
func (c *captureTransport) dashboardRows(t *testing.T, project string) ([]dashboardChunkRow, []dashboardMutationRow) {
	t.Helper()
	chunkRows := make([]dashboardChunkRow, 0, len(c.manifest.Chunks))
	for i, entry := range c.manifest.Chunks {
		payload, ok := c.chunks[entry.ID]
		if !ok {
			t.Fatalf("manifest references chunk %q that was never written", entry.ID)
		}
		chunkRows = append(chunkRows, dashboardChunkRow{
			chunkID:   entry.ID,
			project:   project,
			createdBy: entry.CreatedBy,
			// Manifest timestamps have second precision, so derive a strictly
			// increasing order from the manifest itself.
			createdAt: time.Date(2026, 4, 29, 10, 0, i, 0, time.UTC),
			parsed:    parseMustChunk(t, payload),
		})
	}
	return chunkRows, materializeChunkRowsForDashboard(t, chunkRows)
}

func newPropagationTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func promptListedOnDashboard(t *testing.T, transport *captureTransport, project, content string) bool {
	t.Helper()
	chunkRows, mutationRows := transport.dashboardRows(t, project)
	model, err := buildDashboardReadModelFromRows(chunkRows, mutationRows)
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRows: %v", err)
	}
	cs := &CloudStore{
		dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil },
	}
	prompts, err := cs.ListRecentPrompts(project, "", 50)
	if err != nil {
		t.Fatalf("ListRecentPrompts: %v", err)
	}
	for _, prompt := range prompts {
		if prompt.Content == content {
			return true
		}
	}
	return false
}

// TestPromptDeletePropagatesFromLocalStoreToDashboard is the end-to-end check for
// #837: save a prompt, sync it, delete it locally, sync again, and confirm the
// dashboard stops listing it. It runs the real local store and the real cloud
// exporter; only the network is replaced, and the cloud side is driven through
// the same materialization and dashboard read model a Postgres deployment uses.
func TestPromptDeletePropagatesFromLocalStoreToDashboard(t *testing.T) {
	const project = "proj-e2e-prompt-delete"
	const promptContent = "delete me after sync"

	local := newPropagationTestStore(t)
	transport := newCaptureTransport()
	syncer := engramsync.NewCloudWithTransport(local, transport, project)

	if err := local.EnrollProject(project); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := local.CreateSession("sess-e2e", project, "/tmp/"+project); err != nil {
		t.Fatalf("create session: %v", err)
	}
	promptID, err := local.AddPrompt(store.AddPromptParams{SessionID: "sess-e2e", Content: promptContent, Project: project})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	if result, err := syncer.Export("dev", project); err != nil {
		t.Fatalf("first export: %v", err)
	} else if result.IsEmpty {
		t.Fatal("expected the first export to upload the prompt")
	}
	if !promptListedOnDashboard(t, transport, project, promptContent) {
		t.Fatal("expected the synced prompt to be listed on the dashboard before deletion")
	}

	if err := local.DeletePrompt(promptID); err != nil {
		t.Fatalf("delete prompt: %v", err)
	}
	if result, err := syncer.Export("dev", project); err != nil {
		t.Fatalf("second export: %v", err)
	} else if result.IsEmpty {
		t.Fatal("expected the second export to upload the prompt delete")
	}

	if promptListedOnDashboard(t, transport, project, promptContent) {
		t.Fatal("deleted prompt is still listed on the dashboard after syncing the delete")
	}
}

// TestHardDeletedObservationPropagatesFromLocalStoreToDashboard covers the sibling
// path flagged in #837: DeleteObservation(id, hardDelete=true) removes the row, so
// its delete can only travel as a chunk.Mutations entry.
func TestHardDeletedObservationPropagatesFromLocalStoreToDashboard(t *testing.T) {
	const project = "proj-e2e-obs-hard-delete"

	local := newPropagationTestStore(t)
	transport := newCaptureTransport()
	syncer := engramsync.NewCloudWithTransport(local, transport, project)

	if err := local.EnrollProject(project); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := local.CreateSession("sess-e2e", project, "/tmp/"+project); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := local.AddObservation(store.AddObservationParams{
		SessionID: "sess-e2e",
		Type:      "decision",
		Title:     "delete me after sync",
		Content:   "body",
		Project:   project,
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	if _, err := syncer.Export("dev", project); err != nil {
		t.Fatalf("first export: %v", err)
	}
	if err := local.DeleteObservation(obsID, true); err != nil {
		t.Fatalf("hard-delete observation: %v", err)
	}
	if _, err := syncer.Export("dev", project); err != nil {
		t.Fatalf("second export: %v", err)
	}

	chunkRows, mutationRows := transport.dashboardRows(t, project)
	model, err := buildDashboardReadModelFromRows(chunkRows, mutationRows)
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRows: %v", err)
	}
	cs := &CloudStore{
		dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil },
	}
	observations, err := cs.ListRecentObservations(project, "", 50)
	if err != nil {
		t.Fatalf("ListRecentObservations: %v", err)
	}
	for _, observation := range observations {
		if observation.Title == "delete me after sync" {
			t.Fatalf("hard-deleted observation is still listed on the dashboard: %+v", observation)
		}
	}
}
