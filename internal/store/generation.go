package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	sqlite "modernc.org/sqlite"
)

// ErrDBGenerationReplaced is returned when the live SQLite generation this
// Store opened no longer matches the database files on disk. Per the #571
// maintainer direction, the store keeps the single-connection design and never
// reopens transparently: callers must surface this error loudly and restart
// the process. It covers both halves of the #477 failure mode:
//
//   - the database file at dbPath was replaced or removed (prevented before
//     any critical write commits into a dead generation), or
//   - a committed row is invisible to a fresh independent connection
//     (detected after commit, instead of silently acknowledging a write that
//     a future process will never see).
var ErrDBGenerationReplaced = errors.New("database generation was replaced under the running Engram process: restart Engram to reopen the current database")

// openVerifyDB is the seam for the fresh independent verification connection.
// Production uses sql.Open so every verification actually opens the database
// files as they exist on disk right now.
var openVerifyDB = sql.Open

// checkGeneration is the deterministic prevention gate for critical writes:
// it refuses to write when the database file at dbPath is no longer the
// generation this Store opened. It uses os.SameFile, so it works wherever a
// stable file identity exists and silently passes elsewhere (windows paths
// without identity still get post-commit verification).
func (s *Store) checkGeneration() error {
	if s.dbPath == "" || s.dbIdent == nil {
		return nil
	}
	st, err := os.Stat(s.dbPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: database file %s was removed", ErrDBGenerationReplaced, s.dbPath)
		}
		return err
	}
	if !os.SameFile(s.dbIdent, st) {
		return fmt.Errorf("%w: database file %s was replaced", ErrDBGenerationReplaced, s.dbPath)
	}
	return nil
}

// verifyVisible is the post-commit detection gate for critical writes: it
// confirms the just-committed row is observable from a fresh independent
// connection. A fresh open of the current generation always sees committed
// WAL data, so an invisible row or a missing schema means the live connection
// is operating on a divergent generation (#477) and the write must not be
// acknowledged.
func (s *Store) verifyVisible(query string, args ...any) error {
	if s.dbPath == "" {
		return nil
	}
	vdb, err := openVerifyDB("sqlite", s.dbPath)
	if err != nil {
		return err
	}
	defer vdb.Close()
	vdb.SetMaxOpenConns(1)
	if _, err := vdb.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return err
	}

	var one int
	err = vdb.QueryRow(query, args...).Scan(&one)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: committed row is invisible to a fresh connection", ErrDBGenerationReplaced)
	case isNoSuchTableError(err):
		return fmt.Errorf("%w: schema is missing from a fresh connection", ErrDBGenerationReplaced)
	case isSQLiteIOErr(err):
		// The live connection just committed successfully, so failing media
		// would have surfaced there. A fresh reader hitting an I/O error while
		// the live connection succeeds is the divergent-generation pattern
		// (#477): a stale WAL-index that only a restart can clear.
		return fmt.Errorf("%w: a fresh connection cannot read the current generation (%v)", ErrDBGenerationReplaced, err)
	default:
		return err
	}
}

// isNoSuchTableError reports whether err is SQLite's "no such table" error,
// which is how a fresh open of a replaced, unmigrated generation surfaces.
func isNoSuchTableError(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 1 { // SQLITE_ERROR
		return strings.Contains(err.Error(), "no such table")
	}
	return false
}

// isSQLiteIOErr reports whether err is an SQLite I/O-class error. A live
// single connection whose WAL/SHM generation was replaced often surfaces
// these (disk I/O error, short read) instead of failing politely.
func isSQLiteIOErr(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 10 // SQLITE_IOERR class
}

// classifyWriteErr turns I/O-class transaction failures on critical writes
// into ErrDBGenerationReplaced when the evidence says the live connection is
// the stale party: a fresh independent connection can still read the current
// generation while the live one cannot. If the fresh connection is also
// failing, the error is a real disk problem and is returned untouched, so
// genuine hardware failures never get restart guidance.
func (s *Store) classifyWriteErr(err error) error {
	if err == nil || !isSQLiteIOErr(err) {
		return err
	}
	if probe := s.verifyVisible(`SELECT 1 FROM sqlite_master LIMIT 1`); probe == nil {
		return fmt.Errorf("%w: live connection failed (%v) while a fresh connection reads the current generation", ErrDBGenerationReplaced, err)
	}
	return err
}
