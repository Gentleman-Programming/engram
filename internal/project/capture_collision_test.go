package project

import (
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
