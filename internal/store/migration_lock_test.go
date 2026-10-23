package store

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Exercise the real CLI, not a test-only constructor seam. The test owns the
// contended lock throughout EOF and shutdown; releasing it cannot mask failure.
func TestStartupCancellationOwnedLockEOF(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "engram.exe")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/engram")
	build.Env = startupCancellationEnvironment()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build public executable: %v\n%s", err, out)
	}
	for _, name := range []string{".instance-id.lock", ".migrate.lock"} {
		t.Run(name, func(t *testing.T) {
			cfg := FallbackConfig(t.TempDir())
			fixture, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.Close(); err != nil {
				t.Fatal(err)
			}
			// The Windows CLI prepares its documented SQLite temp tree before startup.
			if runtime.GOOS == "windows" {
				if err := os.MkdirAll(filepath.Join(cfg.DataDir, "tmp", "sqlite"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			// Windows exclusive byte-range locks reject reads from other handles.
			// Snapshot the complete fixture only outside the held-lock interval.
			before := startupCancellationSnapshot(t, cfg.DataDir)
			func() {
				path := filepath.Join(cfg.DataDir, name)
				unlock, err := acquireMigrationLock(path)
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
				child := exec.Command(binary, "serve", "--session-owned")
				child.Env = append(startupCancellationEnvironment(), "ENGRAM_DATA_DIR="+cfg.DataDir, "ENGRAM_TOKEN="+strings.Repeat("a", 64), "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir())
				stdin, err := child.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				var stdout, stderr bytes.Buffer
				child.Stdout = &stdout
				child.Stderr = &stderr
				if err := child.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- child.Wait() }()
				// Give the public process time to enter the held startup-lock wait.
				select {
				case err := <-done:
					t.Fatalf("exited before EOF: %v\n%s", err, stderr.String())
				case <-time.After(time.Second):
				}
				closedAt := time.Now()
				if err := stdin.Close(); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("EOF exit: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
					}
				case <-time.After(3 * time.Second):
					_ = child.Process.Kill()
					<-done
					t.Fatalf("owned startup remained alive with %s held after EOF; stdout=%s stderr=%s", name, stdout.String(), stderr.String())
				}
				t.Logf("EOF exit while %s remains held: %s; stdout=%q stderr=%q", name, time.Since(closedAt), stdout.String(), stderr.String())
				if stdout.Len() != 0 {
					t.Fatalf("cancelled startup emitted readiness: %s", stdout.String())
				}
				probe, err := os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = probe.Close() }()
				acquired, err := tryLockMigrationFile(probe)
				if acquired {
					_ = unlockMigrationFile(probe)
					t.Fatal("child released the test's held lock")
				}
				if err != nil {
					t.Fatal(err)
				}
			}()
			if after := startupCancellationSnapshot(t, cfg.DataDir); !reflect.DeepEqual(before, after) {
				t.Fatal("cancelled lock wait changed fixture")
			}
		})
	}
}

func startupCancellationEnvironment() []string {
	var env []string
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if !strings.HasPrefix(key, "ENGRAM_") && !strings.HasPrefix(key, "GENTLE_") {
			env = append(env, entry)
		}
	}
	return env
}

type startupCancellationEntry struct {
	kind fs.FileMode
	data string
}

func startupCancellationSnapshot(t *testing.T, dir string) map[string]startupCancellationEntry {
	t.Helper()
	result := make(map[string]startupCancellationEntry)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		snapshot := startupCancellationEntry{kind: entry.Type()}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot.data = string(data)
		}
		result[rel] = snapshot
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestStartupCancellationSnapshotTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tmp", "sqlite"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"empty": "", filepath.Join("tmp", "sqlite", "probe"): "\x00fixture\xff"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]startupCancellationEntry{
		".":                                     {kind: fs.ModeDir},
		"tmp":                                   {kind: fs.ModeDir},
		filepath.Join("tmp", "sqlite"):          {kind: fs.ModeDir},
		"empty":                                 {},
		filepath.Join("tmp", "sqlite", "probe"): {data: "\x00fixture\xff"},
	}
	if got := startupCancellationSnapshot(t, dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot tree = %#v; want %#v", got, want)
	}
}
