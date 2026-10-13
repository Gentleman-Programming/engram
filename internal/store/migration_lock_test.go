package store

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exercise the real CLI, not a test-only constructor seam. The test owns the
// contended lock throughout EOF and shutdown; releasing it cannot mask failure.
func TestStartupCancellationOwnedLockEOF(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "engram")
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
			path := filepath.Join(cfg.DataDir, name)
			unlock, err := acquireMigrationLock(path)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			before := startupCancellationSnapshot(t, cfg.DataDir)
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
				_ = <-done
				t.Fatalf("owned startup remained alive with %s held after EOF; stdout=%s stderr=%s", name, stdout.String(), stderr.String())
			}
			t.Logf("EOF exit while %s remains held: %s; stdout=%q stderr=%q", name, time.Since(closedAt), stdout.String(), stderr.String())
			if stdout.Len() != 0 {
				t.Fatalf("cancelled startup emitted readiness: %s", stdout.String())
			}
			if after := startupCancellationSnapshot(t, cfg.DataDir); !reflect.DeepEqual(before, after) {
				t.Fatal("cancelled lock wait changed fixture")
			}
			probe, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer probe.Close()
			acquired, err := tryLockMigrationFile(probe)
			if acquired {
				_ = unlockMigrationFile(probe)
				t.Fatal("child released the test's held lock")
			}
			if err != nil {
				t.Fatal(err)
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

func startupCancellationSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("unexpected fixture directory: %s", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = string(data)
	}
	return result
}
