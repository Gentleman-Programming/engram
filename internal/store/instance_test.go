package store

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEnsureInstanceIDRecoversMalformedAndTruncatedFiles(t *testing.T) {
	for name, contents := range map[string]string{
		"empty":     "",
		"truncated": strings.Repeat("a", 31),
		"uppercase": strings.Repeat("A", 32),
		"non-hex":   strings.Repeat("g", 32),
	} {
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			path := filepath.Join(dataDir, ".instance-id")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("write malformed identity: %v", err)
			}

			id, err := EnsureInstanceID(dataDir)
			if err != nil {
				t.Fatalf("EnsureInstanceID: %v", err)
			}
			if !isValidInstanceID(id) {
				t.Fatalf("recovered identity = %q, want 32 lowercase hexadecimal characters", id)
			}
			persisted, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read recovered identity: %v", err)
			}
			if strings.TrimSpace(string(persisted)) != id {
				t.Fatalf("persisted identity = %q, want %q", persisted, id)
			}
		})
	}
}

func TestEnsureInstanceIDPreservesValidID(t *testing.T) {
	dataDir := t.TempDir()
	want := "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(dataDir, ".instance-id"), []byte(want+"\n"), 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}

	got, err := EnsureInstanceID(dataDir)
	if err != nil {
		t.Fatalf("EnsureInstanceID: %v", err)
	}
	if got != want {
		t.Fatalf("identity = %q, want %q", got, want)
	}
}

func TestEnsureInstanceIDSerializesConcurrentRecoveryAndPublication(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, ".instance-id"), []byte("truncated"), 0o600); err != nil {
		t.Fatalf("write truncated identity: %v", err)
	}

	const callers = 32
	start := make(chan struct{})
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, err := EnsureInstanceID(dataDir)
			ids <- id
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)

	var winner string
	for err := range errs {
		if err != nil {
			t.Fatalf("EnsureInstanceID: %v", err)
		}
	}
	for id := range ids {
		if !isValidInstanceID(id) {
			t.Fatalf("identity = %q, want 32 lowercase hexadecimal characters", id)
		}
		if winner == "" {
			winner = id
		} else if id != winner {
			t.Fatalf("concurrent identity = %q, want published winner %q", id, winner)
		}
	}
}

func TestEnsureInstanceIDRejectsRegularFileDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dataDir, []byte("regular file"), 0o600); err != nil {
		t.Fatalf("write data directory fixture: %v", err)
	}

	if _, err := EnsureInstanceID(dataDir); err == nil {
		t.Fatal("EnsureInstanceID accepted a regular file as data directory")
	}
}
