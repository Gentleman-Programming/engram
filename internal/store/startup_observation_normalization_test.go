package store

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestStartupMigrationReusesCleanObservationCheck(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO cloud_upgrade_state(project, snapshot_json)
		VALUES ('fixture', '{"captured":true,"project_enrolled":false}')`); err != nil {
		t.Fatal(err)
	}
	var phases bytes.Buffer
	ctx := NewStartupTrace(&phases, time.Now()).Context(t.Context())
	if err := s.runStartupMigrations(ctx, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(phases.String(), " observation-normalization-reused\n") {
		t.Fatalf("clean check was not reused through startup:\n%s", phases.String())
	}
}

func TestObservationNormalizationCheckRejectsInterveningWrite(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("scan", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "scan", Type: "discovery", Title: "fixture", Content: "synthetic observation", Project: "fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	checked := s.checkObservationNormalization(t.Context())
	if checked == nil {
		t.Fatal("healthy store did not get a normalization check")
	}
	defer func() { _ = checked.conn.Close() }()
	if !checked.unchanged(t.Context(), s.generation) {
		t.Fatal("clean check was immediately stale")
	}
	if _, err := s.db.Exec(`UPDATE observations SET scope = '', revision_count = 0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if checked.unchanged(t.Context(), s.generation) {
		t.Fatal("check accepted a write on another connection")
	}
	if err := s.migrateWithObservationCheck(t.Context(), checked); err != nil {
		t.Fatal(err)
	}
	row, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Scope != "project" || row.RevisionCount != 1 {
		t.Fatalf("stale check skipped normalization: scope %q revision %d", row.Scope, row.RevisionCount)
	}
}

func TestObservationScanSlotsBoundWaitAndRelease(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	first := acquireObservationScanSlot(ctx, dir)
	if first == nil {
		t.Fatal("first scan slot unavailable")
	}
	defer func() { _ = first.Close() }()
	second := acquireObservationScanSlot(ctx, dir)
	if second == nil {
		t.Fatal("second scan slot unavailable")
	}
	defer func() { _ = second.Close() }()
	waitCtx, stopWaiting := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer stopWaiting()
	if third := acquireObservationScanSlot(waitCtx, dir); third != nil {
		_ = third.Close()
		t.Fatal("third simultaneous scan acquired a slot")
	}
	if err := unlockMigrationFile(first); err != nil {
		t.Fatal(err)
	}
	reopened := acquireObservationScanSlot(ctx, dir)
	if reopened == nil {
		t.Fatal("released scan slot remained unavailable")
	}
	_ = reopened.Close()
}
