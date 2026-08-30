package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireDatabaseGenerationMoveLocksSamePathAndOppositeOrder(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatalf("create first directory: %v", err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatalf("create second directory: %v", err)
	}

	release, err := AcquireDatabaseGenerationMoveLocks(first, filepath.Join(first, "."))
	if err != nil {
		t.Fatalf("acquire same-path move lock: %v", err)
	}
	release()

	done := make(chan error, 2)
	for _, pair := range [][2]string{{first, second}, {second, first}} {
		pair := pair
		go func() {
			release, err := AcquireDatabaseGenerationMoveLocks(pair[0], pair[1])
			if err == nil {
				release()
			}
			done <- err
		}()
	}
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("oppositely ordered lock acquisition: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("oppositely ordered move locks deadlocked")
		}
	}
}

func TestAcquireDatabaseGenerationMoveLocksHonorsCrossProcessSourceLease(t *testing.T) {
	if os.Getenv("ENGRAM_TEST_GENERATION_LOCK_CHILD") == "1" {
		source := os.Getenv("ENGRAM_TEST_GENERATION_LOCK_SOURCE")
		ready := os.Getenv("ENGRAM_TEST_GENERATION_LOCK_READY")
		release, err := acquireStoreGenerationLease(filepath.Join(source, ".generation.lock"))
		if err != nil {
			t.Fatalf("child acquire source lease: %v", err)
		}
		defer release()
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			t.Fatalf("child signal readiness: %v", err)
		}
		time.Sleep(time.Second)
		return
	}

	source := filepath.Join(t.TempDir(), "source")
	destination := filepath.Join(t.TempDir(), "destination")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatalf("create destination directory: %v", err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	child := exec.Command(os.Args[0], "-test.run=^TestAcquireDatabaseGenerationMoveLocksHonorsCrossProcessSourceLease$")
	child.Env = append(os.Environ(),
		"ENGRAM_TEST_GENERATION_LOCK_CHILD=1",
		"ENGRAM_TEST_GENERATION_LOCK_SOURCE="+source,
		"ENGRAM_TEST_GENERATION_LOCK_READY="+ready,
	)
	if err := child.Start(); err != nil {
		t.Fatalf("start lock child: %v", err)
	}
	t.Cleanup(func() { _ = child.Wait() })
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cross-process source lease did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	originalTimeout := migrationLockTimeout
	migrationLockTimeout = 100 * time.Millisecond
	t.Cleanup(func() { migrationLockTimeout = originalTimeout })
	if release, err := AcquireDatabaseGenerationMoveLocks(source, destination); err == nil {
		release()
		t.Fatal("move lock acquired while another process held the source Store lease")
	}
}
