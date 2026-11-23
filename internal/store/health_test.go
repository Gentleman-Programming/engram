package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckReadinessConnectionIsReadOnlyAndCancellable(t *testing.T) {
	s, err := New(Config{DataDir: filepath.Join(t.TempDir(), "store # path")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err = s.CheckReadiness(t.Context()); err != nil {
		t.Fatal(err)
	}
	db := s.readinessDB
	if _, err = db.ExecContext(t.Context(), "CREATE TABLE invalid_readiness_write (id INTEGER)"); err == nil {
		t.Fatal("readiness connection accepted a write")
	}
	held, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	before := db.Stats().WaitCount
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if err = s.CheckReadiness(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readiness waiting on its own pool: %v", err)
	}
	if db.Stats().WaitCount <= before {
		t.Fatal("readiness did not enter its own pool wait")
	}
}

func TestCheckReadinessCoreSchema(t *testing.T) {
	for _, broken := range []string{"", "sessions", "observations", "user_prompts", "project", "created_at", "deleted_at"} {
		t.Run(broken, func(t *testing.T) {
			s := newTestStore(t)
			switch broken {
			case "sessions", "observations", "user_prompts":
				if _, err := s.db.Exec("DROP TABLE " + broken); err != nil {
					t.Fatal(err)
				}
			case "project", "created_at", "deleted_at":
				fields := map[string]string{"project": "'alpha' AS project", "created_at": "'2026-01-01' AS created_at", "deleted_at": "NULL AS deleted_at"}
				delete(fields, broken)
				columns := "1 AS fixture"
				for _, field := range []string{"project", "created_at", "deleted_at"} {
					if expr, ok := fields[field]; ok {
						columns += "," + expr
					}
				}
				if _, err := s.db.Exec("ALTER TABLE observations RENAME TO saved_observations; CREATE VIEW observations AS SELECT " + columns); err != nil {
					t.Fatal(err)
				}
			}
			_, statsErr := s.Stats()
			err := s.CheckReadiness(context.Background())
			if (err != nil) != (statsErr != nil) || (err != nil) != (broken != "") {
				t.Fatalf("schema%q readiness=%v stats=%v", broken, err, statsErr)
			}
		})
	}
}

func TestCheckReadinessPopulatedAndTombstoned(t *testing.T) {
	s := newTestStore(t)
	_, err := s.db.Exec(`INSERT INTO sessions(id,project,directory) VALUES('health','alpha',''); INSERT INTO observations(session_id,type,title,content,project) VALUES('health','decision','title','body','alpha'); INSERT INTO user_prompts(session_id,content) VALUES('health','prompt')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckReadiness(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE observations SET deleted_at='2026-01-01'`); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckReadiness(context.Background()); err != nil {
		t.Fatal("all tombstones remain a readable store", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckReadiness(context.Background()); err == nil {
		t.Fatal("closed store accepted")
	}
}

func TestCheckReadinessGenerationFence(t *testing.T) {
	// statFile is package-global. Simulate drift using real owned-file identity;
	// leave SQLite's open files in place for portable Windows cleanup.
	for _, kind := range []string{"identity", "missing-db", "missing-wal", "missing-shm", "transient"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("health", "alpha", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.CheckReadiness(context.Background()); err != nil {
				t.Fatal(err)
			}
			original := statFile
			t.Cleanup(func() { statFile = original })
			replacement := filepath.Join(t.TempDir(), "replacement")
			if err := os.WriteFile(replacement, []byte("owned identity"), 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(replacement)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.DataDir(), "engram.db")
			if kind == "missing-wal" {
				path += "-wal"
			}
			if kind == "missing-shm" {
				path += "-shm"
			}
			transient := errors.New("synthetic transient stat failure")
			statFile = func(name string) (os.FileInfo, error) {
				if name == path {
					switch kind {
					case "identity":
						return info, nil
					case "transient":
						return nil, transient
					default:
						return nil, os.ErrNotExist
					}
				}
				return original(name)
			}
			err = s.CheckReadiness(context.Background())
			if kind == "transient" {
				if !errors.Is(err, transient) {
					t.Fatalf("transient error=%v", err)
				}
				if s.GenerationError() != nil {
					t.Fatal("transient failure poisoned generation")
				}
			} else {
				assertGenerationChanged(t, err)
				assertGenerationChanged(t, s.GenerationError())
			}
			statFile = original
			err = s.CheckReadiness(context.Background())
			if kind == "transient" {
				if err != nil {
					t.Fatal("transient failure did not recover", err)
				}
			} else {
				assertGenerationChanged(t, err)
			}
		})
	}
}
