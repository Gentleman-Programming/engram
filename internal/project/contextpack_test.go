package project

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func newContextPackTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func strp(v string) *string { return &v }

func TestBuildContextPack_MarkdownIncludesAllSections(t *testing.T) {
	s := newContextPackTestStore(t)

	if _, _, err := s.UpsertProjectCard(store.UpsertProjectCardParams{
		Slug: "nextcloud", DisplayName: strp("Nextcloud"), Owner: strp("owner@example.com"),
		KnowledgeHubPath: strp("Services/Nextcloud/Nextcloud.md"),
	}); err != nil {
		t.Fatalf("UpsertProjectCard: %v", err)
	}
	if err := s.CreateSession("s1", "nextcloud", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	taskResult, err := s.UpsertTask(store.UpsertTaskParams{
		Project: "nextcloud", JiraKey: strp("PROJ-10336"), SDDChange: strp("proj-10336"),
		Title: strp("Previews return 503"), Kind: strp("incident"), JiraStatus: strp("In Develop"),
	})
	if err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	task := taskResult.Task

	pinnedID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s1", Type: "decision", Title: "PR policy", Content: "PRs target nextcloud29 upstream-first.", Project: "nextcloud",
	})
	if err != nil {
		t.Fatalf("AddObservation (pinned): %v", err)
	}
	if err := s.PinObservation(pinnedID); err != nil {
		t.Fatalf("PinObservation: %v", err)
	}

	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s1", Type: "bugfix", Title: "root cause", Content: "ObjectStoreStorage::writeStream generates non-unique .part names",
		Project: "nextcloud", TopicKey: "incident/PROJ-10336",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	commit := "3f9c2a7d1b8e4c6f0a2d9e1b7c5f3a8d2e6b4c1a"
	if _, err := s.LinkTaskObservation(store.LinkTaskObservationParams{
		Task: task, ObservationID: obsID, GraphRef: strp("ObjectStoreStorage::writeStream"), GraphCommit: &commit,
	}); err != nil {
		t.Fatalf("LinkTaskObservation: %v", err)
	}

	size := int64(1000)
	if _, _, _, err := s.AddEvidence(store.AddEvidenceParams{
		Task: task, Path: "acme/PROJ-10336/shot.png",
		SHA256: "9f2b1c0a7e4d5b6c8a1f3e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f0e1d2c3b",
		Kind: "png", Proves: "preview returns 200", SizeBytes: &size, AttachedJira: true,
	}); err != nil {
		t.Fatalf("AddEvidence: %v", err)
	}

	if _, err := s.SyncRunbookIndex(store.RunbookIndexSyncParams{
		Source: "knowledge-mcp",
		Entries: []store.RunbookIndexEntryInput{{
			ID: "RB-003", VaultPath: "Runbooks/Performance/RB-003.md", Title: "Previews return 503",
			Service: "nextcloud", Category: "performance", Status: "verified",
			Symptoms: []string{"previews return 503"},
		}},
	}); err != nil {
		t.Fatalf("SyncRunbookIndex: %v", err)
	}

	opts := DefaultContextPackOptions()
	pack, markdown, err := BuildContextPack(s, "nextcloud", "PROJ-10336", opts)
	if err != nil {
		t.Fatalf("BuildContextPack: %v", err)
	}

	for _, want := range []string{
		"Context pack — PROJ-10336", "Previews return 503",
		"**Proyecto**", "**Punteros**", "**Observaciones de la tarea",
		"**Evidencia", "**Runbooks candidatos**", "**Referencias**", "_Generado",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("expected markdown to contain %q, got:\n%s", want, markdown)
		}
	}
	if pack.Chars == 0 {
		t.Error("expected pack.Chars > 0")
	}
	if len(pack.Observations) == 0 {
		t.Error("expected at least one observation in the pack")
	}
}

func TestBuildContextPack_UnknownTask(t *testing.T) {
	s := newContextPackTestStore(t)
	if _, _, err := s.UpsertProjectCard(store.UpsertProjectCardParams{Slug: "nextcloud"}); err != nil {
		t.Fatalf("UpsertProjectCard: %v", err)
	}
	_, _, err := BuildContextPack(s, "nextcloud", "PROJ-999", DefaultContextPackOptions())
	if err == nil {
		t.Fatal("expected an error for an unknown task ref")
	}
}

func TestBuildContextPack_SectionFilterAndMaxChars(t *testing.T) {
	s := newContextPackTestStore(t)
	if _, _, err := s.UpsertProjectCard(store.UpsertProjectCardParams{Slug: "nextcloud"}); err != nil {
		t.Fatalf("UpsertProjectCard: %v", err)
	}
	if _, err := s.UpsertTask(store.UpsertTaskParams{
		Project: "nextcloud", JiraKey: strp("PROJ-1"), Title: strp("t"), Kind: strp("bugfix"),
	}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}

	opts := DefaultContextPackOptions()
	opts.Sections = []string{"footer", "header"} // order should still come out canonical: header, footer
	_, markdown, err := BuildContextPack(s, "nextcloud", "PROJ-1", opts)
	if err != nil {
		t.Fatalf("BuildContextPack: %v", err)
	}
	headerIdx := strings.Index(markdown, "Context pack")
	footerIdx := strings.Index(markdown, "_Generado")
	if headerIdx == -1 || footerIdx == -1 || headerIdx > footerIdx {
		t.Fatalf("expected header before footer regardless of requested order, got:\n%s", markdown)
	}
	if strings.Contains(markdown, "**Proyecto**") {
		t.Fatalf("expected card section to be excluded, got:\n%s", markdown)
	}
}
