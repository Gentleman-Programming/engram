package daemon

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestMain doubles as the lock-holder child for the interprocess test: with
// ENGRAM_DAEMON_STARTLOCK_CHILD=1 the test binary acquires the start lock on
// the data dir from ENGRAM_DATA_DIR, announces itself on stdout, and holds
// the lock until the parent kills it.
func TestMain(m *testing.M) {
	if os.Getenv("ENGRAM_DAEMON_STARTLOCK_CHILD") == "1" {
		if _, err := AcquireStartLock(os.Getenv("ENGRAM_DATA_DIR"), 0); err != nil {
			os.Exit(3)
		}
		fmt.Println("locked")
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(m.Run())
}

// TestStartLockExcludesSecondHolder pins the mutual-exclusion contract within
// one process: a held start lock rejects a second acquisition and a release
// makes the lock acquirable again.
func TestStartLockExcludesSecondHolder(t *testing.T) {
	dataDir := t.TempDir()
	if base := filepath.Base(StartLockPath(dataDir)); base != "serve.lock" {
		t.Fatalf("start lock base = %q, want serve.lock", base)
	}
	first, err := AcquireStartLock(dataDir, 0)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := AcquireStartLock(dataDir, 100*time.Millisecond); !errors.Is(err, ErrStartLockHeld) {
		t.Fatalf("second acquire err = %v, want ErrStartLockHeld", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	again, err := AcquireStartLock(dataDir, 0)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
}

// TestStartLockInterprocessHold pins the interprocess contract: a lock held
// by another process blocks acquisition here, and the kernel releases it when
// the holder dies, so a crashed starter can never wedge the data directory.
func TestStartLockInterprocessHold(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	dataDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	child := exec.Command(executable)
	child.Env = append(os.Environ(), "ENGRAM_DAEMON_STARTLOCK_CHILD=1", "ENGRAM_DATA_DIR="+dataDir)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe child stdout: %v", err)
	}
	if err := child.Start(); err != nil {
		t.Fatalf("start lock child: %v", err)
	}
	locked := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() || scanner.Text() != "locked" {
			locked <- fmt.Errorf("child announcement = %q", scanner.Text())
			return
		}
		locked <- nil
	}()
	select {
	case err := <-locked:
		if err != nil {
			t.Fatalf("lock child: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
		t.Fatal("lock child never announced the held lock")
	}

	if _, err := AcquireStartLock(dataDir, 200*time.Millisecond); !errors.Is(err, ErrStartLockHeld) {
		t.Fatalf("acquire against child-held lock err = %v, want ErrStartLockHeld", err)
	}
	// Killing the holder releases the lock at the kernel level; acquisition
	// must then succeed within the bounded wait.
	if err := child.Process.Kill(); err != nil {
		t.Fatalf("kill lock child: %v", err)
	}
	if _, err := child.Process.Wait(); err != nil {
		t.Fatalf("reap lock child: %v", err)
	}
	after, err := AcquireStartLock(dataDir, 5*time.Second)
	if err != nil {
		t.Fatalf("acquire after holder death: %v", err)
	}
	if err := after.Release(); err != nil {
		t.Fatalf("release after holder death: %v", err)
	}
}
