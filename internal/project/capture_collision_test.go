package project

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFirstImplicitGitProjectCollisionConcurrentCheckouts(t *testing.T) {
	dataDir := t.TempDir()
	first := DetectionResult{Project: "customer-solution", repositoryID: strings.Repeat("a", 32)}
	second := DetectionResult{Project: first.Project, repositoryID: strings.Repeat("b", 32)}
	var warnings atomic.Int32
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, checkout := range []DetectionResult{first, second} {
		for range 4 {
			workers.Go(func() {
				<-start
				if checkout.FirstImplicitGitProjectCollision(dataDir) {
					warnings.Add(1)
				}
			})
		}
	}
	close(start)
	workers.Wait()
	if got := warnings.Load(); got != 1 {
		t.Fatalf("got %d warnings, want one warning for the later identity", got)
	}
	if first.FirstImplicitGitProjectCollision(dataDir) || second.FirstImplicitGitProjectCollision(dataDir) {
		t.Fatal("warning repeated after the first conflict")
	}
	if second.FirstImplicitGitProjectCollision(t.TempDir()) {
		t.Fatal("another store inherited the first store's identities")
	}
}

func TestFirstImplicitGitProjectCollisionUnavailableMetadata(t *testing.T) {
	first := DetectionResult{Project: "customer-solution", repositoryID: strings.Repeat("a", 32)}
	second := DetectionResult{Project: first.Project, repositoryID: strings.Repeat("b", 32)}
	t.Run("invalid owner", func(t *testing.T) {
		dataDir := t.TempDir()
		if first.FirstImplicitGitProjectCollision(dataDir) {
			t.Fatal("initial checkout should be quiet")
		}
		owners, err := filepath.Glob(filepath.Join(dataDir, "git-project-advisories-v1", "*", "first-checkout"))
		if err != nil || len(owners) != 1 {
			t.Fatalf("expected one advisory owner, got %v, %v", owners, err)
		}
		if err := os.WriteFile(owners[0], []byte("invalid"), 0o600); err != nil {
			t.Fatal(err)
		}
		if second.FirstImplicitGitProjectCollision(dataDir) {
			t.Fatal("invalid owner should not establish a collision")
		}
	})
	t.Run("data directory is a file", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "blocked")
		if err := os.WriteFile(dataDir, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if first.FirstImplicitGitProjectCollision(dataDir) {
			t.Fatal("unavailable advisory directory should not establish a collision")
		}
	})
}
