package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSyncedChunksPreservesIDsAndTargets(t *testing.T) {
	s := newTestStore(t)
	ids := []string{"plain", "", "quotes\"\\\n\t☃", "nul\x00inside", "invalid-\xff-byte"}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		if err := s.RecordSyncedChunkForTarget(" CLOUD:Audit ", id); err != nil {
			t.Fatal(err)
		}
		want[id] = true
	}
	if err := s.RecordSyncedChunk("local-only"); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"cloud:audit", " CLOUD:AUDIT "} {
		got, err := s.GetSyncedChunksForTarget(target)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("target %q: got%#v want%#v err%v", target, got, want, err)
		}
	}
	for _, target := range []string{"", " local "} {
		got, err := s.GetSyncedChunksForTarget(target)
		if err != nil || !reflect.DeepEqual(got, map[string]bool{"local-only": true}) {
			t.Fatalf("local target %q: %v %v", target, got, err)
		}
	}
	got, err := s.GetSyncedChunksForTarget("cloud:other")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty target: %v %v", got, err)
	}
}

func TestSyncedChunksBoundsFilesystemChecks(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 0 UNION ALL SELECT x+1 FROM n WHERE x < 255)
		INSERT INTO sync_chunks(target_key,chunk_id) SELECT 'cloud:audit', printf('chunk-%03d',x) FROM n`); err != nil {
		t.Fatal(err)
	}
	original := statFile
	defer func() { statFile = original }()
	calls := 0
	statFile = func(path string) (os.FileInfo, error) {
		calls++
		return original(path)
	}
	if _, err := s.GetSyncedChunksForTarget("cloud:empty"); err != nil {
		t.Fatal(err)
	}
	emptyCalls := calls
	calls = 0
	got, err := s.GetSyncedChunksForTarget("cloud:audit")
	if err != nil || len(got) != 256 {
		t.Fatalf("history: count%d err%v", len(got), err)
	}
	// Identity checks protect the read, not each ID transferred. Allow a
	// fixed extra result/EOF boundary, but not work proportional to history.
	if emptyCalls == 0 || calls > emptyCalls+12 {
		t.Fatalf("filesystem checks scaled with IDs: empty=%d history=%d", emptyCalls, calls)
	}
}

func TestSyncedChunksRejectsChangedGeneration(t *testing.T) {
	for _, timing := range []string{"before-query", "after-scan"} {
		t.Run(timing, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.RecordSyncedChunk("chunk"); err != nil {
				t.Fatal(err)
			}
			replacement := filepath.Join(t.TempDir(), "replacement")
			if err := os.WriteFile(replacement, []byte("other identity"), 0600); err != nil {
				t.Fatal(err)
			}
			original := statFile
			defer func() { statFile = original }()
			changed := timing == "before-query"
			statFile = func(path string) (os.FileInfo, error) {
				if changed && path == s.generation.paths[0] {
					return original(replacement)
				}
				return original(path)
			}
			if timing == "after-scan" {
				s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
					rows, err := db.Query(query, args...)
					if err != nil {
						return nil, err
					}
					return syncedChunksAfterScanRows{sqlRowScanner{rows}, func() { changed = true }}, nil
				}
			}
			if _, err := s.GetSyncedChunks(); !errors.Is(err, ErrDatabaseGenerationChanged) {
				t.Fatalf("replacement was not rejected: %v", err)
			}
		})
	}
}

type syncedChunksAfterScanRows struct {
	rowScanner
	afterScan func()
}

func (r syncedChunksAfterScanRows) Scan(dest ...any) error {
	err := r.rowScanner.Scan(dest...)
	if err == nil {
		r.afterScan()
	}
	return err
}

func TestSyncedChunksPropagatesReadErrors(t *testing.T) {
	for _, kind := range []string{"query", "scan", "iteration"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestStore(t)
			failure := errors.New("synthetic " + kind + " failure")
			rows := &fakeRows{next: []bool{false}, err: failure}
			if kind == "scan" {
				rows = &fakeRows{next: []bool{true}, scanErr: failure}
			}
			s.hooks.queryIt = func(queryer, string, ...any) (rowScanner, error) {
				if kind == "query" {
					return nil, failure
				}
				return rows, nil
			}
			if _, err := s.GetSyncedChunks(); !errors.Is(err, failure) {
				t.Fatalf("lost %s error: %v", kind, err)
			}
			if kind != "query" && !rows.closed {
				t.Fatal("failed result iterator was not closed")
			}
		})
	}
}
