package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInspectLocalProjectCanceled(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := InspectLocalProject(ctx, root)
	if !errors.Is(err, context.Canceled) || result.Project != "" {
		t.Fatalf("result=%+v error=%v, want no identity and cancellation", result, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled inspection changed fixture: entries=%v error=%v", entries, err)
	}
}

func TestDetectionContextStopsChildScan(t *testing.T) {
	root := t.TempDir()
	// Git does not use the current directory itself as an ancestor ceiling.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	if err := os.Mkdir(filepath.Join(root, "ordinary-child"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := childScanReadDir
	t.Cleanup(func() { childScanReadDir = original })
	childScanReadDir = func(dir *os.File, count int) ([]os.DirEntry, error) {
		entries, err := original(dir, count)
		cancel()
		return entries, err
	}
	result := DetectProjectFullWithOptions(root, DetectionOptions{Context: ctx, InspectOnly: true})
	if !errors.Is(result.Error, context.Canceled) || result.Project != "" {
		t.Fatalf("result=%+v, want no identity after cancellation during scan", result)
	}
	// Omitting the new context preserves the existing detector's behavior even
	// while an unrelated caller's context is canceled by the same test seam.
	legacy := DetectProjectFull(root)
	if legacy.Error != nil || legacy.Source != SourceDirBasename || legacy.Project != fallbackProjectName(root) {
		t.Fatalf("ordinary detection changed: %+v", legacy)
	}
}

func TestInspectLocalProjectScanBudget(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	original := childScanNow
	t.Cleanup(func() { childScanNow = original })
	start := time.Now()
	calls := 0
	childScanNow = func() time.Time {
		calls++
		if calls == 1 {
			return start
		}
		return start.Add(childScanTimeout + time.Millisecond)
	}
	result, err := InspectLocalProject(context.Background(), root)
	if !errors.Is(err, context.DeadlineExceeded) || result.Project != "" {
		t.Fatalf("result=%+v error=%v, want no identity after incomplete scan", result, err)
	}
	calls = 0
	legacy := DetectProjectFull(root)
	if legacy.Error != nil || legacy.Source != SourceDirBasename {
		t.Fatalf("ordinary scan-budget fallback changed: %+v", legacy)
	}
}
