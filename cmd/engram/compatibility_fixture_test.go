package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompatibilityFixtureSurvivesConsumerCleanup(t *testing.T) {
	var first string
	t.Run("first consumer", func(t *testing.T) {
		first = buildCompatibilityBinary(t)
	})
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("fixture removed with first consumer: %v", err)
	}
	t.Run("second consumer", func(t *testing.T) {
		if second := buildCompatibilityBinary(t); second != first {
			t.Fatalf("rebuilt identical CLI: first=%q second=%q", first, second)
		}
		out, err := exec.Command(first, "version").CombinedOutput()
		if err != nil || !strings.Contains(string(out), compatibilityFixtureVersion) {
			t.Fatalf("candidate fixture version: %v %s", err, out)
		}
	})
}

func TestCompatibilityFixtureConcurrentBuildAndCleanup(t *testing.T) {
	f := newCompatibilityBinaryFixture(os.TempDir())
	f.tempRoot = t.TempDir()
	t.Cleanup(func() {
		if err := f.close(); err != nil {
			t.Error(err)
		}
	})
	// Consumers can change their profile and cwd without changing build identity.
	t.Chdir(t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	type result struct {
		path string
		err  error
	}
	results := make(chan result, 8)
	for range cap(results) {
		go func() {
			path, err := f.get()
			results <- result{path, err}
		}()
	}
	var path string
	for range cap(results) {
		r := <-results
		if r.err != nil {
			t.Error(r.err)
			continue
		}
		if path != "" && r.path != path {
			t.Errorf("concurrent consumers received different builds: %q %q", path, r.path)
		}
		path = r.path
	}
	if t.Failed() {
		t.FailNow()
	}
	if !filepath.IsAbs(path) || filepath.Dir(filepath.Dir(path)) != f.tempRoot {
		t.Fatalf("fixture escaped package-owned root: %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := f.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
		t.Fatalf("owned fixture not removed: %v", err)
	}
}

func TestCompatibilityFixtureBuildFailureIsSticky(t *testing.T) {
	f := newCompatibilityBinaryFixture(os.TempDir())
	f.tempRoot = t.TempDir()
	sourceDir := f.sourceDir
	f.sourceDir = filepath.Join(t.TempDir(), "missing-source")
	t.Cleanup(func() {
		if err := f.close(); err != nil {
			t.Error(err)
		}
	})
	path, firstErr := f.get()
	if firstErr == nil || path != "" {
		t.Fatalf("failed build accepted: path=%q err=%v", path, firstErr)
	}
	// A later caller cannot turn a failed initialization into a different build.
	f.sourceDir = sourceDir
	path, err := f.get()
	if path != "" || err != firstErr {
		t.Fatalf("build failure retried or lost: path=%q err=%v want=%v", path, err, firstErr)
	}
}

func TestCompatibilityFixtureRelativeTemporaryRoot(t *testing.T) {
	relative, err := os.MkdirTemp(".", ".cli-relative-test-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(absolute); err != nil {
			t.Error(err)
		}
	})
	f := newCompatibilityBinaryFixture(relative)
	t.Cleanup(func() {
		if err := f.close(); err != nil {
			t.Error(err)
		}
	})
	path, err := f.get()
	if err != nil {
		t.Fatal(err)
	}
	// Public CLI consumers execute from their own working directories.
	command := exec.Command(path, "version")
	command.Dir = t.TempDir()
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), compatibilityFixtureVersion) {
		t.Fatalf("fixture with relative temp root: %v %s", err, output)
	}
	if !filepath.IsAbs(path) || filepath.Dir(filepath.Dir(path)) != absolute {
		t.Fatalf("fixture path is not rooted at captured absolute directory: %q", path)
	}
}
