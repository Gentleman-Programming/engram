package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"
)

const observationNormalizationNeeded = `SELECT EXISTS (
	SELECT 1 FROM observations
	WHERE scope IS NULL OR scope = ''
		OR topic_key = ''
		OR revision_count IS NULL OR revision_count < 1
		OR duplicate_count IS NULL OR duplicate_count < 1
		OR updated_at IS NULL OR updated_at = ''
		OR sync_id IS NULL OR sync_id = ''
)`

// SQLite's data_version is comparable only on the same physical connection.
// Keep that connection checked out until the migration uses or discards the result.
type observationNormalizationCheck struct {
	conn    *sql.Conn
	version int64
}

func (c *observationNormalizationCheck) unchanged(ctx context.Context, generation *databaseGeneration) bool {
	if c == nil || generation.check() != nil {
		return false
	}
	var version int64
	return c.conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&version) == nil &&
		version == c.version && generation.check() == nil
}

func (s *Store) checkObservationNormalization(ctx context.Context) *observationNormalizationCheck {
	if s.readinessDB == nil {
		return nil
	}
	// Reading ahead is optional. Fall back to the existing serialized repair
	// if slots stay busy or the read cannot finish in its own small budget.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	slot := acquireObservationScanSlot(ctx, s.cfg.DataDir)
	if slot == nil {
		return nil
	}
	defer func() {
		_ = unlockMigrationFile(slot)
		_ = slot.Close()
	}()
	conn, err := s.readinessDB.Conn(ctx)
	if err != nil {
		return nil
	}
	check := &observationNormalizationCheck{conn: conn}
	var dirty bool
	if conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&check.version) != nil ||
		conn.QueryRowContext(ctx, observationNormalizationNeeded).Scan(&dirty) != nil ||
		dirty || !check.unchanged(ctx, s.generation) {
		_ = conn.Close()
		return nil
	}
	return check
}

// Limit the simultaneous SQLite scans across processes. The handles are
// released before taking the migration lock. Leave the files in place so all
// processes lock the same file identities.
func acquireObservationScanSlot(ctx context.Context, directory string) (selected *os.File) {
	var slots []*os.File
	defer func() {
		for _, slot := range slots {
			if slot != selected {
				_ = slot.Close()
			}
		}
	}()
	for _, name := range [...]string{".observation-scan-0.lock", ".observation-scan-1.lock"} {
		if ctx.Err() != nil {
			return nil
		}
		slot, err := os.OpenFile(filepath.Join(directory, name), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil
		}
		slots = append(slots, slot)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		for _, slot := range slots {
			acquired, err := tryLockMigrationFile(slot)
			if acquired {
				return slot
			}
			if err != nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}
