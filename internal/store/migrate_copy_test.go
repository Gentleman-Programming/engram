package store

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func copySnapshot(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			r, _ := filepath.Rel(root, p)
			out[r] = sha256.Sum256(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func copyTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
func copyFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(copyTempDir(t), "source")
	s, e := New(Config{DataDir: dir})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`INSERT INTO sync_state(target_key,last_error) VALUES ('copy-proof','not-exported')`); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"copy":"preserved"}`), 0600); e != nil {
		t.Fatal(e)
	}
	return dir
}

// Exercise actual filesystem identities and missing path ancestors on every OS,
// even when no case alias is available. Distinct case spellings are not assumed
// to be aliases: the sibling control uses a separate real directory identity.
func TestMigrateCopyPhysicalAncestry(t *testing.T) {
	root := copyTempDir(t)
	source := filepath.Join(root, "source")
	sibling := filepath.Join(root, "sibling")
	for _, dir := range []string{source, sibling} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	handle, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := handle.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err = handle.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		within bool
	}{
		{source, true},
		{filepath.Join(source, "missing", "deeper"), true},
		{filepath.Join(sibling, "missing"), false},
		{filepath.Join(root, "missing"), false},
	} {
		within, err := copyPhysicalWithin(identity, tc.path)
		if err != nil || within != tc.within {
			t.Errorf("identity ancestry for %s: got %v, %v; want %v", tc.path, within, err, tc.within)
		}
		if tc.path != source {
			if _, err = os.Lstat(tc.path); !os.IsNotExist(err) {
				t.Errorf("ancestry check created output: %v", err)
			}
		}
	}
}

func TestMigrateCopyDistinctCaseDirectories(t *testing.T) {
	source := copyFixture(t)
	sibling := filepath.Join(filepath.Dir(source), "SOURCE")
	if _, err := os.Lstat(sibling); err == nil {
		t.Skip("filesystem exposes a case alias instead of distinct case directories")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	siblingInfo, err := os.Lstat(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(sourceInfo, siblingInfo) {
		t.Fatal("distinct fixture directories unexpectedly share identity")
	}
	before, err := copyManifest(source)
	if err != nil {
		t.Fatal(err)
	}
	result, err := MigrateCopy(source, filepath.Join(sibling, "copy"), true)
	if err != nil || result == nil || !result.MigrationCompleted {
		t.Fatalf("distinct case directories rejected: %+v %v", result, err)
	}
	after, err := copyManifest(source)
	if err != nil || !copyManifestsEqual(before, after) {
		t.Fatalf("source changed: %v", err)
	}
}

func TestMigrateCopyPreservesCompleteDirectory(t *testing.T) {
	src := copyFixture(t)
	before := copySnapshot(t, src)
	dst := filepath.Join(copyTempDir(t), "copy")
	r, e := MigrateCopy(src, dst, true)
	if e != nil {
		t.Fatal(e)
	}
	if !r.MigrationCompleted || r.SourceDir != src || r.OutputDir != dst || r.InstanceID == "" {
		t.Fatalf("bad evidence: %+v", r)
	}
	if !reflect.DeepEqual(before, copySnapshot(t, src)) {
		t.Fatal("source changed")
	}
	b, e := os.ReadFile(filepath.Join(dst, "config.json"))
	if e != nil || string(b) != `{"copy":"preserved"}` {
		t.Fatalf("config: %s %v", b, e)
	}
	s, e := New(Config{DataDir: dst})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var v string
	if e = s.db.QueryRow(`SELECT last_error FROM sync_state WHERE target_key='copy-proof'`).Scan(&v); e != nil || v != "not-exported" {
		t.Fatalf("state: %s %v", v, e)
	}
}

// This fixture derives the historical catalog from the locally inspected
// v3.3.2 -> b81ea5e migration diff; it is not an executed v3.3.2 binary fixture.
func TestMigrateCopyHistoricalCatalog(t *testing.T) {
	src := copyFixture(t)
	s, err := New(Config{DataDir: src})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`DROP TABLE session_finalizations;
 ALTER TABLE observations DROP COLUMN superseded_by_sync_id;
 ALTER TABLE sync_delete_tombstones DROP COLUMN superseded_by_sync_id;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	before := copySnapshot(t, src)
	dst := filepath.Join(copyTempDir(t), "copy")
	result, err := MigrateCopy(src, dst, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MigrationCompleted || !reflect.DeepEqual(before, copySnapshot(t, src)) {
		t.Fatal("historical source changed")
	}
	migrated, err := New(Config{DataDir: dst})
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if _, err = migrated.db.Exec(`SELECT superseded_by_sync_id FROM observations; SELECT superseded_by_sync_id FROM sync_delete_tombstones; SELECT * FROM session_finalizations`); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateCopyLogicalWAL(t *testing.T) {
	for _, rule := range []string{"state", "future", "unknown"} {
		t.Run(rule, func(t *testing.T) {
			src := copyFixture(t)
			s, err := New(Config{DataDir: src})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				t.Fatal(err)
			}
			base, err := os.ReadFile(filepath.Join(src, "engram.db"))
			if err != nil {
				t.Fatal(err)
			}
			sql := `INSERT INTO sync_state(target_key,last_error) VALUES ('wal-only','preserved')`
			if rule == "future" {
				sql = "PRAGMA user_version=99"
			}
			if rule == "unknown" {
				sql = "CREATE TABLE wal_only_future(id TEXT)"
			}
			if _, err = s.db.Exec(sql); err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			// Restore the checkpointed base; the new logical state exists only in WAL.
			if err = os.WriteFile(filepath.Join(src, "engram.db"), base, 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(src, "engram.db-shm"), []byte("source SHM must not be reused"), 0600); err != nil {
				t.Fatal(err)
			}
			before := copySnapshot(t, src)
			dst := filepath.Join(copyTempDir(t), "copy")
			result, err := MigrateCopy(src, dst, true)
			if !reflect.DeepEqual(before, copySnapshot(t, src)) {
				t.Fatal("WAL source changed")
			}
			if rule != "state" {
				if err == nil || result != nil {
					t.Fatalf("WAL-only unsupported state accepted: %+v %v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			copied, err := New(Config{DataDir: dst})
			if err != nil {
				t.Fatal(err)
			}
			defer copied.Close()
			var v string
			if err = copied.db.QueryRow(`SELECT last_error FROM sync_state WHERE target_key='wal-only'`).Scan(&v); err != nil || v != "preserved" {
				t.Fatalf("WAL data lost: %q %v", v, err)
			}
		})
	}
}

func TestMigrateCopyRejects(t *testing.T) {
	for _, rule := range []string{"confirmation", "relative", "existing", "inside", "symlink", "source-alias", "output-alias", "existing-file", "identity", "journal", "future", "unknown", "unknown-column", "unknown-trigger", "missing-table", "corrupt"} {
		t.Run(rule, func(t *testing.T) {
			src := copyFixture(t)
			dst := filepath.Join(copyTempDir(t), "copy")
			confirm := true
			switch rule {
			case "confirmation":
				confirm = false
			case "relative":
				dst = "relative"
			case "existing":
				if e := os.Mkdir(dst, 0700); e != nil {
					t.Fatal(e)
				}
			case "inside":
				dst = filepath.Join(src, "copy")
			case "symlink":
				if e := os.Symlink("config.json", filepath.Join(src, "link")); e != nil {
					t.Fatal(e)
				}
			case "source-alias":
				alias := filepath.Join(copyTempDir(t), "alias")
				if e := os.Symlink(src, alias); e != nil {
					t.Fatal(e)
				}
				src = alias
			case "output-alias":
				parent := filepath.Join(copyTempDir(t), "alias")
				if e := os.Symlink(copyTempDir(t), parent); e != nil {
					t.Fatal(e)
				}
				dst = filepath.Join(parent, "copy")
			case "existing-file":
				if e := os.WriteFile(dst, []byte("caller-owned"), 0600); e != nil {
					t.Fatal(e)
				}
			case "identity":
				if e := os.WriteFile(filepath.Join(src, ".instance-id"), []byte("invalid"), 0600); e != nil {
					t.Fatal(e)
				}
			case "journal":
				if e := os.WriteFile(filepath.Join(src, "engram.db-journal"), []byte("hot"), 0600); e != nil {
					t.Fatal(e)
				}
			case "future", "unknown", "unknown-column", "unknown-trigger", "missing-table":
				s, e := New(Config{DataDir: src})
				if e != nil {
					t.Fatal(e)
				}
				q := "PRAGMA user_version=99"
				switch rule {
				case "unknown":
					q = "CREATE TABLE future_state(id TEXT)"
				case "unknown-column":
					q = "ALTER TABLE sessions ADD COLUMN future_state TEXT"
				case "unknown-trigger":
					q = "CREATE TRIGGER future_trigger AFTER INSERT ON sessions BEGIN SELECT 1; END"
				case "missing-table":
					q = "DROP TABLE sync_state"
				}
				if _, e = s.db.Exec(q); e != nil {
					t.Fatal(e)
				}
				if e = s.Close(); e != nil {
					t.Fatal(e)
				}
			case "corrupt":
				if e := os.WriteFile(filepath.Join(src, "engram.db"), []byte("not sqlite"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			snapshotRoot := src
			if rule == "source-alias" {
				var e error
				snapshotRoot, e = filepath.EvalSymlinks(src)
				if e != nil {
					t.Fatal(e)
				}
			}
			before := copySnapshot(t, snapshotRoot)
			r, e := MigrateCopy(src, dst, confirm)
			if e == nil || r != nil {
				t.Fatalf("accepted %s: %+v %v", rule, r, e)
			}
			if !reflect.DeepEqual(before, copySnapshot(t, snapshotRoot)) {
				t.Fatal("source changed")
			}
			if rule == "future" || rule == "unknown" || rule == "unknown-column" || rule == "unknown-trigger" || rule == "missing-table" || rule == "corrupt" {
				if !strings.Contains(e.Error(), "incomplete output retained") {
					t.Fatalf("partial output not identified: %v", e)
				}
			}
			if rule == "existing-file" {
				b, err := os.ReadFile(dst)
				if err != nil || string(b) != "caller-owned" {
					t.Fatalf("caller output changed: %q %v", b, err)
				}
			}
			if rule == "confirmation" || rule == "relative" || rule == "inside" || rule == "symlink" || rule == "source-alias" || rule == "output-alias" || rule == "identity" || rule == "journal" {
				if _, e = os.Lstat(dst); !os.IsNotExist(e) {
					t.Fatalf("destination created: %v", e)
				}
			}
		})
	}
}
