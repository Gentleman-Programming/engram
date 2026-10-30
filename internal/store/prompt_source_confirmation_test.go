package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfirmPromptSourceAttestation(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("session", "alpha", "/work"); err != nil {
		t.Fatal(err)
	}
	id, _, err := s.AddPromptWithResult(AddPromptParams{SessionID: "session", SourceInboxID: "inbox", Project: "beta", Content: "text"})
	if err != nil {
		t.Fatal(err)
	}
	var key string
	if err := s.DB().QueryRow(`SELECT sync_id FROM user_prompts WHERE id=?`, id).Scan(&key); err != nil {
		t.Fatal(err)
	}
	preview, ok, err := s.PreviewPromptSource(key)
	if err != nil || !ok {
		t.Fatalf("preview: %v %v", ok, err)
	}
	one, two := "https://one.example", "https://two.example"
	confirm := func(target string, p PromptSourcePreview, owner string, audit int64) error {
		return s.ConfirmPromptSourceAttestation(target, p, owner, audit)
	}
	count := func() int {
		t.Helper()
		var n int
		if err := s.DB().QueryRow(`SELECT count(*) FROM prompt_source_confirmations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, tc := range []struct {
		name, target, owner string
		preview             PromptSourcePreview
		audit               int64
	}{
		{"blank target", " ", "alpha", preview, 1},
		{"contradictory session", one, "gamma", preview, 1},
		{"blank owner", one, " ", preview, 1},
		{"invalid audit", one, "alpha", preview, 0},
		{"near match ID", one, "alpha", PromptSourcePreview{SessionID: preview.SessionID, SourceInboxID: preview.SourceInboxID, Project: preview.Project, SyncID: key + "-other", Kind: "live"}, 1},
		{"mismatched project", one, "alpha", PromptSourcePreview{SessionID: preview.SessionID, SourceInboxID: preview.SourceInboxID, Project: "gamma", SyncID: key, Kind: "live"}, 1},
		{"mismatched kind", one, "alpha", PromptSourcePreview{SessionID: preview.SessionID, SourceInboxID: preview.SourceInboxID, Project: preview.Project, SyncID: key, Kind: "deleted"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := confirm(tc.target, tc.preview, tc.owner, tc.audit); err == nil || count() != 0 {
				t.Fatalf("accepted invalid confirmation: %v", err)
			}
		})
	}
	if err := confirm(one, preview, "alpha", 42); err != nil {
		t.Fatal(err)
	}
	if err := confirm(one, preview, "alpha", 42); err != nil || count() != 1 {
		t.Fatalf("idempotent replay: %v", err)
	}
	if err := confirm(two, preview, "alpha", 1); err != nil || count() != 2 {
		t.Fatalf("independent target: %v", err)
	}
	if err := confirm(two, preview, "alpha", 1); err != nil {
		t.Fatalf("second target replay: %v", err)
	}
	if err := confirm(one, preview, "alpha", 43); err != nil {
		t.Fatal(err)
	}
	if err := confirm(one, preview, "alpha", 42); err == nil {
		t.Fatal("older same-target replay accepted")
	}
	if err := confirm(two, preview, "alpha", 2); err != nil {
		t.Fatalf("second target blocked by first target ID: %v", err)
	}
	var auditOne, auditTwo int64
	if err := s.DB().QueryRow(`SELECT remote_attestation_id FROM prompt_source_confirmations WHERE remote_target=? AND sync_id=?`, one, key).Scan(&auditOne); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT remote_attestation_id FROM prompt_source_confirmations WHERE remote_target=? AND sync_id=?`, two, key).Scan(&auditTwo); err != nil {
		t.Fatal(err)
	}
	if auditOne != 43 || auditTwo != 2 {
		t.Fatalf("cross-target update: %d %d", auditOne, auditTwo)
	}
	backup, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(backup)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(serialized) || strings.Contains(string(serialized), "prompt_source_confirmations") || strings.Contains(string(serialized), one) {
		t.Fatal("export leaked confirmation")
	}
	imported := newTestStore(t)
	if _, err := imported.Import(backup); err != nil {
		t.Fatal(err)
	}
	var importedCount int
	if err := imported.DB().QueryRow(`SELECT count(*) FROM prompt_source_confirmations`).Scan(&importedCount); err != nil || importedCount != 0 {
		t.Fatalf("imported confirmation: %d %v", importedCount, err)
	}
	if err := s.DeletePrompt(id); err != nil {
		t.Fatal(err)
	}
	deleted, ok, err := s.PreviewPromptSource(key)
	if err != nil || !ok || deleted.Kind != "deleted" {
		t.Fatalf("deleted preview: %+v %v %v", deleted, ok, err)
	}
	if err := confirm(one, deleted, "alpha", 43); err == nil {
		t.Fatal("same-target conflicting kind accepted")
	}
	if err := confirm(one, preview, "alpha", 44); err == nil {
		t.Fatal("stale live preview accepted")
	}
	if err := confirm(one, deleted, "alpha", 44); err != nil {
		t.Fatal(err)
	}
	if err := confirm(two, deleted, "alpha", 2); err == nil {
		t.Fatal("second target same-ID kind replay accepted")
	}
	if err := confirm(two, deleted, "alpha", 3); err != nil {
		t.Fatal(err)
	}
	if err := confirm(one, deleted, "alpha", 43); err == nil {
		t.Fatal("older deleted replay accepted")
	}
	if err := confirm(one, deleted, "gamma", 45); err == nil {
		t.Fatal("changed owner accepted")
	}
	if _, err := s.DB().Exec(`DELETE FROM prompt_tombstones WHERE sync_id=?`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO user_prompts(sync_id,session_id,source_inbox_id,project,content) VALUES (?,?,?,?,?)`, key, preview.SessionID, preview.SourceInboxID, preview.Project, "restored"); err != nil {
		t.Fatal(err)
	}
	if err := confirm(one, deleted, "alpha", 47); err == nil {
		t.Fatal("stale deleted preview accepted")
	}
	if err := confirm(one, preview, "alpha", 44); err == nil {
		t.Fatal("old marker reused for restore")
	}
	if err := confirm(one, preview, "alpha", 48); err != nil {
		t.Fatal(err)
	}
	if err := confirm(one, preview, "alpha", 49); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmDeletedPromptWithoutSession(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DB().Exec(`INSERT INTO prompt_tombstones(sync_id,session_id,source_inbox_id,project) VALUES ('deleted','gone','inbox','beta')`); err != nil {
		t.Fatal(err)
	}
	p, ok, err := s.PreviewPromptSource("deleted")
	if err != nil || !ok {
		t.Fatalf("preview: %v %v", ok, err)
	}
	if err := s.ConfirmPromptSourceAttestation("https://one.example", p, "unique-owner-not-in-export", 987654321); err != nil {
		t.Fatal(err)
	}
	backup, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(backup)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "unique-owner-not-in-export") || strings.Contains(string(data), "987654321") {
		t.Fatal("export leaked owner or audit ID")
	}
	if _, err := s.DB().Exec(`UPDATE prompt_tombstones SET source_inbox_id='changed' WHERE sync_id='deleted'`); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmPromptSourceAttestation("https://one.example", p, "unique-owner-not-in-export", 987654321); err == nil {
		t.Fatal("stale tombstone accepted")
	}
}
