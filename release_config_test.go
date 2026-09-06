package engram_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseChecksModuleMetadataWithoutMutatingTaggedSources(t *testing.T) {
	root := releaseConfigRepoRoot(t)
	goreleaser := releaseConfigFile(t, filepath.Join(root, ".goreleaser.yaml"))
	if strings.Contains(goreleaser, "go mod tidy") {
		t.Fatal(".goreleaser.yaml must not run go mod tidy during a release")
	}

	workflow := releaseConfigFile(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	setupGo := strings.Index(workflow, "- name: Set up Go")
	tidyCheck := strings.Index(workflow, "run: go mod tidy -diff")
	goreleaserStep := strings.Index(workflow, "- name: Run GoReleaser")
	if setupGo == -1 || tidyCheck == -1 || goreleaserStep == -1 {
		t.Fatal("release workflow must set up Go, check tidy module metadata, and run GoReleaser")
	}
	if !(setupGo < tidyCheck && tidyCheck < goreleaserStep) {
		t.Fatal("release workflow must check tidy module metadata after Go setup and before GoReleaser")
	}
}

func releaseConfigRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

func releaseConfigFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
