package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func compatibilityHash(t *testing.T, dir string) [32]byte {
	t.Helper()
	var b bytes.Buffer
	err := filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		b.WriteString(path)
		info, err := e.Info()
		if err != nil {
			return err
		}
		b.WriteString(info.Mode().String())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			b.Write(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b.Bytes())
}

func TestCompatibilityOfflineNeverAuthorizes(t *testing.T) {
	for _, name := range []string{"fresh", "current", "legacy", "future", "malformed", "missing_identity", "invalid_identity", "wal", "shm", "journal", "ambiguous", "symlink"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name string, data []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name != "fresh" {
				header := make([]byte, 512)
				copy(header, "SQLite format 3\x00")
				binary.BigEndian.PutUint16(header[16:18], 512)
				header[18], header[19] = 1, 1
				schema := uint32(1)
				if name == "legacy" {
					schema = 0
				}
				if name == "future" {
					schema = 2
				}
				binary.BigEndian.PutUint32(header[60:64], schema)
				if name == "malformed" {
					header = []byte("not sqlite")
				}
				write("engram.db", header)
				if name != "missing_identity" {
					id := "0123456789abcdef0123456789abcdef\n"
					if name == "invalid_identity" {
						id = "broken"
					}
					write(".instance-id", []byte(id))
				}
				switch name {
				case "wal":
					write("engram.db-wal", nil)
				case "shm":
					write("engram.db-shm", nil)
				case "journal":
					write("engram.db-journal", nil)
				case "ambiguous":
					write("other.db", nil)
				case "symlink":
					if err := os.Symlink("engram.db", filepath.Join(dir, "alias.db")); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := compatibilityHash(t, dir)
			r, err := ProbeCompatibility(dir)
			valid := name == "fresh" || name == "current" || name == "legacy" || name == "future"
			if (err == nil) != valid {
				t.Fatalf("result=%+v err=%v", r, err)
			}
			if r.ActiveReuseAuthorized || r.OfflineAutomaticOpeningAuthorized {
				t.Fatalf("authorized offline: %+v", r)
			}
			if name == "current" && r.Status != "unknown_shape" {
				t.Fatalf("header must not prove shape: %+v", r)
			}
			if after := compatibilityHash(t, dir); before != after {
				t.Fatal("fixture mutated")
			}
		})
	}
	for _, path := range []string{"relative", filepath.Join(t.TempDir(), "missing")} {
		if _, err := ProbeCompatibility(path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}

func TestCompatibilityActiveStartupFacts(t *testing.T) {
	for _, future := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "future"}[future], func(t *testing.T) {
			cfg := FallbackConfig(t.TempDir())
			if future {
				db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("PRAGMA user_version=2"); err != nil {
					t.Fatal(err)
				}
				_ = db.Close()
			}
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			count := migrateRunCount.Load()
			before := compatibilityHash(t, cfg.DataDir)
			r := s.ActiveCompatibility()
			if r.ActiveReuseAuthorized == future {
				t.Fatalf("wrong startup authorization: %+v", r)
			}
			if !future && (r.InstanceID != s.InstanceID() || len(r.RuntimeID) != 32 || r.Capability == "") {
				t.Fatalf("unbound response: %+v", r)
			}
			if count != migrateRunCount.Load() || before != compatibilityHash(t, cfg.DataDir) {
				t.Fatal("active read changed store")
			}
			_ = s.Close()
			if s.ActiveCompatibility().ActiveReuseAuthorized {
				t.Fatal("closed store authorized")
			}
		})
	}
}
