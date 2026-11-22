package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistentWALReclaimsSpaceWhenLastStoreCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir := t.TempDir()
	s, err := New(FallbackConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_autocheckpoint=0;
		CREATE TABLE wal_retention_fixture (id INTEGER PRIMARY KEY, payload BLOB);
		INSERT INTO wal_retention_fixture VALUES (1,zeroblob(32))`); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, "engram.db")
	reader, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	tx, err := reader.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM wal_retention_fixture").Scan(&count); err != nil || count != 1 {
		t.Fatalf("initial reader snapshot = %d, %v", count, err)
	}

	const payloadBytes = 8 << 20
	if _, err := s.db.ExecContext(ctx, "INSERT INTO wal_retention_fixture VALUES (2,zeroblob(?))", payloadBytes); err != nil {
		t.Fatal(err)
	}
	var busy, logFrames, copiedFrames int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &copiedFrames); err != nil {
		t.Fatal(err)
	}
	if logFrames <= copiedFrames {
		t.Fatalf("pinned reader allowed full checkpoint: busy=%d log=%d copied=%d", busy, logFrames, copiedFrames)
	}
	walPath := dbPath + "-wal"
	if wal, err := os.Stat(walPath); err != nil || wal.Size() < payloadBytes {
		t.Fatalf("active WAL must retain pending writes: %v, %v", wal, err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM wal_retention_fixture").Scan(&count); err != nil || count != 1 {
		t.Fatalf("pinned reader snapshot changed: %d, %v", count, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if wal, err := os.Stat(walPath); err != nil || wal.Size() != 0 {
		t.Fatalf("last Store close should empty the retained WAL: %v, %v", wal, err)
	}
	if _, err := os.Stat(dbPath + "-shm"); err != nil {
		t.Fatalf("expected persistent WAL shared-memory sidecar: %v", err)
	}

	data, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "main-only.db")
	if err := os.WriteFile(copyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	mainOnly, err := sql.Open("sqlite", copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mainOnly.Close() }()
	var payloadSize int
	if err := mainOnly.QueryRowContext(ctx, "SELECT length(payload) FROM wal_retention_fixture WHERE id=2").Scan(&payloadSize); err != nil || payloadSize != payloadBytes {
		t.Fatalf("last Store close didn't checkpoint the committed data: %d, %v", payloadSize, err)
	}
	var integrity string
	if err := mainOnly.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("checkpointed main integrity = %q, %v", integrity, err)
	}
}
